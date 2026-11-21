package ping

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// countKey là ZSET: member = username, score = số lần ping được chấp nhận.
	// Một cấu trúc phục vụ cả "đếm theo từng người" (ZSCORE) lẫn /top (ZREVRANGE).
	countKey = "ping:count"

	// rateKeyPrefix + username là ZSET các lần ping gần đây của một người:
	// member = id duy nhất của lần ping, score = thời điểm (ms). Chỉ giữ trong cửa sổ.
	rateKeyPrefix = "ping:rl:"

	DefaultLimit  = 2                // tối đa 2 lần ping...
	DefaultWindow = 60 * time.Second // ...trong bất kỳ khoảng 60 giây nào
)

// Admission là kết quả của một lần ping được chấp nhận.
type Admission struct {
	Count int64 // tổng số lần username này đã ping thành công, tính cả lần này
}

// RateLimitedError được trả về khi người dùng đã dùng hết hạn ngạch trong cửa sổ.
type RateLimitedError struct {
	RetryAfter time.Duration // bao lâu nữa thì lần ping cũ nhất rời khỏi cửa sổ và được gọi lại
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("ping: vượt giới hạn, thử lại sau %v", e.RetryAfter)
}

// Admitter quyết định một lần ping có được chấp nhận không và ghi nhận nó. Chỉ được gọi
// **sau khi đã giữ khoá** và **trước khi sleep**.
//
//   - Được chấp nhận: ghi nhận (rate limit + bộ đếm) rồi trả Admission.
//   - Hết hạn ngạch: trả *RateLimitedError và KHÔNG ghi nhận gì (lần bị từ chối không
//     tiêu hao hạn ngạch và không được đếm).
type Admitter interface {
	Admit(ctx context.Context, username string) (Admission, error)
}

// Clock cho biết giờ hiện tại. Mặc định lấy từ lệnh TIME của Redis để nhiều instance
// server không bị lệch đồng hồ; test thay bằng đồng hồ giả để điều khiển thời gian.
type Clock interface {
	Now(ctx context.Context) (time.Time, error)
}

// RedisClock lấy giờ từ máy chủ Redis.
type RedisClock struct{ rdb redis.Cmdable }

func (c RedisClock) Now(ctx context.Context) (time.Time, error) {
	return c.rdb.Time(ctx).Result()
}

// RedisAdmitter áp dụng rate limit cửa sổ trượt rồi đếm.
//
// Cửa sổ trượt nghĩa là "bất kỳ khoảng 60 giây nào cũng có tối đa 2 lần": khác cửa sổ
// cố định (INCR + EXPIRE) vốn cho phép 2 lần ở cuối cửa sổ và 2 lần ở đầu cửa sổ kế
// tiếp, tức 4 lần trong vài giây. Vì giới hạn chỉ là 2 nên ZSET tối đa 2 phần tử.
type RedisAdmitter struct {
	rdb    redis.Cmdable
	clock  Clock
	limit  int
	window time.Duration
}

// NewAdmitter tạo Admitter. clock nil thì dùng giờ của Redis; limit <= 0 và window <= 0
// thì dùng mặc định (2 lần / 60 giây).
func NewAdmitter(rdb redis.Cmdable, clock Clock, limit int, window time.Duration) *RedisAdmitter {
	if clock == nil {
		clock = RedisClock{rdb: rdb}
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	if window <= 0 {
		window = DefaultWindow
	}
	return &RedisAdmitter{rdb: rdb, clock: clock, limit: limit, window: window}
}

// Admit kiểm tra hạn ngạch rồi ghi nhận.
//
// Đọc rồi mới ghi (không dùng một lệnh nguyên tử) là an toàn vì hàm này chỉ được gọi khi
// đang giữ khoá toàn cục `ping:lock`: trong cả hệ thống chỉ một request ở đây tại một
// thời điểm, nên không có hai request cùng đọc "còn 1 lượt" rồi cùng ghi. Các lần ghi
// (ZADD, EXPIRE, ZINCRBY) chạy trong một giao dịch nên cùng thành công hoặc cùng không.
func (a *RedisAdmitter) Admit(ctx context.Context, username string) (Admission, error) {
	now, err := a.clock.Now(ctx)
	if err != nil {
		return Admission{}, err
	}
	nowMs := now.UnixMilli()
	windowMs := a.window.Milliseconds()
	rlKey := rateKeyPrefix + username

	// Bỏ các lần đã rời khỏi cửa sổ (điểm <= now - window) rồi đếm số lần còn lại.
	var card *redis.IntCmd
	if _, err := a.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.ZRemRangeByScore(ctx, rlKey, "-inf", strconv.FormatInt(nowMs-windowMs, 10))
		card = p.ZCard(ctx, rlKey)
		return nil
	}); err != nil {
		return Admission{}, err
	}

	if card.Val() >= int64(a.limit) {
		// Lần cũ nhất trong cửa sổ rời đi lúc (điểm + window): đó là lúc được gọi lại.
		oldest, err := a.rdb.ZRangeWithScores(ctx, rlKey, 0, 0).Result()
		if err != nil {
			return Admission{}, err
		}
		retry := a.window
		if len(oldest) > 0 {
			retry = time.Duration(int64(oldest[0].Score)+windowMs-nowMs) * time.Millisecond
		}
		if retry <= 0 {
			retry = time.Millisecond
		}
		return Admission{}, &RateLimitedError{RetryAfter: retry}
	}

	nonce, err := newToken()
	if err != nil {
		return Admission{}, err
	}
	var incr *redis.FloatCmd
	if _, err := a.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.ZAdd(ctx, rlKey, redis.Z{Score: float64(nowMs), Member: strconv.FormatInt(nowMs, 10) + "-" + nonce})
		p.Expire(ctx, rlKey, a.window+time.Second) // dọn key của người không ping nữa
		incr = p.ZIncrBy(ctx, countKey, 1, username)
		return nil
	}); err != nil {
		return Admission{}, err
	}
	return Admission{Count: int64(incr.Val())}, nil
}
