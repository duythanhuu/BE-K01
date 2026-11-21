package ping

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// newRLEnv dựng môi trường với rate limit thật (2 lần / 60s) và đồng hồ giả.
func newRLEnv(t *testing.T) (*env, *fakeClock) {
	t.Helper()
	fc := newFakeClock()
	e := newEnv(t, Options{Sleep: time.Millisecond, Limit: DefaultLimit, Clock: fc})
	return e, fc
}

func retryAfter(w *httptest.ResponseRecorder) string { return w.Header().Get("Retry-After") }

func TestPingRateLimit_ThirdCallWithinWindowIsLimited(t *testing.T) {
	e, fc := newRLEnv(t)
	c := e.cookie(t, "alice")
	fc.At(0)

	for i := 1; i <= 2; i++ {
		if w := e.get(context.Background(), "/ping", c); w.Code != http.StatusOK {
			t.Fatalf("lần %d: status = %d, want 200", i, w.Code)
		}
	}
	w := e.get(context.Background(), "/ping", c)

	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), `"error":"rate_limited"`) {
		t.Fatalf("lần 3: status = %d body=%s, want 429 rate_limited", w.Code, w.Body)
	}
	if retryAfter(w) != "60" {
		t.Errorf("Retry-After = %q, want 60", retryAfter(w))
	}
	var body struct {
		Secs int `json:"retry_after_seconds"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Secs != 60 {
		t.Errorf("retry_after_seconds = %d, want 60", body.Secs)
	}
}

// Hai loại 429 phải phân biệt được (bận khác với hết hạn ngạch) và chỉ rate_limited có Retry-After.
func TestPingRateLimit_BusyAndRateLimitedAreDistinguishable(t *testing.T) {
	e := newEnv(t, Options{Sleep: 200 * time.Millisecond, Limit: DefaultLimit, Clock: newFakeClock()})
	alice, bob := e.cookie(t, "alice"), e.cookie(t, "bob")

	done := make(chan struct{})
	go func() { defer close(done); e.get(context.Background(), "/ping", alice) }()
	waitFor(t, "alice giữ khoá", func() bool { return e.mr.Exists(lockKey) })
	busy := e.get(context.Background(), "/ping", bob)
	<-done

	if !strings.Contains(busy.Body.String(), `"error":"busy"`) || retryAfter(busy) != "" {
		t.Fatalf("busy: body=%s Retry-After=%q, want error=busy và không có Retry-After", busy.Body, retryAfter(busy))
	}
}

func TestPingRateLimit_RetryAfterCountsDownAndRoundsUp(t *testing.T) {
	e, fc := newRLEnv(t)
	c := e.cookie(t, "alice")

	fc.At(0)
	e.get(context.Background(), "/ping", c)
	fc.At(sec(10))
	e.get(context.Background(), "/ping", c)

	fc.At(sec(20))
	if w := e.get(context.Background(), "/ping", c); retryAfter(w) != "40" {
		t.Errorf("giây 20: Retry-After = %q, want 40", retryAfter(w))
	}
	fc.At(sec(20.5)) // còn 39.5s → làm tròn lên 40, không được báo thiếu
	if w := e.get(context.Background(), "/ping", c); retryAfter(w) != "40" {
		t.Errorf("giây 20.5: Retry-After = %q, want 40 (làm tròn lên)", retryAfter(w))
	}
	fc.At(sec(59.9)) // còn 0.1s → ít nhất 1 giây
	if w := e.get(context.Background(), "/ping", c); retryAfter(w) != "1" {
		t.Errorf("giây 59.9: Retry-After = %q, want 1", retryAfter(w))
	}
}

func TestPingRateLimit_AllowedAgainAfterWindow(t *testing.T) {
	e, fc := newRLEnv(t)
	c := e.cookie(t, "alice")
	fc.At(0)
	e.get(context.Background(), "/ping", c)
	e.get(context.Background(), "/ping", c)

	fc.At(sec(61))
	w := e.get(context.Background(), "/ping", c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 sau khi qua cửa sổ", w.Code)
	}
}

// Ca biên đã nêu trong thiết kế: 2 lần ở giây 59, rồi giây 61 phải bị chặn.
func TestPingRateLimit_BoundaryAt59And61(t *testing.T) {
	e, fc := newRLEnv(t)
	c := e.cookie(t, "alice")

	fc.At(sec(59))
	e.get(context.Background(), "/ping", c)
	fc.At(sec(59.1))
	e.get(context.Background(), "/ping", c)

	fc.At(sec(61))
	if w := e.get(context.Background(), "/ping", c); w.Code != http.StatusTooManyRequests {
		t.Fatalf("giây 61: status = %d, want 429 (đã 2 lần trong 60s gần nhất)", w.Code)
	}
	fc.At(sec(119.2))
	if w := e.get(context.Background(), "/ping", c); w.Code != http.StatusOK {
		t.Fatalf("giây 119.2: status = %d, want 200", w.Code)
	}
}

// Bị rate limit thì: không tính vào đếm, và khoá được nhả ngay để người khác ping được.
func TestPingRateLimit_RejectedCallIsNotCountedAndReleasesLock(t *testing.T) {
	e, fc := newRLEnv(t)
	alice, bob := e.cookie(t, "alice"), e.cookie(t, "bob")
	fc.At(0)
	e.get(context.Background(), "/ping", alice)
	e.get(context.Background(), "/ping", alice)

	for i := 0; i < 5; i++ {
		if w := e.get(context.Background(), "/ping", alice); w.Code != http.StatusTooManyRequests {
			t.Fatalf("lần spam %d: status = %d, want 429", i+1, w.Code)
		}
	}

	if s, _ := e.mr.ZScore(countKey, "alice"); s != 2 {
		t.Errorf("ping:count alice = %v, want 2 (các lần bị 429 không được đếm)", s)
	}
	if e.mr.Exists(lockKey) {
		t.Error("khoá vẫn còn sau khi bị rate limit")
	}
	if w := e.get(context.Background(), "/ping", bob); w.Code != http.StatusOK {
		t.Errorf("bob: status = %d, want 200 (alice bị chặn không được giữ khoá)", w.Code)
	}
}

func TestPingRateLimit_UsersAreIndependent(t *testing.T) {
	e, fc := newRLEnv(t)
	alice, bob := e.cookie(t, "alice"), e.cookie(t, "bob")
	fc.At(0)
	e.get(context.Background(), "/ping", alice)
	e.get(context.Background(), "/ping", alice)

	if w := e.get(context.Background(), "/ping", alice); w.Code != http.StatusTooManyRequests {
		t.Fatalf("alice: status = %d, want 429", w.Code)
	}
	for i := 1; i <= 2; i++ {
		if w := e.get(context.Background(), "/ping", bob); w.Code != http.StatusOK {
			t.Fatalf("bob lần %d: status = %d, want 200", i, w.Code)
		}
	}
}

// Nhiều request đồng thời của cùng một người: không bao giờ vượt hạn ngạch,
// dù chúng xếp hàng qua khoá hay đụng nhau.
func TestPingRateLimit_ConcurrentSameUserNeverExceedsLimit(t *testing.T) {
	e := newEnv(t, Options{Sleep: 15 * time.Millisecond, Limit: DefaultLimit, Clock: newFakeClock()})
	c := e.cookie(t, "alice")
	const n = 20

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		codes = map[int]int{}
		start = make(chan struct{})
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w := e.get(context.Background(), "/ping", c)
			mu.Lock()
			codes[w.Code]++
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	if codes[http.StatusOK] > DefaultLimit {
		t.Fatalf("có %d request thành công, vượt giới hạn %d: %v", codes[http.StatusOK], DefaultLimit, codes)
	}
	if got, _ := e.mr.ZScore(countKey, "alice"); int(got) != codes[http.StatusOK] {
		t.Fatalf("ping:count = %v nhưng có %d request thành công", got, codes[http.StatusOK])
	}
	if codes[http.StatusOK]+codes[http.StatusTooManyRequests] != n {
		t.Fatalf("có mã trạng thái lạ: %v", codes)
	}
}

func TestNewHandler_DefaultRateLimitIsTwoPerMinute(t *testing.T) {
	h := NewHandler(redis.NewClient(&redis.Options{Addr: "localhost:0"}), Options{})

	a, ok := h.admitter.(*RedisAdmitter)
	if !ok || a.limit != 2 || a.window != 60*time.Second {
		t.Fatalf("admitter mặc định = %+v, want 2 lần / 60s", h.admitter)
	}
}
