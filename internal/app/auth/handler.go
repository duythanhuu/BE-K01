package auth

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

const maxBodyBytes = 1 << 20 // 1 MiB

// Handler gom các route xác thực.
type Handler struct {
	users *UserStore
}

func NewHandler(users *UserStore) *Handler {
	return &Handler{users: users}
}

// Routes đăng ký các route lên router.
func (h *Handler) Routes(r gin.IRoutes) {
	r.POST("/register", h.register)
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
