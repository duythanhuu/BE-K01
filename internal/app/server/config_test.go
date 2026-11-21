package server

import "testing"

func TestLoadConfig_Defaults(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("REDIS_ADDR", "")
	t.Setenv("REDIS_PASSWORD", "")

	c := LoadConfig()

	if c.HTTPAddr != ":8080" || c.RedisAddr != "localhost:6379" || c.RedisPassword != "" {
		t.Fatalf("defaults sai: %+v", c)
	}
}

func TestLoadConfig_FromEnv(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":9999")
	t.Setenv("REDIS_ADDR", "redis:6380")
	t.Setenv("REDIS_PASSWORD", "s3cret")

	c := LoadConfig()

	if c.HTTPAddr != ":9999" || c.RedisAddr != "redis:6380" || c.RedisPassword != "s3cret" {
		t.Fatalf("đọc env sai: %+v", c)
	}
}
