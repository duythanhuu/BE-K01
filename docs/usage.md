# Server Gin + Redis: cách dùng

Web server nhỏ viết bằng Go (Gin) và Redis: đăng ký và đăng nhập có session, `/ping` chỉ cho một người chạy tại một thời điểm, rate limit, bảng xếp hạng và đếm số người khác nhau bằng HyperLogLog.

## Chạy

```bash
make up        # bật Redis trong docker (tự tạo .env từ .env.example)
make run       # chạy server ở :8080
make test      # build + vet + go test -race
make smoke     # test end-to-end trên Redis tạm và server riêng (không đụng dữ liệu dev)
make down      # tắt Redis, giữ dữ liệu
```

Cấu hình qua biến môi trường (`.env`):

| Biến | Mặc định | Ý nghĩa |
|---|---|---|
| `HTTP_ADDR` | `:8080` | Địa chỉ lắng nghe |
| `REDIS_ADDR` | `localhost:6379` | Địa chỉ Redis (`make run` tự ghép từ `REDIS_PORT`) |
| `REDIS_PASSWORD` | rỗng | Mật khẩu Redis |
| `COOKIE_SECURE` | `false` | `true` khi chạy sau HTTPS: cookie session chỉ gửi qua HTTPS |
| `PING_SLEEP` | `5s` | Thời gian `/ping` xử lý giả lập (đề yêu cầu 5s); đặt nhỏ hơn để thử nhanh |

## API

Đăng nhập bằng cookie `session_id` (curl: `-c jar` khi đăng nhập, `-b jar` ở các lệnh sau).

| Method | Đường dẫn | Cần đăng nhập | Thành công | Lỗi |
|---|---|---|---|---|
| POST | `/register` | không | 201 `{"username"}` | 400, 409 `username_taken` |
| POST | `/login` | không | 200 + cookie | 400, 401 `invalid_credentials` |
| POST | `/logout` | có | 204 | 401 |
| GET | `/me` | có | 200 `{"username"}` | 401 |
| GET | `/ping` | có | 200 `{"message","username","count"}` | 401, 429 `busy`, 429 `rate_limited` (+ `Retry-After`), 503 |
| GET | `/top` | có | 200 `[{"username","count"}]` (tối đa 10) | 401 |
| GET | `/count` | có | 200 `{"approx_unique_users"}` | 401 |
| GET | `/healthz` | không | 200 | 503 |

Mọi lỗi Redis đều là `503`, kể cả khi kiểm tra session (không bao giờ biến thành `401`, để người dùng không bị đăng xuất oan).

### Ví dụ

```bash
curl -X POST localhost:8080/register -H 'Content-Type: application/json' \
     -d '{"username":"alice","password":"correct horse"}'
curl -c jar -X POST localhost:8080/login -H 'Content-Type: application/json' \
     -d '{"username":"alice","password":"correct horse"}'

curl -b jar localhost:8080/ping      # chờ 5s -> {"count":1,"message":"pong","username":"alice"}
curl -b jar localhost:8080/ping      # lần 2 -> count 2
curl -i -b jar localhost:8080/ping   # lần 3 trong 60s -> 429 {"error":"rate_limited",...} + Retry-After

curl -b jar localhost:8080/top       # [{"username":"alice","count":2}]
curl -b jar localhost:8080/count     # {"approx_unique_users":1}
curl -b jar -X POST localhost:8080/logout
```

## Hành vi của `/ping`

Mỗi request đi qua các bước theo thứ tự, và dừng ngay ở bước đầu tiên thất bại:

1. **Xác thực**: cookie → session trong Redis (`401` nếu không có, `503` nếu Redis lỗi).
2. **Khoá**: `SET ping:lock <token> NX EX 10`. Có người đang giữ thì `429 busy` trả ngay, không chờ.
3. **Rate limit** (cửa sổ trượt): tối đa 2 lần trong bất kỳ 60 giây nào. Hết hạn ngạch thì nhả khoá và `429 rate_limited` kèm `Retry-After`.
4. **Ghi nhận**: trong một giao dịch, thêm vào `ping:rl:<user>`, `ZINCRBY ping:count`, `PFADD ping:uniq`.
5. **Sleep** (`PING_SLEEP`, mặc định 5s). Client ngắt kết nối thì thoát sớm.
6. **Nhả khoá** bằng Lua (chỉ xoá nếu khoá vẫn thuộc về request này), rồi trả `200`.

Chỉ những lần ping đi hết bước 4 mới được tính. Request bị `429` (bận hay rate limit) không tiêu hao hạn ngạch, không vào bộ đếm, `/top` hay `/count`. Lần ping được tính ngay ở bước 4, nên client ngắt giữa chừng vẫn tính một lần.

## Dữ liệu trong Redis

| Key | Kiểu | Nội dung | Vòng đời |
|---|---|---|---|
| `user:<username>` | HASH | `password_hash` (bcrypt), `created_at` | vĩnh viễn |
| `session:<sha256(id)>` | HASH | `username`, `created_at`; chỉ lưu **hash** của session id | TTL 30 phút, gia hạn mỗi request |
| `ping:lock` | STRING | token của người đang giữ khoá | TTL 10s (luôn lớn hơn `PING_SLEEP`) |
| `ping:rl:<username>` | ZSET | thời điểm (ms) các lần ping trong cửa sổ | `EXPIRE` 61s |
| `ping:count` | ZSET | username → số lần ping được tính (nguồn của `/top`) | vĩnh viễn |
| `ping:uniq` | HyperLogLog | các username đã ping thành công (nguồn của `/count`) | vĩnh viễn |

## Giới hạn đã biết

- **Redis là nơi lưu duy nhất** user và session: mất Redis là mất tài khoản. AOF được bật nhưng không thay được database thật.
- **Khoá `/ping` hết hạn sau 10s**: nếu một request xử lý lâu hơn TTL thì hai người có thể cùng chạy. Cách chữa thật là gia hạn khoá định kỳ hoặc fencing token.
- `kill -9` server làm khoá kẹt tối đa 10s (mọi người nhận `429 busy`) rồi tự hết hạn.
- `/login` chưa chống dò mật khẩu; `/register` trả 409 nên lộ username nào đã tồn tại.
- Cookie xác thực cần chống CSRF nếu sau này có giao diện web.
- `/count` là xấp xỉ (sai số chuẩn khoảng 0,81%); `/top` là mọi thời đại, đồng hạng xếp theo username ngược chữ cái.
- Khi Redis chết, một số request mất khoảng 0,8s trước khi báo `503` (pool của `go-redis` tự thử kết nối lại).
