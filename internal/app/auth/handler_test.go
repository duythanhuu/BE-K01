package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func newTestRouterWith(t *testing.T, cookie CookieOptions) (*gin.Engine, *miniredis.Miniredis) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	r := gin.New()
	NewHandler(NewUserStore(rdb), NewSessionStore(rdb, DefaultSessionTTL), cookie).Routes(r)
	return r, mr
}

func newTestRouter(t *testing.T) (*gin.Engine, *miniredis.Miniredis) {
	t.Helper()
	return newTestRouterWith(t, CookieOptions{})
}

func do(r http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func post(r http.Handler, path, body string) *httptest.ResponseRecorder {
	return do(r, http.MethodPost, path, body, nil)
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body không phải JSON: %q", w.Body)
	}
	return out.Error
}

// sessionCookie lấy cookie session từ response (nil nếu không có).
func sessionCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookieName {
			return c
		}
	}
	return nil
}

// registerAndLogin đăng ký + đăng nhập, trả về cookie session.
func registerAndLogin(t *testing.T, r http.Handler, username, password string) *http.Cookie {
	t.Helper()
	body := `{"username":"` + username + `","password":"` + password + `"}`
	if w := post(r, "/register", body); w.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, body=%s", w.Code, w.Body)
	}
	w := post(r, "/login", body)
	if w.Code != http.StatusOK {
		t.Fatalf("login: status = %d, body=%s", w.Code, w.Body)
	}
	c := sessionCookie(w)
	if c == nil || c.Value == "" {
		t.Fatal("login không đặt cookie session")
	}
	return c
}

// ---------- /register ----------

func TestRegister_Success(t *testing.T) {
	r, mr := newTestRouter(t)

	w := post(r, "/register", `{"username":"Alice","password":"correct horse"}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"username":"alice"`) {
		t.Errorf("body = %s, want username đã chuẩn hoá", w.Body)
	}

	// Redis lưu bản băm, không lưu mật khẩu gốc; response không lộ mật khẩu.
	stored := mr.HGet("user:alice", "password_hash")
	if stored == "" || stored == "correct horse" || strings.Contains(stored, "correct horse") {
		t.Errorf("password_hash lưu sai: %q", stored)
	}
	if !CheckPassword(stored, "correct horse") {
		t.Error("hash lưu trong Redis không khớp mật khẩu đã đăng ký")
	}
	if strings.Contains(w.Body.String(), "correct horse") || strings.Contains(w.Body.String(), stored) {
		t.Errorf("response lộ mật khẩu hoặc hash: %s", w.Body)
	}
}

func TestRegister_DuplicateUsername(t *testing.T) {
	r, _ := newTestRouter(t)
	body := `{"username":"alice","password":"correct horse"}`

	if w := post(r, "/register", body); w.Code != http.StatusCreated {
		t.Fatalf("lần 1: status = %d", w.Code)
	}
	w := post(r, "/register", body)

	if w.Code != http.StatusConflict || errCode(t, w) != "username_taken" {
		t.Fatalf("lần 2: status = %d body=%s, want 409 username_taken", w.Code, w.Body)
	}
}

// "Alice" và "alice" là cùng một tài khoản.
func TestRegister_DuplicateIsCaseInsensitive(t *testing.T) {
	r, _ := newTestRouter(t)

	post(r, "/register", `{"username":"alice","password":"correct horse"}`)
	w := post(r, "/register", `{"username":"  ALICE ","password":"another pass"}`)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", w.Code, w.Body)
	}
}

func TestRegister_InvalidInput(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCode string
	}{
		{"không phải JSON", `not json`, "invalid_body"},
		{"body rỗng", ``, "invalid_body"},
		{"username quá ngắn", `{"username":"ab","password":"correct horse"}`, "invalid_username"},
		{"username ký tự lạ", `{"username":"bob!","password":"correct horse"}`, "invalid_username"},
		{"thiếu username", `{"password":"correct horse"}`, "invalid_username"},
		{"mật khẩu quá ngắn", `{"username":"alice","password":"short"}`, "invalid_password"},
		{"thiếu mật khẩu", `{"username":"alice"}`, "invalid_password"},
		{"mật khẩu vượt 72 byte", `{"username":"alice","password":"` + strings.Repeat("a", 73) + `"}`, "invalid_password"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, mr := newTestRouter(t)

			w := post(r, "/register", tt.body)

			if w.Code != http.StatusBadRequest || errCode(t, w) != tt.wantCode {
				t.Fatalf("status = %d body=%s, want 400 %s", w.Code, w.Body, tt.wantCode)
			}
			if mr.Exists("user:alice") {
				t.Error("đầu vào sai nhưng vẫn tạo user")
			}
		})
	}
}

