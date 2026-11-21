// Package ping chứa logic của /ping: khoá độc quyền, đếm, rate limit, thống kê.
package ping

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"
)

const lockKey = "ping:lock"

// Lock là khoá phân tán trong Redis: tại một thời điểm chỉ một người giữ được,
// kể cả khi có nhiều instance server (sync.Mutex chỉ có tác dụng trong một tiến trình).
//
// Cơ chế: SET ping:lock <token> NX EX <ttl>.
//   - NX: chỉ đặt khi key chưa tồn tại, nên đúng một người thành công.
//   - EX: khoá tự hết hạn, nên nếu người giữ khoá chết (crash, kill -9) thì khoá
//     không kẹt mãi mà tự nhả sau ttl.
//   - token ngẫu nhiên: chỉ người đang giữ khoá mới nhả được (xem Release).
type Lock struct {
	rdb redis.Cmdable
	key string
	ttl time.Duration
}

func NewLock(rdb redis.Cmdable, ttl time.Duration) *Lock {
	return &Lock{rdb: rdb, key: lockKey, ttl: ttl}
}

// Acquire thử giành khoá một lần, không chờ. Trả (token, true, nil) khi giành được,
// ("", false, nil) khi người khác đang giữ, và lỗi khi Redis hỏng.
func (l *Lock) Acquire(ctx context.Context) (string, bool, error) {
	token, err := newToken()
	if err != nil {
		return "", false, err
	}
	ok, err := l.rdb.SetNX(ctx, l.key, token, l.ttl).Result()
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	return token, true, nil
}

// releaseScript chỉ xoá khoá nếu nó vẫn thuộc về token này.
//
// Phải là script Lua (chạy nguyên tử trên Redis) chứ không thể GET rồi DEL riêng:
// giữa hai lệnh đó khoá có thể hết hạn và người khác giành được, khi đó DEL của
// chúng ta sẽ xoá nhầm khoá của họ.
var releaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
else
    return 0
end`)

// Release nhả khoá nếu token vẫn là chủ khoá. Trả false khi khoá đã hết hạn hoặc
// đã thuộc về người khác (không phải lỗi: chỉ có nghĩa là không còn gì để nhả).
func (l *Lock) Release(ctx context.Context, token string) (bool, error) {
	n, err := releaseScript.Run(ctx, l.rdb, []string{l.key}, token).Int()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
