#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TARGETS=${*:-postgres s3 mysql mariadb s3-driver}
PG_IMAGE=${PG_IMAGE:-postgres:16.15@sha256:5db6dac2ef5dcff512f4f00a009c3c332b9f2f28f24277175bf3e4a3d02ef83a}
MYSQL_IMAGE=${MYSQL_IMAGE:-mysql:8.4.13@sha256:a81d043930e51745522bd5c430961cf8286f0a23f0a9a2a6e0108f7ea71b0902}
MARIADB_IMAGE=${MARIADB_IMAGE:-mariadb:11.4.13@sha256:d1891ede5a559f1086f55053bb8cc1569b0298338c4d72d71725a7f74f6ba620}
MINIO_IMAGE=${MINIO_IMAGE:-cgr.dev/chainguard/minio@sha256:090010a3f73f6aa2dff606c61cd0de3640b22748abe19f4e48cce6dc9de7a53f}
CISTERN_IMAGE=${CISTERN_IMAGE:-cistern:dev}
AWS_CLI_IMAGE=${AWS_CLI_IMAGE:-amazon/aws-cli:2.39.3@sha256:d557b3524475d70dd3104cf44d96568ee3188897b73a6ece2b37aa50e1af362f}
MINIO_USER=e2eadmin
MINIO_PASS=e2e-minio-secret
ID=cistern-e2e-$$
WORK=$(mktemp -d)

cleanup() {
  docker rm -f -v "$ID-pg" "$ID-minio" "$ID-mysql" "$ID-tools" "$ID-ipg" "$ID-imysql" "$ID-imaria" >/dev/null 2>&1 || true
  docker volume rm "$ID-data" >/dev/null 2>&1 || true
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
  docker rm -f -v "$ID-pg" >/dev/null 2>&1 || true
  step "start $PG_IMAGE"
  docker run -d --name "$ID-pg" --network "$ID" -e POSTGRES_PASSWORD=e2e-secret "$PG_IMAGE" >/dev/null
  for _ in $(seq 1 60); do
    docker exec "$ID-pg" pg_isready -q -h 127.0.0.1 -U postgres && break
    sleep 1
  done
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
  docker rm -f -v "$ID-minio" >/dev/null 2>&1 || true
  step "start $MINIO_IMAGE"
  docker run -d --name "$ID-minio" --network "$ID" --network-alias minio \
    -e MINIO_ROOT_USER="$MINIO_USER" -e MINIO_ROOT_PASSWORD="$MINIO_PASS" \
    --tmpfs /data:rw,mode=1777 "$MINIO_IMAGE" server /data >/dev/null
  for _ in $(seq 1 60); do
    docker exec "$1" bash -c 'exec 3<>/dev/tcp/minio/9000' 2>/dev/null && return
    sleep 1
  done
  docker logs "$ID-minio"
  fail "minio did not start"
}

start_mysql() {
  docker rm -f -v "$ID-mysql" >/dev/null 2>&1 || true
  step "start $1"
  docker run -d --name "$ID-mysql" --network "$ID" -e MYSQL_ROOT_PASSWORD=e2e-secret -e MARIADB_ROOT_PASSWORD=e2e-secret "$1" >/dev/null
  for _ in $(seq 1 90); do
    docker exec -e MYSQL_PWD=e2e-secret "$ID-mysql" "$2" -h127.0.0.1 -uroot -e 'SELECT 1' >/dev/null 2>&1 && break
    sleep 1
  done
  docker exec -i "$ID-mysql" sh -c 'cat > /usr/local/bin/cistern && chmod +x /usr/local/bin/cistern' < "$WORK/cistern"
  docker exec -i -e CLI="$2" "$ID-mysql" bash -s < "$ROOT/scripts/e2e/mysql.sh"
}

