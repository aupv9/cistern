#!/usr/bin/env bash
# End-to-end với hệ thống thật trong Docker. Hiện có: PostgreSQL.
#   ./scripts/e2e-local.sh            # postgres:16
#   PG_IMAGE=postgres:17 ./scripts/e2e-local.sh
# Cần: Docker, Go. Container cần mạng để cài `age` cho bước khôi phục thủ công (SKIP_DR=1 để bỏ qua).
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
PG_IMAGE=${PG_IMAGE:-postgres:16}
NAME=cistern-e2e-$$
WORK=$(mktemp -d)
cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

ARCH=$(docker version -f '{{.Server.Arch}}')
echo "==> build cistern linux/$ARCH"
(cd "$ROOT" && GOOS=linux GOARCH=$ARCH CGO_ENABLED=0 go build -o "$WORK/cistern" ./cmd/cistern)

echo "==> start $PG_IMAGE"
docker run -d --name "$NAME" -e POSTGRES_PASSWORD=e2e-secret "$PG_IMAGE" >/dev/null
for i in $(seq 1 60); do
  # -h 127.0.0.1: bỏ qua server tạm của bước init (chỉ nghe unix socket)
  docker exec "$NAME" pg_isready -q -h 127.0.0.1 -U postgres && break
  sleep 1
done
docker exec -i "$NAME" sh -c 'cat > /usr/local/bin/cistern && chmod +x /usr/local/bin/cistern' < "$WORK/cistern"

# Binary và script được đưa vào qua stdin, không cần chia sẻ thư mục với Docker.
docker exec -i -e SKIP_DR="${SKIP_DR:-}" "$NAME" bash -s < "$ROOT/scripts/e2e/postgres.sh"
