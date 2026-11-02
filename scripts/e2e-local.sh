#!/usr/bin/env bash
# End-to-end với hệ thống thật trong Docker.
#   ./scripts/e2e-local.sh              # tất cả
#   ./scripts/e2e-local.sh postgres     # PostgreSQL + repository local
#   ./scripts/e2e-local.sh s3           # PostgreSQL + repository S3 (MinIO) có Object Lock
#   ./scripts/e2e-local.sh mysql        # MySQL + repository local
#   ./scripts/e2e-local.sh mariadb      # MariaDB + repository local
# Biến môi trường: PG_IMAGE, MYSQL_IMAGE, MARIADB_IMAGE, MINIO_IMAGE, AWS_CLI_IMAGE,
# SKIP_DR=1 (bỏ bước cài age để khôi phục thủ công).
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TARGETS=${*:-postgres s3 mysql mariadb}
PG_IMAGE=${PG_IMAGE:-postgres:16}
MYSQL_IMAGE=${MYSQL_IMAGE:-mysql:8.4}
MARIADB_IMAGE=${MARIADB_IMAGE:-mariadb:11.4}
# Docker Hub không còn image minio/minio; dùng bản dựng của Chainguard.
MINIO_IMAGE=${MINIO_IMAGE:-cgr.dev/chainguard/minio:latest}
AWS_CLI_IMAGE=${AWS_CLI_IMAGE:-amazon/aws-cli:latest}
MINIO_USER=e2eadmin
MINIO_PASS=e2e-minio-secret
ID=cistern-e2e-$$
WORK=$(mktemp -d)

