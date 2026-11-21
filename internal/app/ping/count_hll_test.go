package ping

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func approx(t *testing.T, w *httptest.ResponseRecorder) int64 {
	t.Helper()
	var out struct {
		N int64 `json:"approx_unique_users"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body không phải JSON: %q", w.Body)
	}
	return out.N
}

func TestCount_EmptyIsZero(t *testing.T) {
	e := newEnv(t, Options{Sleep: time.Millisecond})

	w := e.get(context.Background(), "/count", e.cookie(t, "viewer"))

	if w.Code != http.StatusOK || approx(t, w) != 0 {
		t.Fatalf("status = %d body=%s, want 200 và 0", w.Code, w.Body)
	}
}

func TestCount_RequiresAuth(t *testing.T) {
	e := newEnv(t, Options{Sleep: time.Millisecond})

	if w := e.get(context.Background(), "/count", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// Một người ping nhiều lần vẫn chỉ tính là một người.
func TestCount_CountsDistinctUsersNotPings(t *testing.T) {
	e := newEnv(t, Options{Sleep: time.Millisecond})
	alice, bob := e.cookie(t, "alice"), e.cookie(t, "bob")

	for i := 0; i < 5; i++ {
		e.get(context.Background(), "/ping", alice)
	}
	e.get(context.Background(), "/ping", bob)

	w := e.get(context.Background(), "/count", alice)
	if got := approx(t, w); got != 2 {
		t.Fatalf("approx_unique_users = %d, want 2 (alice 5 lần + bob 1 lần = 2 người)", got)
	}
	if s, _ := e.mr.ZScore(countKey, "alice"); s != 5 {
		t.Errorf("ping:count alice = %v, want 5 (số lần vẫn đếm đủ)", s)
	}
}

// Người chỉ nhận 429 (bận hoặc rate limit) chưa từng ping thành công thì không được tính.
func TestCount_IgnoresUsersWhoOnlyGotRejected(t *testing.T) {
	e := newEnv(t, Options{Sleep: 300 * time.Millisecond})
	alice, carol := e.cookie(t, "alice"), e.cookie(t, "carol")

	done := make(chan struct{})
	go func() { defer close(done); e.get(context.Background(), "/ping", alice) }()
	waitFor(t, "alice giữ khoá", func() bool { return e.mr.Exists(lockKey) })
	if w := e.get(context.Background(), "/ping", carol); w.Code != http.StatusTooManyRequests {
		t.Fatalf("carol: status = %d, want 429", w.Code)
	}
	<-done

	if got := approx(t, e.get(context.Background(), "/count", alice)); got != 1 {
		t.Fatalf("approx_unique_users = %d, want 1 (carol chỉ nhận 429)", got)
	}
}

// Người bị rate limit thì không làm tăng số người (đã có trong HLL từ các lần thành công trước).
func TestCount_RateLimitedRepeatsDoNotInflate(t *testing.T) {
	fc := newFakeClock()
	e := newEnv(t, Options{Sleep: time.Millisecond, Limit: DefaultLimit, Clock: fc})
	alice := e.cookie(t, "alice")
	fc.At(0)

	for i := 0; i < 6; i++ {
		e.get(context.Background(), "/ping", alice)
	}

	if got := approx(t, e.get(context.Background(), "/count", alice)); got != 1 {
		t.Fatalf("approx_unique_users = %d, want 1", got)
	}
}

// HyperLogLog chỉ là xấp xỉ: so với số chính xác (ZCARD ping:count) phải trong sai số chấp nhận được.
func TestCount_ApproximatesExactCardinality(t *testing.T) {
	e := newEnv(t, Options{Sleep: time.Millisecond})
	admitter := NewAdmitter(e.rdb, newFakeClock(), manyLimit, 0)
	ctx := context.Background()

	const users = 5000
	for i := 0; i < users; i++ {
		if _, err := admitter.Admit(ctx, fmt.Sprintf("user%05d", i)); err != nil {
			t.Fatal(err)
		}
	}
	exact, err := e.rdb.ZCard(ctx, countKey).Result()
	if err != nil || exact != users {
		t.Fatalf("ZCARD = %d, %v; want %d", exact, err, users)
	}

	got := approx(t, e.get(ctx, "/count", e.cookie(t, "viewer")))

	relErr := math.Abs(float64(got)-float64(exact)) / float64(exact)
	t.Logf("HyperLogLog = %d, chính xác = %d, sai số = %.2f%%", got, exact, relErr*100)
	if relErr > 0.03 { // sai số chuẩn là 0,81%; 3% là khoảng 3,7 lần độ lệch chuẩn
		t.Fatalf("sai số %.2f%% vượt 3%% (HLL=%d, chính xác=%d)", relErr*100, got, exact)
	}
}

// /count và /top nhất quán: số người trong /top không vượt /count (khi dưới 10 người).
func TestCount_ConsistentWithTop(t *testing.T) {
	e := newEnv(t, Options{Sleep: time.Millisecond})
	for _, u := range []string{"alice", "bob", "carol"} {
		e.get(context.Background(), "/ping", e.cookie(t, u))
	}
	c := e.cookie(t, "viewer")

	top := decodeTop(t, e.get(context.Background(), "/top", c))
	n := approx(t, e.get(context.Background(), "/count", c))

	if int64(len(top)) != n || n != 3 {
		t.Fatalf("len(top)=%d approx=%d, want cùng bằng 3", len(top), n)
	}
}

func TestCount_StatsErrorIs503(t *testing.T) {
	r := newStatsRouter(failingTopper{})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/count", nil))

	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"error":"unavailable"`) {
		t.Fatalf("status = %d body=%s, want 503 unavailable", w.Code, w.Body)
	}
}
