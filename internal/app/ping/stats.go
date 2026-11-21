package ping

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// TopN là số người hiển thị trong /top.
const TopN = 10

// TopEntry là một dòng của bảng xếp hạng.
type TopEntry struct {
	Username string `json:"username"`
	Count    int64  `json:"count"`
}

// Stats đọc thống kê từ Redis.
type Stats struct {
	rdb redis.Cmdable
}

func NewStats(rdb redis.Cmdable) *Stats {
	return &Stats{rdb: rdb}
}

// Top trả về tối đa n người có số lần ping thành công nhiều nhất, từ cao xuống thấp.
// Luôn trả slice không nil (rỗng thì là [] chứ không phải null khi mã hoá JSON).
//
// Đồng hạng được Redis xếp theo username **ngược thứ tự chữ cái** (ZREVRANGE đảo cả
// thứ tự phụ), nên kết quả luôn xác định.
func (s *Stats) Top(ctx context.Context, n int64) ([]TopEntry, error) {
	if n <= 0 {
		// Không có chốt này thì ZREVRANGE 0 -1 (n = 0) trả về TOÀN BỘ danh sách.
		return []TopEntry{}, nil
	}
	zs, err := s.rdb.ZRevRangeWithScores(ctx, countKey, 0, n-1).Result()
	if err != nil {
		return nil, err
	}
	out := make([]TopEntry, 0, len(zs))
	for _, z := range zs {
		out = append(out, TopEntry{Username: z.Member.(string), Count: int64(z.Score)})
	}
	return out, nil
}

// Count trả về số người khác nhau đã ping thành công, **xấp xỉ** (HyperLogLog, sai số
// chuẩn khoảng 0,81%). Chưa có ai thì là 0.
func (s *Stats) Count(ctx context.Context) (int64, error) {
	return s.rdb.PFCount(ctx, uniqKey).Result()
}

// statsReader là phần của Stats mà handler cần; là interface để test giả lập lỗi.
type statsReader interface {
	Top(ctx context.Context, n int64) ([]TopEntry, error)
	Count(ctx context.Context) (int64, error)
}

// StatsHandler phục vụ các route thống kê.
type StatsHandler struct {
	stats statsReader
}

func NewStatsHandler(rdb redis.Cmdable) *StatsHandler {
	return &StatsHandler{stats: NewStats(rdb)}
}

// Routes đăng ký route. r phải là nhóm route đã gắn auth.RequireAuth().
func (h *StatsHandler) Routes(r gin.IRoutes) {
	r.GET("/top", h.top)
	r.GET("/count", h.count)
}

// top: GET /top (cần đăng nhập)
//
//	200 [{"username","count"}, ...]  tối đa 10 người, nhiều nhất trước; [] khi chưa có ai
//	503 {"error":"unavailable"}      Redis lỗi
func (h *StatsHandler) top(c *gin.Context) {
	entries, err := h.stats.Top(c.Request.Context(), TopN)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	c.JSON(http.StatusOK, entries)
}

// count: GET /count (cần đăng nhập)
//
//	200 {"approx_unique_users": N}  số người khác nhau đã gọi /ping, xấp xỉ (HyperLogLog)
//	503 {"error":"unavailable"}     Redis lỗi
func (h *StatsHandler) count(c *gin.Context) {
	n, err := h.stats.Count(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"approx_unique_users": n})
}
