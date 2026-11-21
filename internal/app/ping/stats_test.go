package ping

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func decodeTop(t *testing.T, w *httptest.ResponseRecorder) []TopEntry {
	t.Helper()
	var out []TopEntry
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body không phải JSON mảng: %q", w.Body)
	}
	return out
}

func names(es []TopEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Username
	}
	return out
}

func TestTop_OrdersByCountDescending(t *testing.T) {
	e := newEnv(t, Options{Sleep: time.Millisecond})
	e.mr.ZAdd(countKey, 5, "alice")
	e.mr.ZAdd(countKey, 3, "bob")
	e.mr.ZAdd(countKey, 9, "carol")
	e.mr.ZAdd(countKey, 1, "dave")

	w := e.get(context.Background(), "/top", e.cookie(t, "viewer"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	got := decodeTop(t, w)
	want := []TopEntry{{"carol", 9}, {"alice", 5}, {"bob", 3}, {"dave", 1}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("top = %v, want %v", got, want)
	}
	// count là số nguyên trong JSON (không phải 9.0).
	if !strings.Contains(w.Body.String(), `"count":9`) || strings.Contains(w.Body.String(), `"count":9.`) {
		t.Errorf("count phải là số nguyên: %s", w.Body)
	}
}

func TestTop_LimitedToTen(t *testing.T) {
	e := newEnv(t, Options{Sleep: time.Millisecond})
	for i := 1; i <= 15; i++ {
		e.mr.ZAdd(countKey, float64(i), fmt.Sprintf("user%02d", i))
	}

	got := decodeTop(t, e.get(context.Background(), "/top", e.cookie(t, "viewer")))

	if len(got) != TopN {
		t.Fatalf("có %d dòng, want %d", len(got), TopN)
	}
	if got[0].Count != 15 || got[len(got)-1].Count != 6 {
		t.Errorf("khoảng điểm = %d..%d, want 15..6 (người điểm 5 trở xuống phải bị loại)", got[0].Count, got[len(got)-1].Count)
	}
}

// Chưa ai ping: trả [] chứ không phải null.
func TestTop_EmptyIsEmptyArray(t *testing.T) {
	e := newEnv(t, Options{Sleep: time.Millisecond})

	w := e.get(context.Background(), "/top", e.cookie(t, "viewer"))

	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("status = %d body=%q, want 200 và []", w.Code, w.Body)
	}
}

// Đồng hạng được xếp theo username ngược chữ cái, và luôn cùng một thứ tự.
func TestTop_TiesAreDeterministic(t *testing.T) {
	e := newEnv(t, Options{Sleep: time.Millisecond})
	for _, u := range []string{"alice", "bob", "carol"} {
		e.mr.ZAdd(countKey, 3, u)
	}
	c := e.cookie(t, "viewer")

	for i := 0; i < 5; i++ {
		got := names(decodeTop(t, e.get(context.Background(), "/top", c)))
		if fmt.Sprint(got) != "[carol bob alice]" {
			t.Fatalf("lần %d: thứ tự = %v, want [carol bob alice]", i+1, got)
		}
	}
}

func TestTop_RequiresAuth(t *testing.T) {
	e := newEnv(t, Options{Sleep: time.Millisecond})

	if w := e.get(context.Background(), "/top", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// Kiểm tra xuyên suốt: bảng xếp hạng phản ánh các lần /ping thật, và các lần bị rate limit
// không được tính (bob spam 5 lần nhưng chỉ 2 lần thành công).
func TestTop_ReflectsRealPingsAndIgnoresRejectedOnes(t *testing.T) {
	fc := newFakeClock()
	e := newEnv(t, Options{Sleep: time.Millisecond, Limit: DefaultLimit, Clock: fc})
	alice, bob := e.cookie(t, "alice"), e.cookie(t, "bob")
	fc.At(0)

	e.get(context.Background(), "/ping", alice)
	for i := 0; i < 5; i++ {
		e.get(context.Background(), "/ping", bob)
	}

	got := decodeTop(t, e.get(context.Background(), "/top", alice))

	if fmt.Sprint(got) != fmt.Sprint([]TopEntry{{"bob", 2}, {"alice", 1}}) {
		t.Fatalf("top = %v, want [{bob 2} {alice 1}] (3 lần bob bị 429 không được tính)", got)
	}
}

// Redis lỗi ở bước đọc bảng xếp hạng: 503.
func TestTop_StatsErrorIs503(t *testing.T) {
	r := newStatsRouter(failingTopper{})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/top", nil))

	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"error":"unavailable"`) {
		t.Fatalf("status = %d body=%s, want 503 unavailable", w.Code, w.Body)
	}
}

type failingTopper struct{}

func (failingTopper) Top(context.Context, int64) ([]TopEntry, error) {
	return nil, errors.New("redis hỏng")
}

func (failingTopper) Count(context.Context) (int64, error) {
	return 0, errors.New("redis hỏng")
}

func TestStats_Top(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	s := NewStats(rdb)
	mr.ZAdd(countKey, 2, "alice")
	mr.ZAdd(countKey, 1, "bob")
	ctx := context.Background()

	if got, err := s.Top(ctx, 1); err != nil || len(got) != 1 || got[0].Username != "alice" {
		t.Errorf("Top(1) = %v, %v; want [alice]", got, err)
	}
	if got, _ := s.Top(ctx, 100); len(got) != 2 {
		t.Errorf("Top(100) có %d dòng, want 2 (n lớn hơn số người thì trả hết)", len(got))
	}
	// n <= 0 phải ra rỗng, không phải "toàn bộ" (ZREVRANGE 0 -1).
	for _, n := range []int64{0, -1} {
		if got, err := s.Top(ctx, n); err != nil || got == nil || len(got) != 0 {
			t.Errorf("Top(%d) = %v, %v; want [] không nil", n, got, err)
		}
	}

	mr.Close()
	if _, err := s.Top(ctx, 10); err == nil {
		t.Error("Top phải trả lỗi khi Redis chết")
	}
}

// newStatsRouter dựng router chỉ có route thống kê (không auth) với nguồn dữ liệu tuỳ ý.
func newStatsRouter(r statsReader) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	(&StatsHandler{stats: r}).Routes(e)
	return e
}
