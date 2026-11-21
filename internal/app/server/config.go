// Package server dựng HTTP server (Gin) và cấu hình chạy.
package server

import (
	"os"
	"strconv"
	"time"
)

// Config là cấu hình đọc từ biến môi trường.
type Config struct {
	HTTPAddr      string // địa chỉ lắng nghe, mặc định :8080
	RedisAddr     string // host:port của Redis, mặc định localhost:6379
	RedisPassword string // mật khẩu Redis (có thể rỗng khi chạy local không đặt requirepass)
	CookieSecure  bool   // COOKIE_SECURE: chỉ gửi cookie session qua HTTPS; mặc định false (dev http://localhost)

	// PingSleep (PING_SLEEP, ví dụ "5s"): thời gian /ping xử lý giả lập. Mặc định 5s theo đề;
	// đặt nhỏ hơn (ví dụ "200ms") để chạy thử nhanh.
	PingSleep time.Duration
}

// LoadConfig đọc cấu hình từ môi trường, dùng giá trị mặc định khi biến không được đặt.
func LoadConfig() Config {
	secure, _ := strconv.ParseBool(os.Getenv("COOKIE_SECURE")) // giá trị rỗng/sai → false
	sleep, err := time.ParseDuration(os.Getenv("PING_SLEEP"))
	if err != nil || sleep <= 0 {
		sleep = 0 // rỗng/sai/không dương → để /ping dùng mặc định 5s
	}
	return Config{
		HTTPAddr:      getenv("HTTP_ADDR", ":8080"),
		RedisAddr:     getenv("REDIS_ADDR", "localhost:6379"),
		RedisPassword: os.Getenv("REDIS_PASSWORD"),
		CookieSecure:  secure,
		PingSleep:     sleep,
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
