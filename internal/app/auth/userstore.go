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

// ErrUserExists được trả về khi username đã có người đăng ký.
var ErrUserExists = errors.New("auth: user đã tồn tại")

func userKey(username string) string { return userKeyPrefix + username }

// UserStore lưu user trong Redis dưới dạng HASH `user:{username}`.
type UserStore struct {
	rdb redis.Cmdable
	now func() time.Time
}

func NewUserStore(rdb redis.Cmdable) *UserStore {
	return &UserStore{rdb: rdb, now: time.Now}
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