func TestRegister_BodyTooLarge(t *testing.T) {
	r, _ := newTestRouter(t)
	big := `{"username":"alice","password":"` + strings.Repeat("a", 2<<20) + `"}`

	w := post(r, "/register", big)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// Redis lỗi là lỗi hệ thống (503), không được báo thành lỗi của người dùng.
func TestRegister_RedisDown(t *testing.T) {
	r, mr := newTestRouter(t)
	mr.Close()

	w := post(r, "/register", `{"username":"alice","password":"correct horse"}`)

	if w.Code != http.StatusServiceUnavailable || errCode(t, w) != "unavailable" {
		t.Fatalf("status = %d body=%s, want 503 unavailable", w.Code, w.Body)
	}
}

func TestRegister_OnlyPOST(t *testing.T) {
	r, _ := newTestRouter(t)

	w := do(r, http.MethodGet, "/register", "", nil)

	if w.Code == http.StatusOK || w.Code == http.StatusCreated {
		t.Fatalf("GET /register không được thành công, status = %d", w.Code)
	}
}

// ---------- /login ----------

func TestLogin_Success(t *testing.T) {
	r, mr := newTestRouter(t)
	post(r, "/register", `{"username":"alice","password":"correct horse"}`)

	w := post(r, "/login", `{"username":"Alice","password":"correct horse"}`)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"username":"alice"`) {
		t.Fatalf("status = %d body=%s, want 200 username=alice", w.Code, w.Body)
	}
	c := sessionCookie(w)
	if c == nil || c.Value == "" {
		t.Fatal("không đặt cookie session")
	}

	// Cookie chứa id gốc; Redis chứa session (khoá theo hash) với username đúng.
	if len(mr.Keys()) != 2 { // user:alice + một session
		t.Fatalf("keys = %v, want user:alice và 1 session", mr.Keys())
	}
	if got := mr.HGet(sessionKey(c.Value), "username"); got != "alice" {
		t.Errorf("username trong session = %q, want alice", got)
	}
	if ttl := mr.TTL(sessionKey(c.Value)); ttl <= 0 || ttl > DefaultSessionTTL {
		t.Errorf("TTL session = %v, want (0, %v]", ttl, DefaultSessionTTL)
	}
}

func TestLogin_CookieAttributes(t *testing.T) {
	r, _ := newTestRouter(t)
	post(r, "/register", `{"username":"alice","password":"correct horse"}`)

	c := sessionCookie(post(r, "/login", `{"username":"alice","password":"correct horse"}`))

	if !c.HttpOnly {
		t.Error("cookie phải HttpOnly (JS không đọc được)")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("Path = %q, want /", c.Path)
	}
	if c.Secure {
		t.Error("Secure phải tắt theo mặc định (dev http://localhost)")
	}
	if c.MaxAge != 0 {
		t.Errorf("MaxAge = %d: cookie phải là cookie phiên, thời hạn do Redis quyết định", c.MaxAge)
	}
}

func TestLogin_SecureCookieWhenConfigured(t *testing.T) {
	r, _ := newTestRouterWith(t, CookieOptions{Secure: true})
	post(r, "/register", `{"username":"alice","password":"correct horse"}`)

	c := sessionCookie(post(r, "/login", `{"username":"alice","password":"correct horse"}`))

	if !c.Secure {
		t.Error("Secure phải bật khi cấu hình CookieSecure")
	}
}

// Sai mật khẩu và username không tồn tại phải cho response GIỐNG HỆT nhau:
// không được để lộ username nào có thật.
func TestLogin_FailureIsIndistinguishable(t *testing.T) {
	r, _ := newTestRouter(t)
	post(r, "/register", `{"username":"alice","password":"correct horse"}`)

	wrongPass := post(r, "/login", `{"username":"alice","password":"wrong horse!"}`)
	noUser := post(r, "/login", `{"username":"nobody","password":"correct horse"}`)

	for name, w := range map[string]*httptest.ResponseRecorder{"sai mật khẩu": wrongPass, "không có user": noUser} {
		if w.Code != http.StatusUnauthorized || errCode(t, w) != "invalid_credentials" {
			t.Errorf("%s: status = %d body=%s, want 401 invalid_credentials", name, w.Code, w.Body)
		}
		if sessionCookie(w) != nil {
			t.Errorf("%s: không được đặt cookie", name)
		}
	}
	if wrongPass.Body.String() != noUser.Body.String() {
		t.Errorf("body khác nhau:\n  sai mật khẩu:  %s\n  không có user: %s", wrongPass.Body, noUser.Body)
	}
}

func TestLogin_RejectsBadCredentialShapes(t *testing.T) {
	r, mr := newTestRouter(t)
	post(r, "/register", `{"username":"alice","password":"correct horse"}`)

	for name, body := range map[string]string{
		"username sai định dạng": `{"username":"a b","password":"correct horse"}`,
		"username rỗng":          `{"username":"","password":"correct horse"}`,
		"mật khẩu rỗng":          `{"username":"alice","password":""}`,
		// Cắt về 72 byte vẫn không được khớp một mật khẩu hợp lệ có tiền tố giống vậy.
		"mật khẩu quá dài": `{"username":"alice","password":"` + strings.Repeat("x", 100) + `"}`,
	} {
		w := post(r, "/login", body)
		if w.Code != http.StatusUnauthorized || errCode(t, w) != "invalid_credentials" {
			t.Errorf("%s: status = %d body=%s, want 401", name, w.Code, w.Body)
		}
	}
	if len(mr.Keys()) != 1 {
		t.Errorf("đăng nhập thất bại không được tạo session; keys = %v", mr.Keys())
	}
}

// Mật khẩu dài hơn 72 byte mà 72 byte đầu trùng mật khẩu thật vẫn phải bị từ chối.
func TestLogin_LongPasswordWithValidPrefixIsRejected(t *testing.T) {
	r, _ := newTestRouter(t)
	prefix := strings.Repeat("p", 72)
	post(r, "/register", `{"username":"alice","password":"`+prefix+`"}`)

	w := post(r, "/login", `{"username":"alice","password":"`+prefix+`EXTRA"}`)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (bcrypt cắt 72 byte không được làm lọt mật khẩu sai)", w.Code)
	}
}

func TestLogin_InvalidBody(t *testing.T) {
	r, _ := newTestRouter(t)

	w := post(r, "/login", `not json`)

	if w.Code != http.StatusBadRequest || errCode(t, w) != "invalid_body" {
		t.Fatalf("status = %d body=%s, want 400 invalid_body", w.Code, w.Body)
	}
}

func TestLogin_EachLoginGetsNewSessionID(t *testing.T) {
	r, _ := newTestRouter(t)
	body := `{"username":"alice","password":"correct horse"}`
	post(r, "/register", body)

	a := sessionCookie(post(r, "/login", body))
	b := sessionCookie(post(r, "/login", body))

	if a.Value == b.Value {
		t.Fatal("hai lần đăng nhập dùng cùng session id (nguy cơ session fixation)")
	}
}

func TestLogin_RedisDown(t *testing.T) {
	r, mr := newTestRouter(t)
	post(r, "/register", `{"username":"alice","password":"correct horse"}`)
	mr.Close()

	w := post(r, "/login", `{"username":"alice","password":"correct horse"}`)

	if w.Code != http.StatusServiceUnavailable || errCode(t, w) != "unavailable" {
		t.Fatalf("status = %d body=%s, want 503 (không phải 401)", w.Code, w.Body)
	}
}

// ---------- RequireAuth, /me, /logout ----------

func TestMe_WithSession(t *testing.T) {
	r, _ := newTestRouter(t)
	c := registerAndLogin(t, r, "alice", "correct horse")

	w := do(r, http.MethodGet, "/me", "", c)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"username":"alice"`) {
		t.Fatalf("status = %d body=%s, want 200 username=alice", w.Code, w.Body)
	}
}

