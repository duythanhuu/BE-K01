package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

const (
	maxBodyBytes = 1 << 20 // 1 MiB

	// SessionCookieName là tên cookie chứa session id.
	SessionCookieName = "session_id"

	ctxUsername  = "auth.username"
	ctxSessionID = "auth.session_id"
)

// CookieOptions cấu hình cookie session.
type CookieOptions struct {
	// Secure: chỉ gửi cookie qua HTTPS. Bật khi chạy sau HTTPS; tắt khi dev trên http://localhost.
	Secure bool
}

// Handler gom các route xác thực và middleware RequireAuth.
type Handler struct {
	users    *UserStore
	sessions *SessionStore
	cookie   CookieOptions
}

func NewHandler(users *UserStore, sessions *SessionStore, cookie CookieOptions) *Handler {
	return &Handler{users: users, sessions: sessions, cookie: cookie}
}

// Routes đăng ký các route xác thực lên router.
func (h *Handler) Routes(r gin.IRouter) {
	r.POST("/register", h.register)
	r.POST("/login", h.login)

	authed := r.Group("", h.RequireAuth())
	authed.POST("/logout", h.logout)
	authed.GET("/me", h.me)
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// register: POST /register {"username","password"}
//
//	201 {"username"}           tạo thành công
//	400 {"error": ...}         đầu vào sai
//	409 {"error":"username_taken"}
//	503 {"error":"unavailable"} Redis lỗi (không phải lỗi của người dùng)
func (h *Handler) register(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)

	var in credentials
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}

	username := NormalizeUsername(in.Username)
	if err := ValidateUsername(username); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_username"})
		return
	}
	if err := ValidatePassword(in.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_password"})
		return
	}

	hash, err := HashPassword(in.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	switch err := h.users.Create(c.Request.Context(), username, hash); {
	case err == nil:
		c.JSON(http.StatusCreated, gin.H{"username": username})
	case errors.Is(err, ErrUserExists):
		c.JSON(http.StatusConflict, gin.H{"error": "username_taken"})
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
	}
}

// login: POST /login {"username","password"}
//
//	200 {"username"} + Set-Cookie: session_id=...
//	400 {"error":"invalid_body"}
//	401 {"error":"invalid_credentials"}  mọi lý do sai (không nói sai username hay mật khẩu)
//	503 {"error":"unavailable"}          Redis lỗi
func (h *Handler) login(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)

	var in credentials
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	username := NormalizeUsername(in.Username)

	ok, err := h.authenticate(c.Request.Context(), username, in.Password)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_credentials"})
		return
	}

	// Luôn tạo session id mới sau khi đăng nhập thành công (chống session fixation).
	id, err := h.sessions.Create(c.Request.Context(), username)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	h.setSessionCookie(c, id)
	c.JSON(http.StatusOK, gin.H{"username": username})
}

// authenticate trả (true, nil) khi username/mật khẩu đúng, (false, nil) khi sai,
// và lỗi khi Redis hỏng.
//
// bcrypt luôn chạy đúng một lần dù username có tồn tại hay không (không tồn tại
// thì so với dummyHash), nên không thể dò username có thật qua thời gian phản hồi.
func (h *Handler) authenticate(ctx context.Context, username, password string) (bool, error) {
	hash := dummyHash()
	found := false

	// Username sai định dạng thì chắc chắn không có trong Redis: khỏi hỏi Redis.
	if ValidateUsername(username) == nil {
		u, err := h.users.Get(ctx, username)
		switch {
		case err == nil:
			hash, found = u.PasswordHash, true
		case errors.Is(err, ErrUserNotFound):
		default:
			return false, err
		}
	}

	// Mật khẩu quá 72 byte không thể là mật khẩu hợp lệ (register đã từ chối);
	// cắt để bcrypt vẫn chạy một lần, và kết quả luôn là sai.
	candidate := password
	if len(candidate) > maxPasswordBytes {
		candidate, found = candidate[:maxPasswordBytes], false
	}

	match := CheckPassword(hash, candidate)
	return found && match, nil
}

// RequireAuth là middleware: bắt buộc có cookie session hợp lệ.
//
//	401 {"error":"unauthorized"} không có cookie, session không tồn tại hoặc hết hạn
//	503 {"error":"unavailable"}  Redis lỗi. KHÔNG được coi là "chưa đăng nhập" (401),
//	                             nếu không người dùng bị đăng xuất oan mỗi khi Redis chập chờn.
//
// Mỗi request hợp lệ gia hạn TTL của session (hết hạn trượt).
func (h *Handler) RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := c.Cookie(SessionCookieName)
		if err != nil || id == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		username, err := h.sessions.Get(c.Request.Context(), id)
		switch {
		case err == nil:
			c.Set(ctxUsername, username)
			c.Set(ctxSessionID, id)
			c.Next()
		case errors.Is(err, ErrSessionNotFound):
			h.clearSessionCookie(c)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		default:
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		}
	}
}

// UsernameFrom lấy username đã được RequireAuth đặt vào context.
func UsernameFrom(c *gin.Context) (string, bool) {
	v, ok := c.Get(ctxUsername)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// logout: POST /logout (cần đăng nhập) → 204, xoá session và cookie.
func (h *Handler) logout(c *gin.Context) {
	id, _ := c.Get(ctxSessionID)
	if sid, ok := id.(string); ok {
		if err := h.sessions.Delete(c.Request.Context(), sid); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
			return
		}
	}
	h.clearSessionCookie(c)
	c.Status(http.StatusNoContent)
}

// me: GET /me (cần đăng nhập) → {"username"}.
func (h *Handler) me(c *gin.Context) {
	username, _ := UsernameFrom(c)
	c.JSON(http.StatusOK, gin.H{"username": username})
}

// setSessionCookie đặt cookie session. Không có Max-Age: đây là cookie phiên của
// trình duyệt; thời hạn thật do TTL trong Redis quyết định (hết hạn trượt), nên
// không phải gia hạn cookie ở mỗi request.
func (h *Handler) setSessionCookie(c *gin.Context, id string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(SessionCookieName, id, 0, "/", "", h.cookie.Secure, true)
}

func (h *Handler) clearSessionCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(SessionCookieName, "", -1, "/", "", h.cookie.Secure, true)
}
