package server

import "testing"

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

func TestLoadConfig_InvalidCookieSecureFallsBackToFalse(t *testing.T) {
	t.Setenv("COOKIE_SECURE", "có")

	if LoadConfig().CookieSecure {
		t.Fatal("giá trị COOKIE_SECURE không hợp lệ phải coi là false")
	}
}
