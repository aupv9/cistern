# Chạy bên trong container postgres (xem scripts/e2e-local.sh).
set -euo pipefail
step() { echo; echo "==> $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

export PGHOST=127.0.0.1 PGUSER=postgres PGPASSWORD=e2e-secret
export CISTERN_CONFIG=/e2e/cistern.yaml
mkdir -p /e2e/metrics && cd /e2e

step "keygen + config"
cistern keygen --out identity.txt > recipient.txt
cat > cistern.yaml <<'YAML'
repositories:
  local: { type: local, path: /e2e/repo }
encryption:
  recipients_file: recipient.txt
  identity_file: identity.txt
metrics:
  textfile_dir: /e2e/metrics
notify:
  webhooks:
    # Cổng đóng: webhook lỗi chỉ được ghi log, không làm job thất bại.
    - { url: "http://127.0.0.1:9/hook", format: slack, on: [failure] }
jobs:
  - name: orders-db
    driver: postgres
    timeout: 10m
    retention: { last: 2 }
    params:
      host: ${PGHOST}
      user: ${PGUSER}
      password: ${PGPASSWORD}
      databases: [orders, users]
YAML

step "seed"
psql -q -v ON_ERROR_STOP=1 -c "CREATE DATABASE orders" -c "CREATE DATABASE users" -c "CREATE ROLE app_reader LOGIN"
psql -q -v ON_ERROR_STOP=1 -d orders <<'SQL'
CREATE TABLE orders (id bigserial PRIMARY KEY, customer text NOT NULL, amount numeric(12,2), note text);
INSERT INTO orders (customer, amount, note)
  SELECT 'c' || g, g * 1.5, md5(g::text) FROM generate_series(1, 200000) g;
CREATE INDEX ON orders (customer);
CREATE VIEW big_orders AS SELECT * FROM orders WHERE amount > 1000;
CREATE FUNCTION total() RETURNS numeric LANGUAGE sql AS 'SELECT sum(amount) FROM orders';
GRANT SELECT ON orders TO app_reader;
SQL
psql -q -v ON_ERROR_STOP=1 -d users -c "CREATE TABLE u (id int PRIMARY KEY, email text)" \
  -c "INSERT INTO u SELECT g, 'u' || g || '@example.com' FROM generate_series(1, 5000) g"

sum() { psql -At -d "$1" -c "SELECT md5(string_agg(t::text, ',' ORDER BY id)) FROM $2 t"; }
ORDERS=$(sum orders orders); USERS=$(sum users u); TOTAL=$(psql -At -d orders -c "SELECT total()")

step "check"
cistern check --job orders-db

step "backup"
cistern backup --job orders-db
cistern list --job orders-db
SNAP_DIR=$(ls -d /e2e/repo/orders-db/*/ | head -1)
find "$SNAP_DIR" -type f | sort
grep -rq e2e-secret /e2e/repo && fail "password found in repository"
grep -q '"pg_dump_version"' "$SNAP_DIR/manifest.json" || fail "manifest lacks pg_dump_version"

step "verify"
cistern verify --job orders-db

step "restore → *_restored"
cistern restore --job orders-db --set restore_suffix=_restored --yes
[ "$(sum orders_restored orders)" = "$ORDERS" ] || fail "orders checksum mismatch"
[ "$(sum users_restored u)" = "$USERS" ] || fail "users checksum mismatch"
[ "$(psql -At -d orders_restored -c 'SELECT total()')" = "$TOTAL" ] || fail "function total() differs"
[ "$(psql -At -d orders_restored -c 'SELECT count(*) FROM big_orders')" -gt 0 ] || fail "view missing"
echo "checksums match: orders=$ORDERS users=$USERS"

step "restore again without clean is refused"
if cistern restore --job orders-db --set restore_suffix=_restored --yes 2>err.txt; then fail "overwrite allowed"; fi
grep -q "already exists" err.txt || { cat err.txt; fail "unexpected error"; }
grep -q "webhook failed" err.txt || { cat err.txt; fail "webhook failure not logged"; }
cat err.txt

step "restore one database with clean=true"
psql -q -d orders_restored -c "DELETE FROM orders WHERE id <= 10"
cistern restore --job orders-db --set databases=orders --set restore_suffix=_restored --set clean=true --yes
[ "$(sum orders_restored orders)" = "$ORDERS" ] || fail "clean restore mismatch"

if [ -z "${SKIP_DR:-}" ]; then
  step "manual DR without cistern: age -d | gunzip | pg_restore"
  (export DEBIAN_FRONTEND=noninteractive; apt-get update -qq && apt-get install -y -qq age >/dev/null 2>&1) || fail "cannot install age (set SKIP_DR=1 to skip)"
  age --version
  createdb orders_dr
  age -d -i identity.txt "$SNAP_DIR/artifacts/db/orders.dump.gz.age" | gunzip | pg_restore --exit-on-error -d orders_dr
  [ "$(sum orders_dr orders)" = "$ORDERS" ] || fail "manual DR mismatch"
  age -d -i identity.txt "$SNAP_DIR/artifacts/globals.sql.gz.age" | gunzip | grep -q "CREATE ROLE app_reader" || fail "globals lacks app_reader"
  echo "manual recovery OK"
fi

step "corrupt one byte → verify and restore fail"
F="$SNAP_DIR/artifacts/db/users.dump.gz.age"
SIZE=$(stat -c %s "$F")
printf '\x00' | dd of="$F" bs=1 seek=$((SIZE / 2)) conv=notrunc status=none
if cistern verify --job orders-db; then fail "verify missed corruption"; fi
if cistern restore --job orders-db --set restore_suffix=_bad --set databases=users --yes; then fail "restore of corrupted artifact succeeded"; fi
[ "$(psql -At -c "SELECT count(*) FROM pg_database WHERE datname = 'users_bad'")" = 0 ] || fail "partial database users_bad left behind"

step "metrics textfile"
PROM=/e2e/metrics/cistern_orders-db.prom
cat "$PROM"
grep -q 'cistern_backup_last_success_timestamp_seconds{cistern_job="orders-db"}' "$PROM" || fail "no last success in textfile"
grep -q 'cistern_verify_last_failure_timestamp_seconds{cistern_job="orders-db"}' "$PROM" || fail "verify failure not recorded"

step "prune (retention last: 2)"
cistern backup --job orders-db
cistern backup --job orders-db
[ "$(cistern list --job orders-db --json | grep -c '"id"')" = 3 ] || fail "expected 3 snapshots before prune"
cistern prune --job orders-db --dry-run | tee prune.txt
grep -q "would remove 1" prune.txt || fail "dry run summary"
[ "$(cistern list --job orders-db --json | grep -c '"id"')" = 3 ] || fail "dry run deleted snapshots"
cistern prune --job orders-db
cistern list --job orders-db
[ "$(cistern list --job orders-db --json | grep -c '"id"')" = 2 ] || fail "expected 2 snapshots after prune"
[ -d "$SNAP_DIR" ] && fail "oldest (corrupted) snapshot still present"
grep -q 'cistern_snapshots{cistern_job="orders-db"} 2' "$PROM" || fail "cistern_snapshots not updated"
grep -q 'cistern_prune_removed_snapshots_total{cistern_job="orders-db"} 1' "$PROM" || fail "prune counter missing"
cistern verify --job orders-db --snapshot all

step "daemon: scheduled backup and verify, /metrics, /status, SIGTERM"
BEFORE=$(cistern list --job orders-db --json | grep -c '"id"')
sed 's|^    driver: postgres$|    driver: postgres\n    schedule: "@every 3s"\n    verify_schedule: "@every 4s"|' cistern.yaml > cistern-daemon.yaml
grep -q 'schedule: "@every 3s"' cistern-daemon.yaml || fail "could not add schedule to config"
cistern daemon -c cistern-daemon.yaml --listen 127.0.0.1:19464 2>daemon.log &
DPID=$!
sleep 9
http_get() {
  exec 3<>/dev/tcp/127.0.0.1/19464
  printf 'GET %s HTTP/1.0\r\nHost: localhost\r\n\r\n' "$1" >&3
  cat <&3
  exec 3<&-
}
METRICS=$(http_get /metrics)
STATUS=$(http_get /status)
kill -TERM $DPID
wait $DPID || { cat daemon.log; fail "daemon did not exit cleanly"; }
AFTER=$(cistern list --job orders-db --json | grep -c '"id"')
echo "snapshots: $BEFORE before daemon, $AFTER after ~9s of @every 3s"
[ "$AFTER" -ge $((BEFORE + 2)) ] || { cat daemon.log; fail "scheduled backups did not run"; }
grep -q 'msg=scheduled job=orders-db op=verify' daemon.log || fail "verify task not scheduled"
grep -q 'msg="shutting down"' daemon.log || fail "no graceful shutdown in log"
echo "$METRICS" | grep -E '^cistern_(backup_last_success|verify_last_success|snapshots)'
echo "$METRICS" | grep -q 'cistern_verify_last_success_timestamp_seconds{cistern_job="orders-db"}' || fail "scheduled verify did not run"
echo "$STATUS" | grep -q '"op": "backup"' || { echo "$STATUS"; fail "status lacks scheduled tasks"; }
echo "$STATUS" | grep -q '"last_started"' || { echo "$STATUS"; fail "status lacks last_started"; }
echo "$STATUS" | sed -n '/"tasks"/,$p'

echo; echo "E2E POSTGRES: PASS"
