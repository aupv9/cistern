# Chạy bên trong container postgres theo từng pha (xem run_s3 trong scripts/e2e-local.sh).
set -euo pipefail
step() { echo; echo "==> $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
export PGHOST=127.0.0.1 PGUSER=postgres PGPASSWORD=e2e-secret
export CISTERN_CONFIG=/e2e/cistern.yaml
mkdir -p /e2e && cd /e2e
sum() { psql -At -d "$1" -c "SELECT md5(string_agg(t::text, ',' ORDER BY id)) FROM $2 t"; }
count() { cistern list --job orders-db --json | grep -c '"id"' || true; }

case "$1" in
setup)
  step "keygen + config (s3 repository, COMPLIANCE 1 day, part 5 MiB)"
  cistern keygen --out identity.txt > recipient.txt
  cat > cistern.yaml <<'YAML'
repositories:
  minio:
    type: s3
    endpoint: http://minio:9000
    bucket: backups
    prefix: cistern
    path_style: true
    access_key_id: ${S3_ACCESS_KEY}
    secret_access_key: ${S3_SECRET_KEY}
    part_size_mb: 5
    object_lock: { mode: COMPLIANCE, days: 1 }
encryption:
  recipients_file: recipient.txt
  identity_file: identity.txt
jobs:
  - name: orders-db
    driver: postgres
    timeout: 10m
    retention: { last: 1 }
    params:
      host: ${PGHOST}
      user: ${PGUSER}
      password: ${PGPASSWORD}
      databases: [orders]
YAML
  step "seed (~8 MB of incompressible data)"
  psql -q -v ON_ERROR_STOP=1 -c "CREATE DATABASE orders"
  psql -q -v ON_ERROR_STOP=1 -d orders -c "CREATE TABLE orders (id int PRIMARY KEY, payload bytea)" \
    -c "INSERT INTO orders SELECT g, decode(md5(random()::text) || md5(random()::text), 'hex') FROM generate_series(1, 250000) g"
  sum orders orders > orders.md5
  step "check (bucket reachable, Object Lock enabled)"
  cistern check --job orders-db
  ;;
backup)
  step "backup"
  cistern backup --job orders-db | tee backup.out
  awk '{print $2}' backup.out > last_id
  ;;
verify-restore)
  step "verify"
  cistern verify --job orders-db
  step "restore → orders_restored"
  cistern restore --job orders-db --set restore_suffix=_restored --yes
  [ "$(sum orders_restored orders)" = "$(cat orders.md5)" ] || fail "checksum mismatch after restore"
  echo "checksum match: $(cat orders.md5)"
  ;;
locked)
  step "backup while another host holds the lock must fail"
  if cistern backup --job orders-db 2>err.txt; then fail "backup ran despite lock"; fi
  grep -q "job is locked: backup by other-host (pid 4242)" err.txt || { cat err.txt; fail "unexpected error"; }
  grep "locked" err.txt | tail -1
  ;;
prune)
  step "prune (retention last: 1)"
  [ "$(count)" = 2 ] || fail "expected 2 snapshots before prune"
  cistern prune --job orders-db
  [ "$(count)" = 1 ] || fail "expected 1 snapshot after prune"
  cistern verify --job orders-db --snapshot all
  ;;
*) fail "unknown phase $1" ;;
esac
