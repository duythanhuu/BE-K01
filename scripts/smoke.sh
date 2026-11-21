#!/usr/bin/env bash
# Smoke test end-to-end cho toàn bộ server.
#
# Tự dựng một Redis tạm (docker) và một server riêng ở cổng khác, nên KHÔNG đụng tới
# dữ liệu của môi trường dev (make up / make run). Dọn sạch khi xong.
#
# Chạy: make smoke    (hoặc ./scripts/smoke.sh)
# Cần: docker, go, curl. Biến tuỳ chọn: SMOKE_PORT, SMOKE_REDIS_PORT, SMOKE_PING_SLEEP.

set -uo pipefail
cd "$(dirname "$0")/.."
export GOFLAGS=-mod=mod # bỏ qua vendor/ của template, giống Makefile

PORT="${SMOKE_PORT:-8099}"
REDIS_PORT="${SMOKE_REDIS_PORT:-6390}"
PING_SLEEP="${SMOKE_PING_SLEEP:-1s}"
REDIS_PASS="smoke"
REDIS_NAME="bek01-smoke-redis"
BASE="http://127.0.0.1:${PORT}"
JSON='Content-Type: application/json'

WORK="$(mktemp -d)"
SERVER_PID=""
passes=0
fails=0

cleanup() {
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null
    wait "$SERVER_PID" 2>/dev/null
  fi
  docker rm -f "$REDIS_NAME" >/dev/null 2>&1
  rm -rf "$WORK"
}
trap cleanup EXIT

ok()  { passes=$((passes + 1)); printf '  ok    %s\n' "$1"; }
bad() { fails=$((fails + 1)); printf '  FAIL  %s\n          mong đợi : %s\n          nhận được: %s\n' "$1" "$2" "$3"; }
expect() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "$2" "$3"; fi; }
expect_contains() { case "$3" in *"$2"*) ok "$1" ;; *) bad "$1" "chứa: $2" "$3" ;; esac; }

code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
body() { curl -s "$@"; }
post_json() { # url, json, [curl args...]
  local url="$1" data="$2"; shift 2
  curl -s -H "$JSON" -d "$data" "$@" "$url"
}
creds() { printf '{"username":"%s","password":"correct horse"}' "$1"; }

step() { printf '\n== %s ==\n' "$1"; }

# ---------- dựng môi trường ----------
step "Dựng Redis tạm và server (PING_SLEEP=${PING_SLEEP})"
docker rm -f "$REDIS_NAME" >/dev/null 2>&1
docker run -d --rm --name "$REDIS_NAME" -p "127.0.0.1:${REDIS_PORT}:6379" redis:7-alpine \
  redis-server --requirepass "$REDIS_PASS" >/dev/null || { echo "không chạy được Redis (docker?)"; exit 2; }
for _ in $(seq 100); do
  docker exec "$REDIS_NAME" redis-cli -a "$REDIS_PASS" --no-auth-warning ping 2>/dev/null | grep -q PONG && break
  sleep 0.1
done

go build -o "$WORK/server" ./cmd/server || { echo "build lỗi"; exit 2; }
HTTP_ADDR="127.0.0.1:${PORT}" REDIS_ADDR="127.0.0.1:${REDIS_PORT}" REDIS_PASSWORD="$REDIS_PASS" \
  PING_SLEEP="$PING_SLEEP" "$WORK/server" >"$WORK/server.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 100); do curl -sf "$BASE/healthz" >/dev/null && break; sleep 0.1; done
curl -sf "$BASE/healthz" >/dev/null || { echo "server không lên được:"; cat "$WORK/server.log"; exit 2; }
ok "server và Redis sẵn sàng"

# ---------- 1. chưa đăng nhập ----------
step "1. Chưa đăng nhập"
expect "GET /ping  -> 401" 401 "$(code "$BASE/ping")"
expect "GET /top   -> 401" 401 "$(code "$BASE/top")"
expect "GET /count -> 401" 401 "$(code "$BASE/count")"
expect "GET /me    -> 401" 401 "$(code "$BASE/me")"

# ---------- 2. đăng ký, đăng nhập ----------
step "2. Đăng ký và đăng nhập"
expect "đăng ký alice -> 201" 201 "$(code -X POST -H "$JSON" -d "$(creds alice)" "$BASE/register")"
expect "đăng ký bob   -> 201" 201 "$(code -X POST -H "$JSON" -d "$(creds bob)" "$BASE/register")"
expect "đăng ký trùng username (khác hoa/thường) -> 409" 409 \
  "$(code -X POST -H "$JSON" -d '{"username":"ALICE","password":"another pass"}' "$BASE/register")"
