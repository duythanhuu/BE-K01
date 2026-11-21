package ping

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/EngineerProOrg/BE-K01/internal/app/auth"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const (
	DefaultSleep   = 5 * time.Second  // thời gian /ping "xử lý" theo đề
	defaultLockTTL = 10 * time.Second // phải lớn hơn thời gian xử lý
	releaseTimeout = 2 * time.Second

	// statusClientClosed là mã không chuẩn (theo nginx) chỉ để log cho rõ khi client ngắt kết nối.
	statusClientClosed = 499
)

// Options cấu hình Handler. Giá trị 0 nghĩa là dùng mặc định.
type Options struct {
	Sleep   time.Duration // thời gian xử lý giả lập; mặc định 5s
	LockTTL time.Duration // TTL của khoá; mặc định 10s, luôn lớn hơn Sleep

	Limit  int           // số lần ping tối đa mỗi người trong Window; mặc định 2
	Window time.Duration // độ dài cửa sổ rate limit; mặc định 60s
	Clock  Clock         // nguồn thời gian cho rate limit; mặc định là giờ của Redis (test thay bằng đồng hồ giả)
}

// Handler phục vụ /ping.
type Handler struct {
	lock     *Lock
	admitter Admitter
	sleep    time.Duration
}

func NewHandler(rdb redis.Cmdable, opt Options) *Handler {
	sleep := opt.Sleep
	if sleep <= 0 {
		sleep = DefaultSleep
	}
	ttl := opt.LockTTL
	if ttl <= 0 {
		ttl = defaultLockTTL
	}
	// Khoá hết hạn trong lúc đang xử lý thì hai người có thể cùng chạy: giữ TTL > sleep.
	if ttl <= sleep {
		ttl = 2 * sleep
	}
	return &Handler{lock: NewLock(rdb, ttl), admitter: NewAdmitter(rdb, opt.Clock, opt.Limit, opt.Window), sleep: sleep}
}

// Routes đăng ký route. r phải là nhóm route đã gắn auth.RequireAuth().
func (h *Handler) Routes(r gin.IRoutes) {
	r.GET("/ping", h.ping)
}

// ping: GET /ping (cần đăng nhập)
//
//	200 {"message":"pong","username","count"}  chạy xong sau ~5s; count = số lần ping của người này
//	429 {"error":"busy"}                       đang có người khác ping
//	429 {"error":"rate_limited","retry_after_seconds"} + header Retry-After: đã ping 2 lần trong 60s
//	503 {"error":"unavailable"}                Redis lỗi
func (h *Handler) ping(c *gin.Context) {
	username, _ := auth.UsernameFrom(c)
	ctx := c.Request.Context()

	token, acquired, err := h.lock.Acquire(ctx)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	if !acquired {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "busy"})
		return
	}
	// Luôn nhả khoá khi rời hàm: xong việc, client ngắt kết nối, hay panic.
	defer h.release(token)

	// Ghi nhận ngay khi được chấp nhận, trước khi sleep: lần ping này tính là một lần
	// gọi kể cả khi client ngắt kết nối giữa chừng. Request bị 429 ở trên không tới đây.
	adm, err := h.admitter.Admit(ctx, username)
	var limited *RateLimitedError
	switch {
	case err == nil:
	case errors.As(err, &limited):
		// Khoá được nhả bởi defer ngay khi rời hàm: người khác ping được liền.
		secs := int(math.Ceil(limited.RetryAfter.Seconds()))
		if secs < 1 {
			secs = 1
		}
		c.Header("Retry-After", strconv.Itoa(secs))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate_limited", "retry_after_seconds": secs})
		return
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}

	timer := time.NewTimer(h.sleep)
	defer timer.Stop()
	select {
	case <-timer.C:
		c.JSON(http.StatusOK, gin.H{"message": "pong", "username": username, "count": adm.Count})
	case <-ctx.Done():
		// Client đã ngắt: không cần giữ khoá thêm cho tới hết thời gian xử lý.
		c.AbortWithStatus(statusClientClosed)
	}
}

// release nhả khoá bằng context riêng: context của request có thể đã bị huỷ
// (client ngắt) mà khoá vẫn phải được nhả.
func (h *Handler) release(token string) {
	ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	if _, err := h.lock.Release(ctx, token); err != nil {
		// Không nhả được thì khoá tự hết hạn theo TTL.
		log.Printf("ping: không nhả được khoá (sẽ tự hết hạn): %v", err)
	}
}
