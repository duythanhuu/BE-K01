package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	sessionKeyPrefix  = "session:"
	sessionIDBytes    = 32
	fieldUsername     = "username"
	DefaultSessionTTL = 30 * time.Minute
)

// ErrSessionNotFound: session không tồn tại, đã hết hạn hoặc đã bị xoá.
var ErrSessionNotFound = errors.New("auth: session không tồn tại hoặc đã hết hạn")

// SessionStore lưu session trong Redis dưới dạng HASH `session:{sha256(id)}`.
//
// Redis chỉ lưu SHA-256 của session id, không lưu id gốc: ai đọc được Redis cũng
// không dùng được dữ liệu đó để giả danh (cookie của người dùng mới chứa id gốc).
type SessionStore struct {
	rdb redis.Cmdable
	ttl time.Duration
	now func() time.Time
}

func NewSessionStore(rdb redis.Cmdable, ttl time.Duration) *SessionStore {
	return &SessionStore{rdb: rdb, ttl: ttl, now: time.Now}
}

// newSessionID sinh 32 byte ngẫu nhiên an toàn (256 bit), mã hoá base64url (43 ký tự).
func newSessionID() (string, error) {
	b := make([]byte, sessionIDBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func sessionKey(id string) string {
	sum := sha256.Sum256([]byte(id))
	return sessionKeyPrefix + hex.EncodeToString(sum[:])
}

// Create tạo session mới cho username và trả về session id gốc (để đặt vào cookie).
// Luôn sinh id mới, nên đăng nhập không bao giờ dùng lại id cũ (chống session fixation).
func (s *SessionStore) Create(ctx context.Context, username string) (string, error) {
	id, err := newSessionID()
	if err != nil {
		return "", err
	}
	key := sessionKey(id)

	// HSET + EXPIRE trong một giao dịch: không để lại session không có TTL.
	_, err = s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, key,
			fieldUsername, username,
			fieldCreatedAt, s.now().UTC().Format(time.RFC3339))
		p.Expire(ctx, key, s.ttl)
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Get trả về username của session và **gia hạn TTL** (hết hạn theo kiểu trượt:
// 30 phút không hoạt động mới bị đăng xuất). Trả ErrSessionNotFound nếu không có.
func (s *SessionStore) Get(ctx context.Context, id string) (string, error) {
	key := sessionKey(id)

	var hget *redis.StringCmd
	_, err := s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		hget = p.HGet(ctx, key, fieldUsername)
		p.Expire(ctx, key, s.ttl) // key không tồn tại thì EXPIRE không làm gì
		return nil
	})
	// HGET trên key không tồn tại trả redis.Nil: đó là "không tìm thấy", không phải lỗi hạ tầng.
	if err != nil && !errors.Is(err, redis.Nil) {
		return "", err
	}

	username, err := hget.Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrSessionNotFound
	}
	if err != nil {
		return "", err
	}
	return username, nil
}

// Delete xoá session (đăng xuất). Xoá session không tồn tại không phải lỗi.
func (s *SessionStore) Delete(ctx context.Context, id string) error {
	return s.rdb.Del(ctx, sessionKey(id)).Err()
}
