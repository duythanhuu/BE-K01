package auth

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	userKeyPrefix     = "user:"
	fieldPasswordHash = "password_hash"
	fieldCreatedAt    = "created_at"
)

var (
	// ErrUserExists được trả về khi username đã có người đăng ký.
	ErrUserExists = errors.New("auth: user đã tồn tại")
	// ErrUserNotFound được trả về khi không có user với username đó.
	ErrUserNotFound = errors.New("auth: user không tồn tại")
)

// User là dữ liệu cần để xác thực.
type User struct {
	Username     string
	PasswordHash string
}

func userKey(username string) string { return userKeyPrefix + username }

// UserStore lưu user trong Redis dưới dạng HASH `user:{username}`.
type UserStore struct {
	rdb redis.Cmdable
	now func() time.Time
}

func NewUserStore(rdb redis.Cmdable) *UserStore {
	return &UserStore{rdb: rdb, now: time.Now}
}

// Get lấy user theo username đã chuẩn hoá. Trả ErrUserNotFound nếu không có.
func (s *UserStore) Get(ctx context.Context, username string) (*User, error) {
	hash, err := s.rdb.HGet(ctx, userKey(username), fieldPasswordHash).Result()
	if errors.Is(err, redis.Nil) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	return &User{Username: username, PasswordHash: hash}, nil
}

// Create tạo user mới. username phải đã được chuẩn hoá và hợp lệ.
//
// Dùng HSETNX trên password_hash làm "cổng": Redis chạy từng lệnh nguyên tử nên
// khi nhiều request cùng đăng ký một username, đúng một request qua được cổng,
// các request còn lại nhận ErrUserExists.
func (s *UserStore) Create(ctx context.Context, username, passwordHash string) error {
	key := userKey(username)

	created, err := s.rdb.HSetNX(ctx, key, fieldPasswordHash, passwordHash).Result()
	if err != nil {
		return err
	}
	if !created {
		return ErrUserExists
	}

	// created_at chỉ để tham khảo. Ghi sau cổng; lỗi ở đây không được làm hỏng
	// một lần đăng ký đã thành công (tài khoản đã tạo xong ở bước trên).
	_ = s.rdb.HSet(ctx, key, fieldCreatedAt, s.now().UTC().Format(time.RFC3339)).Err()
	return nil
}
