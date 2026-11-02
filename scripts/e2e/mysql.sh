# Chạy bên trong container MySQL hoặc MariaDB (xem scripts/e2e-local.sh). CLI=mysql | mariadb.
set -euo pipefail
step() { echo; echo "==> $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
CLI=${CLI:-mysql}
export MYSQL_PWD=e2e-secret DB_PASSWORD=e2e-secret   # chỉ cho script kiểm thử; cistern dùng file option 0600
export CISTERN_CONFIG=/e2e/cistern.yaml
sqlc() { "$CLI" -h127.0.0.1 -uroot --default-character-set=utf8mb4 "$@"; }
q() { sqlc -N -B -e "$1"; }
mkdir -p /e2e && cd /e2e

step "keygen + config"
cistern keygen --out identity.txt > recipient.txt
cat > cistern.yaml <<'YAML'
repositories:
  local: { type: local, path: /e2e/repo }
encryption:
  recipients_file: recipient.txt
  identity_file: identity.txt
jobs:
  - name: shop
    driver: mysql
    timeout: 10m
    params:
      host: 127.0.0.1
      port: 3306
      user: root
      password: ${DB_PASSWORD}
      databases: [orders, users]
YAML

step "seed: 100k rows (utf8mb4, BLOB), view, function, procedure, trigger, event"
sqlc <<'SQL'
CREATE DATABASE orders CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
USE orders;
CREATE TABLE orders (id INT PRIMARY KEY, customer VARCHAR(50) NOT NULL, amount DECIMAL(12,2), note VARCHAR(100), payload BLOB) ENGINE=InnoDB;
CREATE DATABASE seedtmp;
CREATE TABLE seedtmp.d (i INT);
INSERT INTO seedtmp.d VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9);
INSERT INTO orders
  SELECT n, CONCAT('khách ', n), n * 1.5, CONCAT('ghi chú đơn hàng ', n, ' 🚀'), UNHEX(MD5(n))
  FROM (SELECT a.i + 10*b.i + 100*c.i + 1000*e.i + 10000*f.i + 1 AS n
        FROM seedtmp.d a, seedtmp.d b, seedtmp.d c, seedtmp.d e, seedtmp.d f) s;
DROP DATABASE seedtmp;
CREATE TABLE audit (id INT AUTO_INCREMENT PRIMARY KEY, order_id INT);
CREATE VIEW big_orders AS SELECT id, amount FROM orders WHERE amount > 100000;
CREATE FUNCTION total_amount() RETURNS DECIMAL(20,2) READS SQL DATA RETURN (SELECT SUM(amount) FROM orders);
CREATE PROCEDURE add_order(c VARCHAR(50)) MODIFIES SQL DATA INSERT INTO orders (id, customer, amount) VALUES ((SELECT MAX(id) + 1 FROM orders o2), c, 1);
CREATE TRIGGER orders_ai AFTER INSERT ON orders FOR EACH ROW INSERT INTO audit (order_id) VALUES (NEW.id);
CREATE EVENT purge_audit ON SCHEDULE EVERY 1 DAY DO DELETE FROM audit WHERE id < 0;
CREATE DATABASE users;
CREATE TABLE users.u (id INT PRIMARY KEY, email VARCHAR(100));
INSERT INTO users.u SELECT id, CONCAT('u', id, '@example.com') FROM orders.orders WHERE id <= 1000;
SQL
sum() { q "SELECT CONCAT(COUNT(*), ':', BIT_XOR(CRC32(CONCAT_WS('#', id, customer, amount, note, HEX(payload)))), ':', SUM(amount)) FROM $1.orders"; }
usum() { q "SELECT CONCAT(COUNT(*), ':', BIT_XOR(CRC32(CONCAT_WS('#', id, email)))) FROM $1.u"; }
ORDERS=$(sum orders); USERS=$(usum users); VIEW=$(q "SELECT COUNT(*) FROM orders.big_orders")
echo "orders=$ORDERS users=$USERS big_orders=$VIEW"

