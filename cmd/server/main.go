// server là web server Gin + Redis của bài tập.
//
// Cấu hình qua biến môi trường: HTTP_ADDR, REDIS_ADDR, REDIS_PASSWORD, COOKIE_SECURE.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/EngineerProOrg/BE-K01/internal/app/server"
	"github.com/redis/go-redis/v9"
)

func main() {
	cfg := server.LoadConfig()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Mỗi bước (mở kết nối, gửi lệnh, chờ trả lời) chỉ chờ tối đa 1s và lệnh chỉ
	// thử lại 1 lần, để Redis chậm/chết thì request lỗi (503) sau cỡ 1s thay vì
	// treo 3-5s như mặc định.
	// Lưu ý: pool của go-redis còn tự thử mở lại kết nối nên thực tế đo được
	// khoảng 0,8s khi Redis chết, chưa phải tức thì.
	rdb := redis.NewClient(&redis.Options{
		Addr:         cfg.RedisAddr,
		Password:     cfg.RedisPassword,
		DialTimeout:  time.Second,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
		MaxRetries:   1,
	})
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		// Chưa fatal: /healthz sẽ báo 503 cho tới khi Redis sẵn sàng.
		log.Printf("CẢNH BÁO: chưa kết nối được Redis tại %s: %v", cfg.RedisAddr, err)
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           server.NewRouter(server.Deps{Redis: rdb, CookieSecure: cfg.CookieSecure}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("lắng nghe tại %s", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("đang tắt server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
