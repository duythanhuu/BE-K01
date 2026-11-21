package ping

import (
	"context"
	"log"
	"net/http"
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
}

// Handler phục vụ /ping.
type Handler struct {
	lock  *Lock
	sleep time.Duration
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
	return &Handler{lock: NewLock(rdb, ttl), sleep: sleep}
}

// Routes đăng ký route. r phải là nhóm route đã gắn auth.RequireAuth().
func (h *Handler) Routes(r gin.IRoutes) {
	r.GET("/ping", h.ping)
}

// ping: GET /ping (cần đăng nhập)
//
//	200 {"message":"pong","username"}  chạy xong sau ~5s
//	429 {"error":"busy"}               đang có người khác ping
//	503 {"error":"unavailable"}        Redis lỗi
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

	timer := time.NewTimer(h.sleep)
	defer timer.Stop()
	select {
	case <-timer.C:
		c.JSON(http.StatusOK, gin.H{"message": "pong", "username": username})
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