expect "mật khẩu quá ngắn -> 400" 400 \
  "$(code -X POST -H "$JSON" -d '{"username":"carol","password":"short"}' "$BASE/register")"

wrong="$(post_json "$BASE/login" '{"username":"alice","password":"wrong horse!"}' -w ' [%{http_code}]')"
nouser="$(post_json "$BASE/login" '{"username":"nobody","password":"wrong horse!"}' -w ' [%{http_code}]')"
expect "đăng nhập sai mật khẩu -> 401 invalid_credentials" '{"error":"invalid_credentials"} [401]' "$wrong"
expect "username không tồn tại cho response GIỐNG HỆT" "$wrong" "$nouser"

post_json "$BASE/login" "$(creds alice)" -c "$WORK/alice.jar" -o /dev/null
post_json "$BASE/login" "$(creds bob)" -c "$WORK/bob.jar" -o /dev/null
expect "alice /me" '{"username":"alice"}' "$(body -b "$WORK/alice.jar" "$BASE/me")"
expect "cookie session là HttpOnly" 1 "$(grep -c '^#HttpOnly_' "$WORK/alice.jar")"

# ---------- 3. /ping chỉ một người tại một thời điểm ----------
step "3. /ping: chỉ một người chạy tại một thời điểm"
( body -b "$WORK/alice.jar" "$BASE/ping" >"$WORK/alice.ping" ) &
alice_pid=$!
sleep 0.3
expect "bob ping khi alice đang chạy -> 429 busy" '{"error":"busy"} [429]' \
  "$(body -b "$WORK/bob.jar" -w ' [%{http_code}]' "$BASE/ping")"
wait "$alice_pid"
expect "alice ping xong, count = 1" '{"count":1,"message":"pong","username":"alice"}' "$(cat "$WORK/alice.ping")"

# ---------- 4. đếm + rate limit ----------
step "4. Đếm và rate limit (2 lần / 60s)"
expect "alice ping lần 2, count = 2" '{"count":2,"message":"pong","username":"alice"}' \
  "$(body -b "$WORK/alice.jar" "$BASE/ping")"
third="$(curl -s -i -b "$WORK/alice.jar" "$BASE/ping" | tr -d '\r')"
expect_contains "alice ping lần 3 -> 429" "429" "$(printf '%s' "$third" | head -1)"
expect_contains "lần 3 là rate_limited" '"error":"rate_limited"' "$third"
expect_contains "lần 3 có header Retry-After" "Retry-After: " "$third"
expect "bob ping (hạn ngạch độc lập), count = 1" '{"count":1,"message":"pong","username":"bob"}' \
  "$(body -b "$WORK/bob.jar" "$BASE/ping")"

# ---------- 5. /top và /count ----------
step "5. /top và /count"
expect "/top: alice 2, bob 1 (lần bị 429 không được tính)" \
  '[{"username":"alice","count":2},{"username":"bob","count":1}]' "$(body -b "$WORK/alice.jar" "$BASE/top")"
expect "/count: 2 người khác nhau" '{"approx_unique_users":2}' "$(body -b "$WORK/alice.jar" "$BASE/count")"

# ---------- 6. đăng xuất ----------
step "6. Đăng xuất"
expect "alice logout -> 204" 204 "$(code -X POST -b "$WORK/alice.jar" "$BASE/logout")"
expect "alice /me sau logout -> 401" 401 "$(code -b "$WORK/alice.jar" "$BASE/me")"
expect "bob vẫn đăng nhập -> 200" 200 "$(code -b "$WORK/bob.jar" "$BASE/me")"

# ---------- 7. Redis chết ----------
step "7. Redis chết"
docker stop "$REDIS_NAME" >/dev/null
expect "/healthz -> 503" 503 "$(code "$BASE/healthz")"
expect "/me khi Redis chết -> 503 (không phải 401)" 503 "$(code -b "$WORK/bob.jar" "$BASE/me")"
expect "/login khi Redis chết -> 503" 503 "$(code -X POST -H "$JSON" -d "$(creds bob)" "$BASE/login")"

# ---------- kết quả ----------
printf '\n== Kết quả: %d đạt, %d lỗi ==\n' "$passes" "$fails"
[ "$fails" -eq 0 ]
