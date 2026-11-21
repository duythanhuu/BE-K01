package ping

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func countOf(t *testing.T, w *httptest.ResponseRecorder) int64 {
	t.Helper()
	var out struct {
		Count int64 `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body không phải JSON: %q", w.Body)
	}
	return out.Count
}

func TestPing_CountIncrementsPerUser(t *testing.T) {
	e := newEnv(t, Options{Sleep: time.Millisecond})
	alice, bob := e.cookie(t, "alice"), e.cookie(t, "bob")

	for want := int64(1); want <= 3; want++ {
		w := e.get(context.Background(), "/ping", alice)
		if w.Code != http.StatusOK || countOf(t, w) != want {
			t.Fatalf("alice lần %d: status=%d count=%d, want 200 và %d (body=%s)", want, w.Code, countOf(t, w), want, w.Body)
		}
	}
	w := e.get(context.Background(), "/ping", bob)

	if countOf(t, w) != 1 {
		t.Errorf("bob lần đầu: count=%d, want 1 (mỗi người một bộ đếm)", countOf(t, w))
	}
	if s, _ := e.mr.ZScore(countKey, "alice"); s != 3 {
		t.Errorf("ZSCORE alice = %v, want 3", s)
	}
}

// Request bị từ chối vì bận (429) không được tính vào số lần ping.
func TestPing_BusyRequestIsNotCounted(t *testing.T) {
	e := newEnv(t, Options{Sleep: 300 * time.Millisecond})
	alice, bob := e.cookie(t, "alice"), e.cookie(t, "bob")

	done := make(chan struct{})
	go func() { defer close(done); e.get(context.Background(), "/ping", alice) }()
	waitFor(t, "alice giữ khoá", func() bool { return e.mr.Exists(lockKey) })

	for i := 0; i < 3; i++ {
		if w := e.get(context.Background(), "/ping", bob); w.Code != http.StatusTooManyRequests {
			t.Fatalf("bob lần %d: status = %d, want 429", i+1, w.Code)
		}
	}
	<-done

	members, _ := e.mr.ZMembers(countKey)
	if !slices.Equal(members, []string{"alice"}) {
		t.Fatalf("ping:count có %v, want chỉ alice (bob chỉ nhận 429 nên không được tính)", members)
	}
}

func TestPing_UnauthenticatedIsNotCounted(t *testing.T) {
	e := newEnv(t, Options{Sleep: time.Millisecond})

	e.get(context.Background(), "/ping", nil)

	if e.mr.Exists(countKey) {
		t.Fatal("request chưa đăng nhập đã làm thay đổi ping:count")
	}
}

// Lần ping được tính ngay khi được chấp nhận, trước sleep: client ngắt giữa chừng vẫn tính 1 lần.
func TestPing_CountedAtAdmissionEvenIfClientDisconnects(t *testing.T) {
	e := newEnv(t, Options{Sleep: 5 * time.Second})
	c := e.cookie(t, "alice")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.get(ctx, "/ping", c) }()

	// Đã được tính trong lúc handler còn đang ngủ (chưa trả lời).
	waitFor(t, "alice được ghi nhận", func() bool {
		s, err := e.mr.ZScore(countKey, "alice")
		return err == nil && s == 1
	})
	cancel()
	<-done

	if s, _ := e.mr.ZScore(countKey, "alice"); s != 1 {
		t.Fatalf("ZSCORE alice = %v sau khi client ngắt, want 1", s)
	}
}

// Ghi nhận lỗi thì trả 503 và phải nhả khoá, không để người khác bị 429 oan cho tới hết TTL.
func TestPing_AdmitFailureReturns503AndReleasesLock(t *testing.T) {
	e := newEnvWith(t, Options{Sleep: time.Millisecond}, func(h *Handler) {
		h.admitter = failingAdmitter{}
	})
	c := e.cookie(t, "alice")

	w := e.get(context.Background(), "/ping", c)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if e.mr.Exists(lockKey) {
		t.Fatal("khoá vẫn còn sau khi ghi nhận lỗi")
	}
}

type failingAdmitter struct{}

func (failingAdmitter) Admit(context.Context, string) (Admission, error) {
	return Admission{}, errors.New("redis hỏng")
}
