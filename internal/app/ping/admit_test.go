package ping

import (
	"context"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestAdmitter(t *testing.T) (*RedisAdmitter, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewAdmitter(rdb), mr
}

func TestAdmitter_CountsPerUser(t *testing.T) {
	a, mr := newTestAdmitter(t)
	ctx := context.Background()

	for want := int64(1); want <= 3; want++ {
		got, err := a.Admit(ctx, "alice")
		if err != nil || got.Count != want {
			t.Fatalf("alice lần %d: Count=%d err=%v, want %d", want, got.Count, err, want)
		}
	}
	got, _ := a.Admit(ctx, "bob")

	if got.Count != 1 {
		t.Errorf("bob lần đầu: Count=%d, want 1 (bộ đếm mỗi người độc lập)", got.Count)
	}
	if s, _ := mr.ZScore(countKey, "alice"); s != 3 {
		t.Errorf("ZSCORE alice = %v, want 3", s)
	}
	if s, _ := mr.ZScore(countKey, "bob"); s != 1 {
		t.Errorf("ZSCORE bob = %v, want 1", s)
	}
}

// Nhiều lần ghi đồng thời không được mất lượt đếm.
func TestAdmitter_ConcurrentIncrementsAreNotLost(t *testing.T) {
	a, mr := newTestAdmitter(t)
	const n = 100

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.Admit(context.Background(), "alice"); err != nil {
				t.Errorf("lỗi bất ngờ: %v", err)
			}
		}()
	}
	wg.Wait()

	if s, _ := mr.ZScore(countKey, "alice"); s != n {
		t.Fatalf("ZSCORE alice = %v, want %d", s, n)
	}
}

func TestAdmitter_RedisDownReturnsError(t *testing.T) {
	a, mr := newTestAdmitter(t)
	mr.Close()

	if _, err := a.Admit(context.Background(), "alice"); err == nil {
		t.Fatal("want lỗi khi Redis chết")
	}
}
