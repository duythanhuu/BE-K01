package ping

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// fakeClock là đồng hồ giả: test tự quyết định "bây giờ là lúc nào".
type fakeClock struct {
	mu   sync.Mutex
	base time.Time
	off  time.Duration
	err  error
}

func newFakeClock() *fakeClock {
	return &fakeClock{base: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// At đặt thời gian hiện tại là base + d.
func (c *fakeClock) At(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.off = d
}

func (c *fakeClock) Now(context.Context) (time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return time.Time{}, c.err
	}
	return c.base.Add(c.off), nil
}

const manyLimit = 1_000_000 // giới hạn đủ lớn để test không dính rate limit

func newTestAdmitter(t *testing.T, clock Clock, limit int) (*RedisAdmitter, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewAdmitter(rdb, clock, limit, 0), mr
}

func sec(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

// ---------- đếm ----------

func TestAdmitter_CountsPerUser(t *testing.T) {
	a, mr := newTestAdmitter(t, newFakeClock(), manyLimit)
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
}

// Nhiều lần ghi đồng thời không được mất lượt đếm.
func TestAdmitter_ConcurrentIncrementsAreNotLost(t *testing.T) {
	a, mr := newTestAdmitter(t, newFakeClock(), manyLimit)
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
	a, mr := newTestAdmitter(t, newFakeClock(), manyLimit)
	mr.Close()

	_, err := a.Admit(context.Background(), "alice")

	var rl *RateLimitedError
	if err == nil || errors.As(err, &rl) {
		t.Fatalf("err = %v, want lỗi kết nối (không phải RateLimitedError)", err)
	}
}

func TestAdmitter_ClockErrorIsReturned(t *testing.T) {
	fc := newFakeClock()
	fc.err = errors.New("đồng hồ hỏng")
	a, _ := newTestAdmitter(t, fc, manyLimit)

	if _, err := a.Admit(context.Background(), "alice"); err == nil {
		t.Fatal("want lỗi khi không đọc được giờ")
	}
}

// ---------- rate limit cửa sổ trượt ----------

func TestRateLimit_AllowsLimitThenDenies(t *testing.T) {
	fc := newFakeClock()
	a, _ := newTestAdmitter(t, fc, DefaultLimit)
	ctx := context.Background()

	for i := 1; i <= DefaultLimit; i++ {
		if _, err := a.Admit(ctx, "alice"); err != nil {
			t.Fatalf("lần %d phải được chấp nhận: %v", i, err)
		}
	}
	_, err := a.Admit(ctx, "alice")

	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("lần %d: err = %v, want RateLimitedError", DefaultLimit+1, err)
	}
	if rl.RetryAfter != DefaultWindow {
		t.Errorf("RetryAfter = %v, want %v (cả hai lần ping vừa xảy ra)", rl.RetryAfter, DefaultWindow)
	}
}

// RetryAfter = thời điểm lần ping cũ nhất rời khỏi cửa sổ, trừ đi bây giờ.
func TestRateLimit_RetryAfterCountsDown(t *testing.T) {
	fc := newFakeClock()
	a, _ := newTestAdmitter(t, fc, DefaultLimit)
	ctx := context.Background()

	fc.At(0)
	_, _ = a.Admit(ctx, "alice")
	fc.At(sec(10))
	_, _ = a.Admit(ctx, "alice")
	fc.At(sec(20))
	_, err := a.Admit(ctx, "alice")

	var rl *RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter != sec(40) {
		t.Fatalf("err = %v, want RetryAfter = 40s (lần cũ nhất ở giây 0 rời cửa sổ ở giây 60)", err)
	}
}

// Ca biên mà cửa sổ cố định xử lý sai: 2 lần ở giây 59 rồi gọi tiếp ở giây 61.
// Cửa sổ cố định [0,60) rồi [60,120) sẽ cho phép (4 lần trong 2 giây); cửa sổ trượt thì chặn.
func TestRateLimit_SlidingWindowBoundary(t *testing.T) {
	fc := newFakeClock()
	a, _ := newTestAdmitter(t, fc, DefaultLimit)
	ctx := context.Background()

	fc.At(sec(59))
	if _, err := a.Admit(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	fc.At(sec(59.1))
	if _, err := a.Admit(ctx, "alice"); err != nil {
		t.Fatal(err)
	}

	fc.At(sec(61)) // sang "cửa sổ cố định kế tiếp", nhưng vẫn là 2 lần trong 60s gần nhất
	_, err := a.Admit(ctx, "alice")
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("giây 61: err = %v, want RateLimitedError (đã 2 lần trong 60s gần nhất)", err)
	}
	if rl.RetryAfter != sec(58) {
		t.Errorf("RetryAfter = %v, want 58s (lần ở giây 59 rời cửa sổ ở giây 119)", rl.RetryAfter)
	}

	fc.At(sec(119.05)) // lần ở giây 59 vừa rời cửa sổ, lần ở 59.1 vẫn còn → còn 1 lượt
	if _, err := a.Admit(ctx, "alice"); err != nil {
		t.Fatalf("giây 119.05: err = %v, want được chấp nhận", err)
	}
	fc.At(sec(119.06))
	if _, err := a.Admit(ctx, "alice"); !errors.As(err, &rl) {
		t.Fatalf("giây 119.06: err = %v, want RateLimitedError (59.1 và 119.05 đều còn trong cửa sổ)", err)
	}
}

// Lần ping cách đúng 60 giây đã nằm ngoài cửa sổ.
func TestRateLimit_ExactlyOneWindowLaterIsAllowed(t *testing.T) {
	fc := newFakeClock()
	a, _ := newTestAdmitter(t, fc, DefaultLimit)
	ctx := context.Background()
	fc.At(0)
	_, _ = a.Admit(ctx, "alice")
	_, _ = a.Admit(ctx, "alice")

	fc.At(sec(59.999))
	var rl *RateLimitedError
	if _, err := a.Admit(ctx, "alice"); !errors.As(err, &rl) {
		t.Fatalf("giây 59.999: err = %v, want RateLimitedError", err)
	}
	fc.At(sec(60))
	if _, err := a.Admit(ctx, "alice"); err != nil {
		t.Fatalf("giây 60.000: err = %v, want được chấp nhận", err)
	}
}

// Lần bị từ chối không tiêu hao hạn ngạch và không được đếm: spam khi đang bị chặn
// không làm cửa sổ trôi dài thêm.
func TestRateLimit_DeniedAttemptsDoNotConsumeQuotaOrCount(t *testing.T) {
	fc := newFakeClock()
	a, mr := newTestAdmitter(t, fc, DefaultLimit)
	ctx := context.Background()
	fc.At(0)
	_, _ = a.Admit(ctx, "alice")
	_, _ = a.Admit(ctx, "alice")

	for i := 1; i <= 10; i++ {
		fc.At(sec(float64(i)))
		if _, err := a.Admit(ctx, "alice"); err == nil {
			t.Fatalf("lần spam %d phải bị từ chối", i)
		}
	}

	if members, _ := mr.ZMembers(rateKeyPrefix + "alice"); len(members) != 2 {
		t.Errorf("ping:rl:alice có %d phần tử, want 2 (lần bị từ chối không được ghi)", len(members))
	}
	if s, _ := mr.ZScore(countKey, "alice"); s != 2 {
		t.Errorf("ping:count alice = %v, want 2", s)
	}
	fc.At(sec(60)) // hai lần đầu (giây 0) vừa rời cửa sổ dù đã spam 10 lần
	if _, err := a.Admit(ctx, "alice"); err != nil {
		t.Fatalf("giây 60: err = %v, want được chấp nhận (spam không được làm trễ việc mở lại)", err)
	}
}

func TestRateLimit_UsersAreIndependent(t *testing.T) {
	fc := newFakeClock()
	a, _ := newTestAdmitter(t, fc, DefaultLimit)
	ctx := context.Background()
	_, _ = a.Admit(ctx, "alice")
	_, _ = a.Admit(ctx, "alice")
	var rl *RateLimitedError
	if _, err := a.Admit(ctx, "alice"); !errors.As(err, &rl) {
		t.Fatal("alice phải bị chặn")
	}

	if _, err := a.Admit(ctx, "bob"); err != nil {
		t.Fatalf("bob bị ảnh hưởng bởi hạn ngạch của alice: %v", err)
	}
}

func TestRateLimit_CleansUpOldEntriesAndSetsExpiry(t *testing.T) {
	fc := newFakeClock()
	a, mr := newTestAdmitter(t, fc, DefaultLimit)
	ctx := context.Background()
	fc.At(0)
	_, _ = a.Admit(ctx, "alice")
	_, _ = a.Admit(ctx, "alice")

	if ttl := mr.TTL(rateKeyPrefix + "alice"); ttl != DefaultWindow+time.Second {
		t.Errorf("TTL = %v, want %v (dọn key của người không ping nữa)", ttl, DefaultWindow+time.Second)
	}
	fc.At(sec(100))
	_, _ = a.Admit(ctx, "alice")

	if members, _ := mr.ZMembers(rateKeyPrefix + "alice"); len(members) != 1 {
		t.Fatalf("còn %d phần tử, want 1 (hai lần cũ đã rời cửa sổ và bị xoá)", len(members))
	}
}

// Hai lần ghi cùng một mili giây không được trùng member (nếu trùng, ZADD ghi đè và mất một lượt).
func TestRateLimit_SameMillisecondEntriesDoNotCollide(t *testing.T) {
	fc := newFakeClock()
	a, mr := newTestAdmitter(t, fc, 5)
	ctx := context.Background()
	fc.At(0)
	for i := 0; i < 3; i++ {
		if _, err := a.Admit(ctx, "alice"); err != nil {
			t.Fatal(err)
		}
	}

	if members, _ := mr.ZMembers(rateKeyPrefix + "alice"); len(members) != 3 {
		t.Fatalf("ping:rl:alice có %d phần tử, want 3", len(members))
	}
}

func TestNewAdmitter_Defaults(t *testing.T) {
	a := NewAdmitter(redis.NewClient(&redis.Options{Addr: "localhost:0"}), nil, 0, 0)

	if a.limit != 2 || a.window != 60*time.Second {
		t.Fatalf("limit=%d window=%v, want 2 và 60s (đúng yêu cầu của đề)", a.limit, a.window)
	}
	if _, ok := a.clock.(RedisClock); !ok {
		t.Errorf("clock mặc định là %T, want RedisClock", a.clock)
	}
}

// Clock mặc định lấy giờ từ Redis (để nhiều instance không bị lệch đồng hồ).
func TestRedisClock_UsesRedisTime(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	want := time.Date(2030, 5, 6, 7, 8, 9, 0, time.UTC)
	mr.SetTime(want)

	got, err := RedisClock{rdb: rdb}.Now(context.Background())

	if err != nil || got.Unix() != want.Unix() {
		t.Fatalf("Now = %v, %v; want %v", got, err, want)
	}
}
