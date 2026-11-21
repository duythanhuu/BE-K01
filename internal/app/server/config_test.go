package server

import (
	"testing"
	"time"
)

func TestLoadConfig_Defaults(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("REDIS_ADDR", "")
	t.Setenv("REDIS_PASSWORD", "")
	t.Setenv("COOKIE_SECURE", "")

	c := LoadConfig()

	if c.HTTPAddr != ":8080" || c.RedisAddr != "localhost:6379" || c.RedisPassword != "" || c.CookieSecure {
		t.Fatalf("defaults sai: %+v", c)
	}
}

func TestLoadConfig_FromEnv(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":9999")
	t.Setenv("REDIS_ADDR", "redis:6380")
	t.Setenv("REDIS_PASSWORD", "s3cret")
	t.Setenv("COOKIE_SECURE", "true")

	c := LoadConfig()

	if c.HTTPAddr != ":9999" || c.RedisAddr != "redis:6380" || c.RedisPassword != "s3cret" || !c.CookieSecure {
		t.Fatalf("đọc env sai: %+v", c)
	}
}

func TestLoadConfig_PingSleep(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"":      0, // 0 = để /ping dùng mặc định 5s
		"5s":    5 * time.Second,
		"250ms": 250 * time.Millisecond,
		"oops":  0,
		"-1s":   0,
		"0s":    0,
	} {
		t.Setenv("PING_SLEEP", in)
		if got := LoadConfig().PingSleep; got != want {
			t.Errorf("PING_SLEEP=%q → %v, want %v", in, got, want)
		}
	}
}

func TestLoadConfig_InvalidCookieSecureFallsBackToFalse(t *testing.T) {
	t.Setenv("COOKIE_SECURE", "có")

	if LoadConfig().CookieSecure {
		t.Fatal("giá trị COOKIE_SECURE không hợp lệ phải coi là false")
	}
}
