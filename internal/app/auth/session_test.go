package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestSessions(t *testing.T, ttl time.Duration) (*SessionStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewSessionStore(rdb, ttl), mr
}

func TestSessionStore_CreateAndGet(t *testing.T) {
	s, _ := newTestSessions(t, 30*time.Minute)
	ctx := context.Background()

	id, err := s.Create(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, id)

	if err != nil || got != "alice" {
		t.Fatalf("Get = %q, %v; want alice, nil", got, err)
	}
}

func TestSessionStore_IDFormat(t *testing.T) {
	s, _ := newTestSessions(t, time.Minute)

	id, _ := s.Create(context.Background(), "alice")

	// 32 byte base64url không đệm = 43 ký tự.
	if len(id) != 43 || strings.ContainsAny(id, "+/=") {
		t.Fatalf("id = %q (len %d), want 43 ký tự base64url", id, len(id))
	}
}

func TestSessionStore_IDsAreUnique(t *testing.T) {
	s, _ := newTestSessions(t, time.Minute)
	seen := map[string]bool{}

	for i := 0; i < 500; i++ {
		id, err := s.Create(context.Background(), "alice")
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("trùng session id ở lần %d", i)
		}
		seen[id] = true
	}
}

// Redis chỉ chứa SHA-256 của id, không chứa id gốc.
func TestSessionStore_StoresOnlyHashOfID(t *testing.T) {
	s, mr := newTestSessions(t, time.Minute)

	id, _ := s.Create(context.Background(), "alice")

	sum := sha256.Sum256([]byte(id))
	wantKey := "session:" + hex.EncodeToString(sum[:])
	if !mr.Exists(wantKey) {
		t.Fatalf("không thấy key %s; keys = %v", wantKey, mr.Keys())
	}
	for _, k := range mr.Keys() {
		if strings.Contains(k, id) {
			t.Errorf("key %q chứa session id gốc", k)
		}
	}
}

func TestSessionStore_UnknownID(t *testing.T) {
	s, _ := newTestSessions(t, time.Minute)

	_, err := s.Get(context.Background(), "không-có-session-này")

	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestSessionStore_Expires(t *testing.T) {
	s, mr := newTestSessions(t, 30*time.Minute)
	id, _ := s.Create(context.Background(), "alice")

	if ttl := mr.TTL(sessionKey(id)); ttl != 30*time.Minute {
		t.Fatalf("TTL sau khi tạo = %v, want 30m", ttl)
	}
	mr.FastForward(31 * time.Minute)

	_, err := s.Get(context.Background(), id)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("err = %v, want ErrSessionNotFound sau khi hết hạn", err)
	}
}

// Hết hạn kiểu trượt: mỗi lần Get gia hạn thêm TTL.
func TestSessionStore_GetRefreshesTTL(t *testing.T) {
	s, mr := newTestSessions(t, 30*time.Minute)
	ctx := context.Background()
	id, _ := s.Create(ctx, "alice")

	mr.FastForward(20 * time.Minute)
	if _, err := s.Get(ctx, id); err != nil { // gia hạn về 30m
		t.Fatal(err)
	}
	mr.FastForward(20 * time.Minute) // tổng 40m từ lúc tạo, nhưng mới 20m từ lần Get

	if _, err := s.Get(ctx, id); err != nil {
		t.Fatalf("session bị hết hạn dù vừa được gia hạn: %v", err)
	}
}

// Gia hạn không được "hồi sinh" session đã hết hạn.
func TestSessionStore_GetDoesNotResurrectExpired(t *testing.T) {
	s, mr := newTestSessions(t, time.Minute)
	ctx := context.Background()
	id, _ := s.Create(ctx, "alice")
	mr.FastForward(2 * time.Minute)

	_, _ = s.Get(ctx, id)

	if mr.Exists(sessionKey(id)) {
		t.Fatal("Get tạo lại key cho session đã hết hạn")
	}
}

func TestSessionStore_Delete(t *testing.T) {
	s, _ := newTestSessions(t, time.Minute)
	ctx := context.Background()
	id, _ := s.Create(ctx, "alice")

	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Get(ctx, id); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("err = %v, want ErrSessionNotFound sau khi xoá", err)
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("xoá session không tồn tại không được báo lỗi: %v", err)
	}
}

// Redis lỗi phải là lỗi hạ tầng, không được lẫn với "không tìm thấy".
func TestSessionStore_RedisDownIsNotNotFound(t *testing.T) {
	s, mr := newTestSessions(t, time.Minute)
	id, _ := s.Create(context.Background(), "alice")
	mr.Close()

	_, err := s.Get(context.Background(), id)

	if err == nil || errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("err = %v, want lỗi kết nối (không phải ErrSessionNotFound)", err)
	}
}