func TestMe_Unauthorized(t *testing.T) {
	r, _ := newTestRouter(t)

	for name, cookie := range map[string]*http.Cookie{
		"không có cookie":  nil,
		"cookie rỗng":      {Name: SessionCookieName, Value: ""},
		"session không có": {Name: SessionCookieName, Value: "id-giả-không-tồn-tại"},
	} {
		w := do(r, http.MethodGet, "/me", "", cookie)
		if w.Code != http.StatusUnauthorized || errCode(t, w) != "unauthorized" {
			t.Errorf("%s: status = %d body=%s, want 401 unauthorized", name, w.Code, w.Body)
		}
	}
}

func TestMe_SessionExpires(t *testing.T) {
	r, mr := newTestRouter(t)
	c := registerAndLogin(t, r, "alice", "correct horse")

	mr.FastForward(DefaultSessionTTL + time.Minute)
	w := do(r, http.MethodGet, "/me", "", c)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 sau khi hết hạn", w.Code)
	}
}

// Hoạt động gia hạn session: dùng đều đặn thì không bao giờ bị đăng xuất.
func TestMe_ActivityExtendsSession(t *testing.T) {
	r, mr := newTestRouter(t)
	c := registerAndLogin(t, r, "alice", "correct horse")

	for i := 0; i < 4; i++ { // 4 × 20 phút = 80 phút > TTL 30 phút
		mr.FastForward(20 * time.Minute)
		if w := do(r, http.MethodGet, "/me", "", c); w.Code != http.StatusOK {
			t.Fatalf("lần %d: status = %d, session bị hết hạn dù đang hoạt động", i+1, w.Code)
		}
	}
}

