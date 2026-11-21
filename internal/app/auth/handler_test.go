package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
)

func newTestRouter(t *testing.T) (*gin.Engine, *miniredis.Miniredis) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	store, mr := newTestStore(t)
	r := gin.New()
	NewHandler(store).Routes(r)
	return r, mr
}

func post(r http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
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

	req := httptest.NewRequest(http.MethodGet, "/register", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code == http.StatusOK || w.Code == http.StatusCreated {
		t.Fatalf("GET /register không được thành công, status = %d", w.Code)
	}
}
