package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestStore(t *testing.T) (*UserStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewUserStore(rdb), mr
}

func TestUserStore_Create(t *testing.T) {
	s, mr := newTestStore(t)
	s.now = func() time.Time { return time.Date(2026, 11, 21, 3, 4, 5, 0, time.UTC) }

	if err := s.Create(context.Background(), "alice", "HASH"); err != nil {
		t.Fatal(err)
	}

	if got := mr.HGet("user:alice", "password_hash"); got != "HASH" {
		t.Errorf("password_hash = %q, want HASH", got)
	}
	if got := mr.HGet("user:alice", "created_at"); got != "2026-11-21T03:04:05Z" {
		t.Errorf("created_at = %q", got)
	}
}

func TestUserStore_Create_Duplicate(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()

	if err := s.Create(ctx, "alice", "FIRST"); err != nil {
		t.Fatal(err)
	}
	err := s.Create(ctx, "alice", "SECOND")

	if !errors.Is(err, ErrUserExists) {
		t.Fatalf("err = %v, want ErrUserExists", err)
	}
	// Lần đăng ký trùng không được ghi đè tài khoản cũ.
	if got := mr.HGet("user:alice", "password_hash"); got != "FIRST" {
		t.Errorf("password_hash bị ghi đè thành %q", got)
	}
}

// Nhiều request cùng đăng ký một username: đúng một request thành công.
func TestUserStore_Create_ConcurrentSameUsername(t *testing.T) {
	s, _ := newTestStore(t)
	const n = 30

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		success int
		exists  int
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.Create(context.Background(), "alice", "HASH")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				success++
			case errors.Is(err, ErrUserExists):
				exists++
			default:
				t.Errorf("lỗi bất ngờ: %v", err)
			}
		}()
	}
	wg.Wait()

	if success != 1 || exists != n-1 {
		t.Fatalf("success=%d exists=%d, want 1 và %d", success, exists, n-1)
	}
}

func TestUserStore_Create_RedisDown(t *testing.T) {
	s, mr := newTestStore(t)
	mr.Close()

	err := s.Create(context.Background(), "alice", "HASH")

	if err == nil || errors.Is(err, ErrUserExists) {
		t.Fatalf("err = %v, want lỗi kết nối (không phải ErrUserExists)", err)
	}
}