func TestMe_ExpiredSessionClearsCookie(t *testing.T) {
	r, mr := newTestRouter(t)
	c := registerAndLogin(t, r, "alice", "correct horse")
	mr.FastForward(DefaultSessionTTL + time.Minute)

	w := do(r, http.MethodGet, "/me", "", c)

	cleared := sessionCookie(w)
	if cleared == nil || cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Fatalf("phải xoá cookie hết hạn, nhưng nhận được %+v", cleared)
	}
}

// Redis lỗi khi kiểm tra session là 503, KHÔNG phải 401 (tránh đăng xuất oan).
func TestMe_RedisDownIs503NotUnauthorized(t *testing.T) {
	r, mr := newTestRouter(t)
	c := registerAndLogin(t, r, "alice", "correct horse")
	mr.Close()

	w := do(r, http.MethodGet, "/me", "", c)

	if w.Code != http.StatusServiceUnavailable || errCode(t, w) != "unavailable" {
		t.Fatalf("status = %d body=%s, want 503 unavailable", w.Code, w.Body)
	}
}

func TestLogout(t *testing.T) {
	r, mr := newTestRouter(t)
	c := registerAndLogin(t, r, "alice", "correct horse")

	w := do(r, http.MethodPost, "/logout", "", c)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if mr.Exists(sessionKey(c.Value)) {
		t.Error("session vẫn còn trong Redis sau khi đăng xuất")
	}
	cleared := sessionCookie(w)
	if cleared == nil || cleared.MaxAge >= 0 {
		t.Errorf("phải xoá cookie, nhận được %+v", cleared)
	}
	// Dùng lại cookie cũ sau khi đăng xuất phải bị từ chối.
	if w := do(r, http.MethodGet, "/me", "", c); w.Code != http.StatusUnauthorized {
		t.Fatalf("/me sau logout: status = %d, want 401", w.Code)
	}
}

func TestLogout_RequiresAuth(t *testing.T) {
	r, _ := newTestRouter(t)

	w := do(r, http.MethodPost, "/logout", "", nil)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// Đăng xuất một thiết bị không ảnh hưởng session của thiết bị khác.
func TestLogout_OnlyEndsOwnSession(t *testing.T) {
	r, _ := newTestRouter(t)
	body := `{"username":"alice","password":"correct horse"}`
	post(r, "/register", body)
	phone := sessionCookie(post(r, "/login", body))
	laptop := sessionCookie(post(r, "/login", body))

	do(r, http.MethodPost, "/logout", "", phone)

	if w := do(r, http.MethodGet, "/me", "", laptop); w.Code != http.StatusOK {
		t.Fatalf("session của laptop bị ảnh hưởng: status = %d", w.Code)
	}
}

// UsernameFrom phải trả đúng người cho route dùng RequireAuth (các PR sau dựa vào đây).
func TestUsernameFrom(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	h := NewHandler(NewUserStore(rdb), NewSessionStore(rdb, time.Hour), CookieOptions{})

	r := gin.New()
	h.Routes(r)
	r.GET("/who", h.RequireAuth(), func(c *gin.Context) {
		u, ok := UsernameFrom(c)
		c.JSON(http.StatusOK, gin.H{"u": u, "ok": ok})
	})
	cookie := registerAndLogin(t, r, "alice", "correct horse")

	w := do(r, http.MethodGet, "/who", "", cookie)

	if !strings.Contains(w.Body.String(), `"u":"alice"`) || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("body = %s", w.Body)
	}
}
