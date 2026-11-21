# note: call scripts from /scripts

# Thư mục vendor/ của template làm Go báo "inconsistent vendoring" khi thêm
# dependency, nên dùng module cache. Luôn chạy go qua make để cờ này có hiệu lực.
export GOFLAGS := -mod=mod

COMPOSE := docker compose --env-file .env -f deployments/docker-compose.yml

.PHONY: up down run test smoke

.env:
	cp .env.example .env

# Bật Redis (tạo .env từ .env.example nếu chưa có).
up: .env
	$(COMPOSE) up -d redis

# Tắt container, giữ dữ liệu trong volume.
down: .env
	$(COMPOSE) down

# Chạy server với cấu hình từ .env.
run: .env
	set -a; . ./.env; set +a; \
	REDIS_ADDR="localhost:$$REDIS_PORT" go run ./cmd/server

# Build, vet và test với race detector.
test:
	go build ./...
	go vet ./...
	go test -race ./...

# Smoke test end-to-end: tự dựng Redis tạm + server riêng (cổng 8099), không đụng dữ liệu dev.
smoke:
	./scripts/smoke.sh