run_s3() {
  start_pg
  start_minio "$ID-pg"
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

run_s3_driver() {
  docker rm -f -v "$ID-tools" >/dev/null 2>&1 || true
  step "start tools container ($AWS_CLI_IMAGE + cistern)"
  docker run -d --name "$ID-tools" --network "$ID" -e AWS_ACCESS_KEY_ID="$MINIO_USER" -e AWS_SECRET_ACCESS_KEY="$MINIO_PASS" \
    --entrypoint sleep "$AWS_CLI_IMAGE" infinity >/dev/null
  docker exec -i "$ID-tools" sh -c 'cat > /usr/local/bin/cistern && chmod +x /usr/local/bin/cistern' < "$WORK/cistern"
  start_minio "$ID-tools"
  docker exec -i "$ID-tools" bash -s < "$ROOT/scripts/e2e/s3-driver.sh"
}

run_image() {
  docker image inspect "$CISTERN_IMAGE" >/dev/null 2>&1 || fail "image $CISTERN_IMAGE not found: docker build -t $CISTERN_IMAGE ."
  step "image $CISTERN_IMAGE: start PostgreSQL, MySQL, MariaDB"
  docker run -d --name "$ID-ipg" --network "$ID" --network-alias pg -e POSTGRES_PASSWORD=e2e-secret "$PG_IMAGE" >/dev/null
  docker run -d --name "$ID-imysql" --network "$ID" --network-alias mysql -e MYSQL_ROOT_PASSWORD=e2e-secret "$MYSQL_IMAGE" >/dev/null
  docker run -d --name "$ID-imaria" --network "$ID" --network-alias mariadb -e MARIADB_ROOT_PASSWORD=e2e-secret "$MARIADB_IMAGE" >/dev/null
  for _ in $(seq 1 90); do docker exec "$ID-ipg" pg_isready -q -h 127.0.0.1 -U postgres && break; sleep 1; done
  for c in "$ID-imysql:mysql" "$ID-imaria:mariadb"; do
    for _ in $(seq 1 90); do docker exec -e MYSQL_PWD=e2e-secret "${c%%:*}" "${c##*:}" -h127.0.0.1 -uroot -e 'SELECT 1' >/dev/null 2>&1 && break; sleep 1; done
  done

  step "seed"
  docker exec "$ID-ipg" psql -q -U postgres -c "CREATE DATABASE app" >/dev/null
  docker exec "$ID-ipg" psql -q -U postgres -d app -c "CREATE TABLE t AS SELECT g AS id, md5(g::text) AS v FROM generate_series(1, 50000) g" >/dev/null
  for c in "$ID-imysql:mysql" "$ID-imaria:mariadb"; do
    docker exec -e MYSQL_PWD=e2e-secret "${c%%:*}" "${c##*:}" -h127.0.0.1 -uroot --default-character-set=utf8mb4 -e "CREATE DATABASE app CHARACTER SET utf8mb4; CREATE TABLE app.t (id INT PRIMARY KEY, v VARCHAR(40)); INSERT INTO app.t VALUES (1,'một'),(2,'hai'),(3,'ba 🚀'); CREATE PROCEDURE app.p() SELECT COUNT(*) FROM app.t;"
  done
  pgsum() { docker exec "$ID-ipg" psql -At -U postgres -d "$1" -c "SELECT md5(string_agg(t::text, ',' ORDER BY id)) FROM t"; }
  mysum() { docker exec -e MYSQL_PWD=e2e-secret "$1" "$2" -h127.0.0.1 -uroot -N -B --default-character-set=utf8mb4 -e "SELECT CONCAT(COUNT(*), ':', BIT_XOR(CRC32(CONCAT_WS('#', id, v)))) FROM $3.t"; }

  step "config and key in a volume, written as the image user"
  docker volume create "$ID-data" >/dev/null
  docker run --rm -v "$ID-data:/data" --user 0 --entrypoint sh "$CISTERN_IMAGE" -c 'mkdir -p /data/repo && chown -R 10001:10001 /data'
  docker run --rm -v "$ID-data:/data" "$CISTERN_IMAGE" keygen --out /data/identity.txt > "$WORK/recipient.txt"
  cat > "$WORK/cistern.yaml" <<'YAML'
repositories:
  local: { type: local, path: /data/repo }
encryption:
  recipients_file: /data/recipient.txt
  identity_file: /data/identity.txt
jobs:
  - { name: pg, driver: postgres, params: { host: pg, user: postgres, password: "${DB_PASSWORD}", databases: [app] } }
  - { name: my, driver: mysql, params: { host: mysql, user: root, password: "${DB_PASSWORD}", databases: [app] } }
  - { name: maria, driver: mysql, params: { host: mariadb, user: root, password: "${DB_PASSWORD}", databases: [app] } }
YAML
  docker run --rm -i -v "$ID-data:/data" --entrypoint sh "$CISTERN_IMAGE" -c 'cat > /data/cistern.yaml' < "$WORK/cistern.yaml"
  docker run --rm -i -v "$ID-data:/data" --entrypoint sh "$CISTERN_IMAGE" -c 'cat > /data/recipient.txt' < "$WORK/recipient.txt"

  cis() {
    docker run --rm --network "$ID" --read-only --tmpfs /tmp -v "$ID-data:/data" \
      -e CISTERN_CONFIG=/data/cistern.yaml -e DB_PASSWORD=e2e-secret "$CISTERN_IMAGE" "$@"
  }
  step "check, backup, verify (read-only root filesystem, uid 10001)"
  cis check --all
  cis backup --all
  for j in pg my maria; do cis verify --job "$j"; done
  cis list --job maria

  step "restore into new databases and compare"
  for j in pg my maria; do cis restore --job "$j" --set restore_suffix=_restored --yes; done
  [ "$(pgsum app)" = "$(pgsum app_restored)" ] || fail "postgres checksum mismatch"
  [ "$(mysum "$ID-imysql" mysql app)" = "$(mysum "$ID-imysql" mysql app_restored)" ] || fail "mysql checksum mismatch"
  [ "$(mysum "$ID-imaria" mariadb app)" = "$(mysum "$ID-imaria" mariadb app_restored)" ] || fail "mariadb checksum mismatch"
  docker exec -e MYSQL_PWD=e2e-secret "$ID-imaria" mariadb -h127.0.0.1 -uroot -N -e "CALL app_restored.p()" | grep -qx 3 || fail "mariadb procedure not restored"
  echo "postgres, mysql and mariadb restored with matching checksums"
  echo; echo "E2E IMAGE: PASS"
}

for t in $TARGETS; do
  case $t in
    postgres) start_pg; inner postgres.sh ;;
    s3) run_s3 ;;
    mysql) start_mysql "$MYSQL_IMAGE" mysql ;;
    mariadb) start_mysql "$MARIADB_IMAGE" mariadb ;;
    s3-driver) run_s3_driver ;;
    image) run_image ;;
    *) fail "unknown target $t (postgres | s3 | mysql | mariadb | s3-driver | image)" ;;
  esac
done
