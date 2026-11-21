package ping

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EngineerProOrg/BE-K01/internal/app/auth"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// env dựng router giống router thật: auth.RequireAuth bọc route /ping, trên miniredis.
type env struct {
	r        *gin.Engine
	mr       *miniredis.Miniredis
	rdb      *redis.Client
	sessions *auth.SessionStore
}

func newEnv(t *testing.T, opt Options) *env {
	t.Helper()
	gin.SetMode(gin.TestMode)

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	sessions := auth.NewSessionStore(rdb, auth.DefaultSessionTTL)
	authH := auth.NewHandler(auth.NewUserStore(rdb), sessions, auth.CookieOptions{})

	r := gin.New()
	authed := r.Group("", authH.RequireAuth())
	NewHandler(rdb, opt).Routes(authed)

	return &env{r: r, mr: mr, rdb: rdb, sessions: sessions}
}

// cookie tạo session thẳng trong Redis (bỏ qua register/login để không tốn bcrypt).
func (e *env) cookie(t *testing.T, username string) *http.Cookie {
	t.Helper()
	id, err := e.sessions.Create(context.Background(), username)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: auth.SessionCookieName, Value: id}
}

func (e *env) get(ctx context.Context, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	return w
}

// waitFor chờ điều kiện đúng (tối đa 2s), dùng để chờ goroutine khác tiến tới trạng thái cần test.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("hết thời gian chờ: %s", what)
}

func TestPing_RequiresAuth(t *testing.T) {
	e := newEnv(t, Options{Sleep: time.Millisecond})

	w := e.get(context.Background(), "/ping", nil)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if e.mr.Exists(lockKey) {
		t.Error("request chưa đăng nhập không được đụng tới khoá")
	}
}