step "check"
cistern check --job shop
step "backup"
cistern backup --job shop
cistern list --job shop
SNAP_DIR=$(ls -d /e2e/repo/shop/*/ | head -1)
grep -rq e2e-secret /e2e/repo && fail "password found in repository"
ls -la "${CISTERN_TMPDIR:-/tmp}" | grep -q cistern- && fail "option file directory left behind"
step "verify"
cistern verify --job shop

step "restore → *_restored"
cistern restore --job shop --set restore_suffix=_restored --yes
[ "$(sum orders_restored)" = "$ORDERS" ] || fail "orders checksum mismatch"
[ "$(usum users_restored)" = "$USERS" ] || fail "users checksum mismatch"
[ "$(q "SELECT default_collation_name FROM information_schema.schemata WHERE schema_name='orders_restored'")" = utf8mb4_unicode_ci ] || fail "collation not preserved"
[ "$(q "SELECT note FROM orders_restored.orders WHERE id = 42")" = "ghi chú đơn hàng 42 🚀" ] || fail "utf8mb4 text mangled"
R=$(q "SELECT CONCAT((SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema='orders_restored'), '/',
                     (SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema='orders_restored'), '/',
                     (SELECT COUNT(*) FROM information_schema.events WHERE event_schema='orders_restored'), '/',
                     (SELECT COUNT(*) FROM information_schema.views WHERE table_schema='orders_restored'))")
echo "routines/triggers/events/views in orders_restored: $R"
[ "$R" = "2/1/1/1" ] || fail "expected 2 routines, 1 trigger, 1 event, 1 view"
[ "$(q "SELECT orders_restored.total_amount()")" = "$(q "SELECT orders.total_amount()")" ] || fail "function result differs"
q "CALL orders_restored.add_order('mới')"
[ "$(q "SELECT COUNT(*) FROM orders_restored.audit")" = 1 ] || fail "trigger did not fire in restored database"
# View phải đọc bảng của database đã restore, không phải database gốc.
q "DELETE FROM orders.orders WHERE amount > 100000"
[ "$(q "SELECT COUNT(*) FROM orders_restored.big_orders")" = "$VIEW" ] || fail "restored view still reads the original database"
echo "checksums, collation, utf8mb4, routines, trigger, event and view all match"

step "restore again without clean is refused"
if cistern restore --job shop --set restore_suffix=_restored --yes 2>err.txt; then fail "overwrite allowed"; fi
grep -q "already exists" err.txt || { cat err.txt; fail "unexpected error"; }

if command -v apt-get >/dev/null; then
  step "manual DR without cistern: age -d | gunzip | $CLI"
  (export DEBIAN_FRONTEND=noninteractive; apt-get update -qq && apt-get install -y -qq age >/dev/null 2>&1) || fail "cannot install age"
  q "CREATE DATABASE orders_dr"
  age -d -i identity.txt "$SNAP_DIR/artifacts/db/orders.sql.gz.age" | gunzip | sqlc orders_dr
  [ "$(sum orders_dr)" = "$ORDERS" ] || fail "manual DR mismatch"
  echo "manual recovery OK"
else
  step "manual DR: skipped (no apt-get in this image to install age)"
fi

step "corrupt one byte → verify and restore fail, no partial database left"
F="$SNAP_DIR/artifacts/db/users.sql.gz.age"
SIZE=$(stat -c %s "$F")
printf '\x00' | dd of="$F" bs=1 seek=$((SIZE / 2)) conv=notrunc status=none
if cistern verify --job shop; then fail "verify missed corruption"; fi
if cistern restore --job shop --set restore_suffix=_bad --set databases=users --yes; then fail "restore of corrupted artifact succeeded"; fi
[ "$(q "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name='users_bad'")" = 0 ] || fail "partial database users_bad left behind"

echo; echo "E2E $(echo "$CLI" | tr a-z A-Z): PASS"
