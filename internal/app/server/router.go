package server

import (
	"context"
	"net/http"
	"time"

	"github.com/EngineerProOrg/BE-K01/internal/app/auth"
	"github.com/EngineerProOrg/BE-K01/internal/app/ping"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// Deps gom các phụ thuộc của router. Redis là redis.Cmdable để test thay được
// bằng miniredis mà không cần đổi code.
type Deps struct {
	Redis        redis.Cmdable
	CookieSecure bool          // cookie session chỉ gửi qua HTTPS
	PingSleep    time.Duration // thời gian /ping xử lý; 0 = mặc định 5s
}

// NewRouter dựng Gin engine với middleware chung và các route.
// Log mặc định của Gin chỉ ghi method, đường dẫn, mã trạng thái và thời gian,
// không ghi body nên không làm lộ mật khẩu ở các PR sau.
func NewRouter(d Deps) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), gin.Logger())

	r.GET("/healthz", healthz(d.Redis))

	authHandler := auth.NewHandler(
		auth.NewUserStore(d.Redis),
		auth.NewSessionStore(d.Redis, auth.DefaultSessionTTL),
		auth.CookieOptions{Secure: d.CookieSecure},
	)
	authHandler.Routes(r)

	// Các route dưới đây bắt buộc đăng nhập.
	authed := r.Group("", authHandler.RequireAuth())
	ping.NewHandler(d.Redis, ping.Options{Sleep: d.PingSleep}).Routes(authed)
	ping.NewStatsHandler(d.Redis).Routes(authed)
	return r
}

// healthz trả 200 khi Redis phản hồi PING, 503 khi không. Redis là nơi lưu
// duy nhất user và session nên "Redis chết" nghĩa là server không dùng được.
func healthz(rdb redis.Cmdable) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()

		if err := rdb.Ping(ctx).Err(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}