func TestPing_Success(t *testing.T) {
	e := newEnv(t, Options{Sleep: 20 * time.Millisecond})
	c := e.cookie(t, "alice")

	start := time.Now()
	w := e.get(context.Background(), "/ping", c)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"message":"pong"`) ||
		!strings.Contains(w.Body.String(), `"username":"alice"`) {
		t.Fatalf("status = %d body=%s, want 200 pong alice", w.Code, w.Body)
	}
	if took := time.Since(start); took < 20*time.Millisecond {
		t.Errorf("trả lời sau %v, nhanh hơn thời gian sleep 20ms", took)
	}
	if e.mr.Exists(lockKey) {
		t.Error("khoá vẫn còn sau khi /ping xong")
	}
}

// Yêu cầu cốt lõi: khi một người đang ping, người khác bị từ chối ngay (không phải chờ).
func TestPing_OnlyOneAtATime(t *testing.T) {
	e := newEnv(t, Options{Sleep: 300 * time.Millisecond})
	alice, bob := e.cookie(t, "alice"), e.cookie(t, "bob")

	var wg sync.WaitGroup
	var first *httptest.ResponseRecorder
	wg.Add(1)
	go func() {
		defer wg.Done()
		first = e.get(context.Background(), "/ping", alice)
	}()
	waitFor(t, "alice giữ khoá", func() bool { return e.mr.Exists(lockKey) })

	start := time.Now()
	second := e.get(context.Background(), "/ping", bob)

	if second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), `"error":"busy"`) {
		t.Fatalf("bob: status = %d body=%s, want 429 busy", second.Code, second.Body)
	}
	if took := time.Since(start); took > 150*time.Millisecond {
		t.Errorf("bob bị từ chối sau %v: phải từ chối ngay, không được chờ", took)
	}

	wg.Wait()
	if first.Code != http.StatusOK {
		t.Fatalf("alice: status = %d, want 200 (request bị từ chối không được làm hỏng người đang chạy)", first.Code)
	}
}

// Cũng đúng khi cùng một người gọi hai lần chồng nhau.
func TestPing_SameUserOverlappingIsAlsoBusy(t *testing.T) {
	e := newEnv(t, Options{Sleep: 200 * time.Millisecond})
	alice := e.cookie(t, "alice")

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- e.get(context.Background(), "/ping", alice) }()
	waitFor(t, "alice giữ khoá", func() bool { return e.mr.Exists(lockKey) })

	if w := e.get(context.Background(), "/ping", alice); w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
	if w := <-done; w.Code != http.StatusOK {
		t.Fatalf("request đầu: status = %d, want 200", w.Code)
	}
}

// Nhiều request cùng lúc: đúng một cái chạy, các cái còn lại bị 429.
func TestPing_ManyConcurrentExactlyOneRuns(t *testing.T) {
	e := newEnv(t, Options{Sleep: 300 * time.Millisecond})
	const n = 12
	cookies := make([]*http.Cookie, n)
	for i := range cookies {
		cookies[i] = e.cookie(t, "user"+string(rune('a'+i)))
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		ok, busy int
		start    = make(chan struct{})
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(c *http.Cookie) {
			defer wg.Done()
			<-start
			w := e.get(context.Background(), "/ping", c)
			mu.Lock()
			defer mu.Unlock()
			switch w.Code {
			case http.StatusOK:
				ok++
			case http.StatusTooManyRequests:
				busy++
			default:
				t.Errorf("status bất ngờ %d", w.Code)
			}
		}(cookies[i])
	}
	close(start)
	wg.Wait()

	if ok != 1 || busy != n-1 {
		t.Fatalf("200=%d 429=%d, want 1 và %d", ok, busy, n-1)
	}
}

func TestPing_LockReleasedSoNextCallWorks(t *testing.T) {
	e := newEnv(t, Options{Sleep: 10 * time.Millisecond})
	c := e.cookie(t, "alice")

	for i := 0; i < 3; i++ {
		if w := e.get(context.Background(), "/ping", c); w.Code != http.StatusOK {
			t.Fatalf("lần %d: status = %d, want 200", i+1, w.Code)
		}
	}
}

// Client ngắt kết nối giữa chừng: khoá phải được nhả ngay, không giữ tới hết sleep.
func TestPing_ClientDisconnectReleasesLock(t *testing.T) {
	e := newEnv(t, Options{Sleep: 5 * time.Second})
	c := e.cookie(t, "alice")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- e.get(ctx, "/ping", c) }()
	waitFor(t, "alice giữ khoá", func() bool { return e.mr.Exists(lockKey) })

	cancel() // client ngắt kết nối

	select {
	case w := <-done:
		if w.Code != statusClientClosed {
			t.Errorf("status = %d, want %d", w.Code, statusClientClosed)
		}
	case <-time.After(time.Second):
		t.Fatal("handler không thoát sau khi client ngắt (vẫn ngủ hết 5s)")
	}
	// Khoá phải được nhả ngay (không phải đợi hết 5s sleep hay 10s TTL).
	waitFor(t, "khoá được nhả", func() bool { return !e.mr.Exists(lockKey) })
}

// Người giữ khoá chết (kill -9) mà không nhả: khoá kẹt nhưng tự hết hạn theo TTL.
func TestPing_StaleLockFromCrashedHolderSelfHeals(t *testing.T) {
	e := newEnv(t, Options{Sleep: time.Millisecond, LockTTL: 10 * time.Second})
	c := e.cookie(t, "alice")
	// Giả lập process khác đã chết khi đang giữ khoá.
	if err := e.rdb.Set(context.Background(), lockKey, "token-cua-process-da-chet", 10*time.Second).Err(); err != nil {
		t.Fatal(err)
	}

	if w := e.get(context.Background(), "/ping", c); w.Code != http.StatusTooManyRequests {
		t.Fatalf("khi khoá còn kẹt: status = %d, want 429", w.Code)
	}
	e.mr.FastForward(11 * time.Second) // TTL hết hạn

	if w := e.get(context.Background(), "/ping", c); w.Code != http.StatusOK {
		t.Fatalf("sau khi khoá hết hạn: status = %d, want 200", w.Code)
	}
}

// Request đang chạy không được nhả nhầm khoá của người khác nếu khoá của nó đã hết hạn giữa chừng.
func TestPing_DoesNotReleaseSomeoneElsesLockAfterExpiry(t *testing.T) {
	e := newEnv(t, Options{Sleep: 200 * time.Millisecond, LockTTL: 300 * time.Millisecond})
	c := e.cookie(t, "alice")

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- e.get(context.Background(), "/ping", c) }()
	waitFor(t, "alice giữ khoá", func() bool { return e.mr.Exists(lockKey) })

	// Khoá của alice bị thay bằng khoá của người khác (như thể đã hết hạn và bị giành mất).
	e.mr.Set(lockKey, "khoa-cua-nguoi-khac")
	if w := <-done; w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	if got, _ := e.mr.Get(lockKey); got != "khoa-cua-nguoi-khac" {
		t.Fatalf("alice đã nhả nhầm khoá của người khác (còn %q)", got)
	}
}

func TestNewHandler_LockTTLAlwaysExceedsSleep(t *testing.T) {
	for name, tc := range map[string]struct {
		opt  Options
		want time.Duration
	}{
		"mặc định":             {Options{}, defaultLockTTL},
		"sleep nhỏ":            {Options{Sleep: time.Second}, defaultLockTTL},
		"sleep vượt TTL":       {Options{Sleep: 8 * time.Second, LockTTL: 5 * time.Second}, 16 * time.Second},
		"sleep bằng TTL":       {Options{Sleep: 10 * time.Second}, 20 * time.Second},
		"TTL tuỳ chỉnh hợp lệ": {Options{Sleep: time.Second, LockTTL: 3 * time.Second}, 3 * time.Second},
	} {
		h := NewHandler(redis.NewClient(&redis.Options{Addr: "localhost:0"}), tc.opt)
		if h.lock.ttl != tc.want {
			t.Errorf("%s: ttl = %v, want %v", name, h.lock.ttl, tc.want)
		}
		if h.lock.ttl <= h.sleep {
			t.Errorf("%s: ttl %v không lớn hơn sleep %v", name, h.lock.ttl, h.sleep)
		}
	}
}

func TestPing_RedisDown(t *testing.T) {
	e := newEnv(t, Options{Sleep: time.Millisecond})
	c := e.cookie(t, "alice")
	e.mr.Close()

	w := e.get(context.Background(), "/ping", c)

	// RequireAuth không kiểm tra được session: 503, không phải 401.
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}
