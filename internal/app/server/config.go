// Package server dựng HTTP server (Gin) và cấu hình chạy.
package server

import "os"

// Config là cấu hình đọc từ biến môi trường.
type Config struct {
	HTTPAddr      string // địa chỉ lắng nghe, mặc định :8080
	RedisAddr     string // host:port của Redis, mặc định localhost:6379
	RedisPassword string // mật khẩu Redis (có thể rỗng khi chạy local không đặt requirepass)
}

// LoadConfig đọc cấu hình từ môi trường, dùng giá trị mặc định khi biến không được đặt.
func LoadConfig() Config {
	return Config{
		HTTPAddr:      getenv("HTTP_ADDR", ":8080"),
		RedisAddr:     getenv("REDIS_ADDR", "localhost:6379"),
		RedisPassword: os.Getenv("REDIS_PASSWORD"),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
