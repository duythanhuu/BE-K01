package ping

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestLock(t *testing.T, ttl time.Duration) (*Lock, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewLock(rdb, ttl), mr
}

func TestLock_AcquireIsExclusive(t *testing.T) {
	l, _ := newTestLock(t, 10*time.Second)
	ctx := context.Background()

	token, ok, err := l.Acquire(ctx)
	if err != nil || !ok || token == "" {
		t.Fatalf("lần 1: token=%q ok=%v err=%v, want giành được", token, ok, err)
	}

	token2, ok2, err2 := l.Acquire(ctx)
	if err2 != nil || ok2 || token2 != "" {
		t.Fatalf("lần 2: token=%q ok=%v err=%v, want không giành được (đang bị giữ)", token2, ok2, err2)
	}
}

func TestLock_ReleaseThenAcquireAgain(t *testing.T) {
	l, mr := newTestLock(t, 10*time.Second)
	ctx := context.Background()
	token, _, _ := l.Acquire(ctx)

	released, err := l.Release(ctx, token)

	if err != nil || !released {
		t.Fatalf("Release = %v, %v; want true, nil", released, err)
	}
	if mr.Exists(lockKey) {
		t.Fatal("khoá vẫn còn trong Redis sau khi nhả")
	}
	if _, ok, _ := l.Acquire(ctx); !ok {
		t.Fatal("không giành lại được khoá sau khi nhả")
	}
}

// Chỉ chủ khoá mới nhả được: token sai không xoá được khoá.
func TestLock_ReleaseWithWrongTokenKeepsLock(t *testing.T) {
	l, mr := newTestLock(t, 10*time.Second)
	ctx := context.Background()
	_, _, _ = l.Acquire(ctx)

	released, err := l.Release(ctx, "token-cua-nguoi-khac")

	if err != nil || released {
		t.Fatalf("Release = %v, %v; want false, nil", released, err)
	}
	if !mr.Exists(lockKey) {
		t.Fatal("token sai mà vẫn xoá được khoá của người khác")
	}
}

// Kịch bản mà Lua sinh ra để chặn: khoá của A hết hạn, B giành được, rồi A (chậm) mới nhả.
// A không được xoá khoá của B.
func TestLock_ExpiredHolderCannotReleaseNewOwnersLock(t *testing.T) {
	l, mr := newTestLock(t, 5*time.Second)
	ctx := context.Background()
	tokenA, _, _ := l.Acquire(ctx)

	mr.FastForward(6 * time.Second) // khoá của A hết hạn
	tokenB, okB, _ := l.Acquire(ctx)
	if !okB {
		t.Fatal("B phải giành được khoá sau khi khoá của A hết hạn")
	}

	releasedA, err := l.Release(ctx, tokenA) // A tới muộn

	if err != nil || releasedA {
		t.Fatalf("A nhả được khoá của B: released=%v err=%v", releasedA, err)
	}
	if got, _ := mr.Get(lockKey); got != tokenB {
		t.Fatalf("khoá đã bị đổi: %q, want token của B %q", got, tokenB)
	}
}

// Lưới an toàn khi người giữ khoá chết mà không kịp nhả.
func TestLock_ExpiresByTTL(t *testing.T) {
	l, mr := newTestLock(t, 10*time.Second)
	ctx := context.Background()
	_, _, _ = l.Acquire(ctx)

	if ttl := mr.TTL(lockKey); ttl != 10*time.Second {
		t.Fatalf("TTL = %v, want 10s", ttl)
	}
	mr.FastForward(11 * time.Second)

	if _, ok, _ := l.Acquire(ctx); !ok {
		t.Fatal("khoá phải tự hết hạn và giành lại được")
	}
}

func TestLock_ReleaseOfExpiredLockIsNotAnError(t *testing.T) {
	l, mr := newTestLock(t, time.Second)
	ctx := context.Background()
	token, _, _ := l.Acquire(ctx)
	mr.FastForward(2 * time.Second)

	released, err := l.Release(ctx, token)

	if err != nil || released {
		t.Fatalf("Release = %v, %v; want false, nil", released, err)
	}
}

// Nhiều người cùng giành một lúc: đúng một người được.
func TestLock_ConcurrentAcquireExactlyOneWins(t *testing.T) {
	l, _ := newTestLock(t, 10*time.Second)
	const n = 50

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, ok, err := l.Acquire(context.Background())
			if err != nil {
				t.Errorf("lỗi bất ngờ: %v", err)
				return
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if wins != 1 {
		t.Fatalf("có %d người giành được khoá, want đúng 1", wins)
	}
}

func TestLock_RedisDownReturnsError(t *testing.T) {
	l, mr := newTestLock(t, time.Second)
	mr.Close()

	token, ok, err := l.Acquire(context.Background())

	if err == nil || ok || token != "" {
		t.Fatalf("token=%q ok=%v err=%v, want lỗi kết nối (không phải 'đang bận')", token, ok, err)
	}
}

func TestNewToken_UniqueAndLongEnough(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		tok, err := newToken()
		if err != nil {
			t.Fatal(err)
		}
		if len(tok) != 32 || seen[tok] {
			t.Fatalf("token %q trùng hoặc sai độ dài", tok)
		}
		seen[tok] = true
	}
}
