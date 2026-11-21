package ping

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// countKey là ZSET: member = username, score = số lần ping được chấp nhận.
// Một cấu trúc phục vụ cả "đếm theo từng người" (ZSCORE) lẫn /top (ZREVRANGE).
const countKey = "ping:count"

// Admission là kết quả của một lần ping được chấp nhận.
type Admission struct {
	Count int64 // tổng số lần username này đã ping thành công, tính cả lần này
}

// Admitter ghi nhận một lần ping được chấp nhận. Chỉ được gọi **sau khi đã giữ khoá**
// và **trước khi sleep**, nên chỉ những lần ping thực sự chạy mới được tính; request
// bị 429 không bao giờ tới đây.
//
// Là interface để test giả lập lỗi, và để các PR sau thêm rate limit mà không phải
// đổi handler.
type Admitter interface {
	Admit(ctx context.Context, username string) (Admission, error)
}

// RedisAdmitter đếm bằng ZINCRBY trên ping:count.
type RedisAdmitter struct {
	rdb redis.Cmdable
}

func NewAdmitter(rdb redis.Cmdable) *RedisAdmitter {
	return &RedisAdmitter{rdb: rdb}
}

// Admit tăng bộ đếm của username thêm 1 và trả về giá trị mới.
// ZINCRBY nguyên tử trên Redis nên không mất lượt đếm khi nhiều instance cùng ghi.
func (a *RedisAdmitter) Admit(ctx context.Context, username string) (Admission, error) {
	score, err := a.rdb.ZIncrBy(ctx, countKey, 1, username).Result()
	if err != nil {
		return Admission{}, err
	}
	return Admission{Count: int64(score)}, nil
}