cleanup() {
  docker rm -f "$ID-pg" "$ID-minio" "$ID-mysql" >/dev/null 2>&1 || true
  docker network rm "$ID" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT
step() { echo; echo "==> $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

ARCH=$(docker version -f '{{.Server.Arch}}')
step "build cistern linux/$ARCH"
(cd "$ROOT" && GOOS=linux GOARCH=$ARCH CGO_ENABLED=0 go build -o "$WORK/cistern" ./cmd/cistern)
docker network create "$ID" >/dev/null

start_pg() {
  docker rm -f "$ID-pg" >/dev/null 2>&1 || true
  step "start $PG_IMAGE"
  docker run -d --name "$ID-pg" --network "$ID" -e POSTGRES_PASSWORD=e2e-secret "$PG_IMAGE" >/dev/null
  for _ in $(seq 1 60); do
    # -h 127.0.0.1: bỏ qua server tạm của bước init (chỉ nghe unix socket)
    docker exec "$ID-pg" pg_isready -q -h 127.0.0.1 -U postgres && break
    sleep 1
  done
  # Binary và script đi qua stdin, không cần chia sẻ thư mục với Docker.
  docker exec -i "$ID-pg" sh -c 'cat > /usr/local/bin/cistern && chmod +x /usr/local/bin/cistern' < "$WORK/cistern"
}

inner() {
  local script=$1
  shift
  docker exec -i -e SKIP_DR="${SKIP_DR:-}" -e S3_ACCESS_KEY="$MINIO_USER" -e S3_SECRET_KEY="$MINIO_PASS" \
    "$ID-pg" bash -s -- "$@" < "$ROOT/scripts/e2e/$script"
}

aws() {
  docker run --rm --network "$ID" -e AWS_ACCESS_KEY_ID="$MINIO_USER" -e AWS_SECRET_ACCESS_KEY="$MINIO_PASS" \
    -e AWS_DEFAULT_REGION=us-east-1 -e AWS_PAGER= "$AWS_CLI_IMAGE" --endpoint-url http://minio:9000 "$@"
}

start_minio() {
  step "start $MINIO_IMAGE"
  docker run -d --name "$ID-minio" --network "$ID" --network-alias minio \
    -e MINIO_ROOT_USER="$MINIO_USER" -e MINIO_ROOT_PASSWORD="$MINIO_PASS" \
    --tmpfs /data:rw,mode=1777 "$MINIO_IMAGE" server /data >/dev/null
  for _ in $(seq 1 60); do
    docker exec "$ID-pg" bash -c 'exec 3<>/dev/tcp/minio/9000' 2>/dev/null && return
    sleep 1
  done
  docker logs "$ID-minio"
  fail "minio did not start"
}

# start_mysql IMAGE CLI: MySQL hoặc MariaDB, cistern chạy bên trong container.
start_mysql() {
  docker rm -f "$ID-mysql" >/dev/null 2>&1 || true
  step "start $1"
  docker run -d --name "$ID-mysql" --network "$ID" -e MYSQL_ROOT_PASSWORD=e2e-secret -e MARIADB_ROOT_PASSWORD=e2e-secret "$1" >/dev/null
  for _ in $(seq 1 90); do
    # TCP 127.0.0.1: server tạm của bước init chỉ nghe socket
    docker exec -e MYSQL_PWD=e2e-secret "$ID-mysql" "$2" -h127.0.0.1 -uroot -e 'SELECT 1' >/dev/null 2>&1 && break
    sleep 1
  done
  docker exec -i "$ID-mysql" sh -c 'cat > /usr/local/bin/cistern && chmod +x /usr/local/bin/cistern' < "$WORK/cistern"
  docker exec -i -e CLI="$2" "$ID-mysql" bash -s < "$ROOT/scripts/e2e/mysql.sh"
}

run_s3() {
  start_pg
  start_minio
  step "create bucket with Object Lock (aws cli)"
  aws s3api create-bucket --bucket backups --object-lock-enabled-for-bucket >/dev/null
  aws s3api get-object-lock-configuration --bucket backups --output text

  inner s3.sh setup
  inner s3.sh backup
  FIRST=$(docker exec "$ID-pg" cat /e2e/last_id)
  inner s3.sh verify-restore

  step "check what cistern wrote, with aws cli"
  MK=cistern/orders-db/$FIRST/manifest.json
  BIG=cistern/orders-db/$FIRST/artifacts/db/orders.dump.gz.age
  MODE=$(aws s3api get-object-retention --bucket backups --key "$MK" --query Retention.Mode --output text)
  UNTIL=$(aws s3api get-object-retention --bucket backups --key "$BIG" --query Retention.RetainUntilDate --output text)
  echo "manifest retention: $MODE, artifact retain until: $UNTIL"
  [ "$MODE" = COMPLIANCE ] || fail "manifest not locked in COMPLIANCE mode"
  ETAG=$(aws s3api head-object --bucket backups --key "$BIG" --query ETag --output text)
  SIZE=$(aws s3api head-object --bucket backups --key "$BIG" --query ContentLength --output text)
  echo "orders.dump: $SIZE bytes, ETag $ETAG"
  [[ "$ETAG" == *-* ]] || fail "expected a multipart upload (ETag with -N)"
  # MinIO bỏ KeyCount khi bằng 0, nên đếm Contents.
  LOCKS=$(aws s3api list-objects-v2 --bucket backups --prefix cistern/locks/ --query 'length(Contents || `[]`)' --output text)
  [ "$LOCKS" = 0 ] || fail "job lock not released ($LOCKS objects under locks/)"
  echo "job lock released (only a delete marker remains)"

  step "locked version cannot be deleted (COMPLIANCE)"
  VID=$(aws s3api list-object-versions --bucket backups --prefix "$MK" --query 'Versions[0].VersionId' --output text)
  if aws s3api delete-object --bucket backups --key "$MK" --version-id "$VID" > "$WORK/del.txt" 2>&1; then
    fail "deleting a locked version succeeded"
  fi
  cat "$WORK/del.txt"

  step "job lock held by another host (If-None-Match)"
  docker run --rm --network "$ID" -e AWS_ACCESS_KEY_ID="$MINIO_USER" -e AWS_SECRET_ACCESS_KEY="$MINIO_PASS" \
    -e AWS_DEFAULT_REGION=us-east-1 --entrypoint sh "$AWS_CLI_IMAGE" -c \
    'printf %s "{\"host\":\"other-host\",\"pid\":4242,\"op\":\"backup\",\"started\":\"2026-01-01T00:00:00Z\",\"expires\":\"2099-01-01T00:00:00Z\"}" > /tmp/l &&
     aws --endpoint-url http://minio:9000 s3api put-object --bucket backups --key cistern/locks/orders-db.lock --body /tmp/l >/dev/null'
  inner s3.sh locked
  aws s3api delete-object --bucket backups --key cistern/locks/orders-db.lock >/dev/null

  inner s3.sh backup
  inner s3.sh prune

  step "pruned snapshot: hidden from listing, locked data still retained"
  aws s3api list-object-versions --bucket backups --prefix "$MK" \
    --query '{versions: length(Versions), delete_markers: length(DeleteMarkers)}' --output text
  [ "$(aws s3api list-object-versions --bucket backups --prefix "$MK" --query 'length(DeleteMarkers)' --output text)" = 1 ] || fail "expected a delete marker"
  aws s3api get-object --bucket backups --key "$MK" --version-id "$VID" /tmp/m.json >/dev/null || fail "locked version not readable"
  echo "locked version $VID still readable"
  echo; echo "E2E S3 + OBJECT LOCK: PASS"
}

for t in $TARGETS; do
  case $t in
    postgres) start_pg; inner postgres.sh ;;
    s3) run_s3 ;;
    mysql) start_mysql "$MYSQL_IMAGE" mysql ;;
    mariadb) start_mysql "$MARIADB_IMAGE" mariadb ;;
    *) fail "unknown target $t (postgres | s3 | mysql | mariadb)" ;;
  esac
done
