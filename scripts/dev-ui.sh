#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
PG_IMAGE=${PG_IMAGE:-postgres:16.15@sha256:5db6dac2ef5dcff512f4f00a009c3c332b9f2f28f24277175bf3e4a3d02ef83a}
PORT=${DEV_UI_PORT:-8480}
DAEMON_PORT=${DEV_UI_DAEMON_PORT:-8481}
DAEMON_API_PORT=${DEV_UI_DAEMON_API_PORT:-$((DAEMON_PORT + 1))}
DIR=${DEV_UI_DIR:-$ROOT/.dev-ui}
SEED=1
RUN=""
while [ $# -gt 0 ]; do
  case $1 in
    --no-seed) SEED=0 ;;
    --run) RUN=$2; shift ;;
    *) echo "usage: $0 [--no-seed] [--run COMMAND]" >&2; exit 2 ;;
  esac
  shift
done

NAME=cistern-dev-ui-$$
BASE=http://127.0.0.1:$PORT
SUPERVISOR_PID=""
PG_PASSWORD=dev-ui-postgres-password
SESSION_KEY=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
DAEMON_TOKEN=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')

cleanup() {
  if [ -n "$SUPERVISOR_PID" ]; then
    touch "$DIR/server.stop"
    kill "$SUPERVISOR_PID" 2>/dev/null || true
    wait "$SUPERVISOR_PID" 2>/dev/null || true
    if [ -f "$DIR/server.pid" ]; then
      pid=$(cat "$DIR/server.pid")
      kill "$pid" 2>/dev/null || true
      for _ in $(seq 1 100); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
    fi
  fi
  docker rm -f -v "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT TERM
step() { echo; echo "==> $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
for tool in docker go curl jq; do command -v "$tool" >/dev/null || fail "$tool is required"; done

step "prepare $DIR"
rm -rf "$DIR"
mkdir -p "$DIR/bin" "$DIR/repo"
TAGS=""
[ -f "$ROOT/web/dist/client/_shell.html" ] && TAGS="-tags ui"
(cd "$ROOT" && go build $TAGS -o "$DIR/bin/cistern" ./cmd/cistern)
ARCH=$(docker version -f '{{.Server.Arch}}')
(cd "$ROOT" && GOOS=linux GOARCH=$ARCH CGO_ENABLED=0 go build -o "$DIR/bin/cistern-linux" ./cmd/cistern)
"$DIR/bin/cistern" keygen --out "$DIR/identity.txt" > "$DIR/recipient.txt" 2>/dev/null

cat > "$DIR/cistern.yaml" <<'YAML'
repositories:
  local: { type: local, path: "${DEV_DIR}/repo" }
encryption:
  recipients_file: recipient.txt
  identity_file: identity.txt
scheduler:
  timezone: Asia/Ho_Chi_Minh
defaults:
  repository: local
  timeout: 30m
  retention: { last: 5, daily: 7 }
  prune_after_backup: true
daemon:
  api_token: ${DAEMON_TOKEN:-}
  api_listen: ":9465"
server:
  name: development
  session_key: ${SESSION_KEY:-}
  daemon: { url: "http://127.0.0.1:${DAEMON_API_PORT}", token: "${DAEMON_TOKEN:-}" }
  rate_limit: { login_per_minute: "${LOGIN_PER_MINUTE:-120}", write_per_minute: "${WRITE_PER_MINUTE:-30}" }
jobs:
  - name: shop-db
    driver: postgres
    schedule: "*/30 * * * *"
    verify_schedule: "15 * * * *"
    params: { host: 127.0.0.1, user: postgres, password: "${PGPASSWORD}", databases: [shop] }
    restore_targets:
      - { name: scratch, description: "Same server, database shop_<suffix>, overwritten", production: false, set: { clean: "true" } }
      - { name: production, description: "The live shop database server", production: true }
  - name: crm-db
    driver: postgres
    schedule: "0 */6 * * *"
    verify_schedule: "@weekly"
    params: { host: 127.0.0.1, user: postgres, password: "${PGPASSWORD}", databases: [crm, crm_audit] }
  - name: legacy-db
    driver: postgres
    schedule: "0 3 * * *"
    params: { host: 127.0.0.1, user: postgres, password: "${PGPASSWORD}", databases: [legacy] }
  - name: analytics
    driver: postgres
    params: { host: 127.0.0.1, user: postgres, password: "${PGPASSWORD}", databases: [analytics] }
YAML

step "start $PG_IMAGE with the daemon"
docker run -d --name "$NAME" -e POSTGRES_PASSWORD="$PG_PASSWORD" -v "$DIR:/dev-ui" \
  -p "127.0.0.1:$DAEMON_PORT:9464" -p "127.0.0.1:$DAEMON_API_PORT:9465" "$PG_IMAGE" >/dev/null
for _ in $(seq 1 60); do
  docker exec "$NAME" pg_isready -q -h 127.0.0.1 -U postgres && break
  sleep 1
done
docker exec "$NAME" pg_isready -q -h 127.0.0.1 -U postgres || fail "postgres did not start"
docker exec "$NAME" sh -c 'cp /dev-ui/bin/cistern-linux /usr/local/bin/cistern && chmod 755 /usr/local/bin/cistern'
docker exec -e PGPASSWORD="$PG_PASSWORD" "$NAME" psql -q -h 127.0.0.1 -U postgres -v ON_ERROR_STOP=1 \
  -c "CREATE DATABASE shop" -c "CREATE DATABASE crm" -c "CREATE DATABASE crm_audit" -c "CREATE DATABASE analytics"
docker exec -e PGPASSWORD="$PG_PASSWORD" "$NAME" psql -q -h 127.0.0.1 -U postgres -d shop -v ON_ERROR_STOP=1 \
  -c "CREATE TABLE orders AS SELECT g AS id, md5(g::text) AS note, g * 1.5 AS amount FROM generate_series(1, 50000) g"
docker exec -e PGPASSWORD="$PG_PASSWORD" "$NAME" psql -q -h 127.0.0.1 -U postgres -d crm -v ON_ERROR_STOP=1 \
  -c "CREATE TABLE contacts AS SELECT g AS id, 'c' || g || '@example.com' AS email FROM generate_series(1, 5000) g"

in_container() {
  docker exec -u "$(id -u):$(id -g)" -e HOME=/tmp -e CISTERN_TMPDIR=/tmp -e DEV_DIR=/dev-ui -e PGPASSWORD="$PG_PASSWORD" \
    -e DAEMON_TOKEN="$DAEMON_TOKEN" -e CISTERN_CONFIG=/dev-ui/cistern.yaml "$NAME" "$@"
}

if [ "$SEED" = 1 ]; then
  step "history: synthetic run records for the last 30 days"
  now=$(date -u +%s)
  record() {
    local job=$1 op=$2 status=$3 ago=$4 dur=$5 err=${6:-}
    local t=$((now - ago)) id ts fin
    ts=$(date -u -r "$t" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d "@$t" +%Y-%m-%dT%H:%M:%SZ)
    fin=$(date -u -r "$((t + dur / 1000))" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d "@$((t + dur / 1000))" +%Y-%m-%dT%H:%M:%SZ)
    id=$(date -u -r "$t" +%Y%m%dT%H%M%SZ 2>/dev/null || date -u -d "@$t" +%Y%m%dT%H%M%SZ)-$(head -c 4 /dev/urandom | od -An -tx1 | tr -d ' \n')
    mkdir -p "$DIR/repo/_admin/runs/$job"
    jq -n --arg id "$id" --arg job "$job" --arg op "$op" --arg st "$status" --arg ts "$ts" --arg fin "$fin" \
      --argjson dur "$dur" --arg err "$err" \
      '{id: $id, job: $job, op: $op, status: $st, trigger: "schedule", requested_by: {id: "scheduler", name: "Scheduler"},
        started_at: $ts, finished_at: $fin, duration_ms: $dur} + (if $err == "" then {} else {error: $err} end)' \
      > "$DIR/repo/_admin/runs/$job/$id.json"
  }
  for d in $(seq 29 -1 1); do
    if [ "$d" = 12 ] || [ "$d" = 5 ]; then
      record shop-db backup failed $((d * 86400)) 4100 "backup shop-db: pg_dump: error: connection to server failed: timeout expired"
    else
      record shop-db backup succeeded $((d * 86400)) $((38000 + d * 700))
    fi
    [ $((d % 7)) = 0 ] && record shop-db verify succeeded $((d * 86400 - 3600)) 9000
    [ $((d % 3)) = 0 ] && record crm-db backup succeeded $((d * 86400)) 12000
  done
  record crm-db verify succeeded $((10 * 86400)) 5000
  record legacy-db backup succeeded $((3 * 86400)) 21000

  step "history: real backups, a failing job and a verify (trigger cli)"
  in_container cistern backup --job shop-db
  in_container cistern backup --job shop-db
  in_container cistern backup --job crm-db
  in_container cistern backup --job legacy-db && fail "legacy-db should fail" || true
  in_container cistern verify --job shop-db >/dev/null
fi

step "start cistern daemon (in the container): metrics on 127.0.0.1:$DAEMON_PORT, API on 127.0.0.1:$DAEMON_API_PORT"
docker exec -d -u "$(id -u):$(id -g)" -e HOME=/tmp -e CISTERN_TMPDIR=/tmp -e DEV_DIR=/dev-ui -e PGPASSWORD="$PG_PASSWORD" \
  -e DAEMON_TOKEN="$DAEMON_TOKEN" "$NAME" sh -c 'exec cistern daemon -c /dev-ui/cistern.yaml --listen :9464 --grace 10s > /dev-ui/daemon.log 2>&1'
for _ in $(seq 1 50); do curl -fs "http://127.0.0.1:$DAEMON_PORT/healthz" >/dev/null && break; sleep 0.2; done
curl -fs "http://127.0.0.1:$DAEMON_PORT/healthz" >/dev/null || { cat "$DIR/daemon.log"; fail "daemon did not start"; }
for _ in $(seq 1 50); do curl -fs "http://127.0.0.1:$DAEMON_API_PORT/healthz" >/dev/null && break; sleep 0.2; done
curl -fs "http://127.0.0.1:$DAEMON_API_PORT/healthz" >/dev/null || { cat "$DIR/daemon.log"; fail "daemon API did not start"; }

step "start cistern server on $BASE"
: > "$DIR/server.env"
: > "$DIR/audit.jsonl"
: > "$DIR/server.log"
rm -f "$DIR/server.stop" "$DIR/server.pid"
supervise() {
  while [ ! -f "$DIR/server.stop" ]; do
    (
      set -a
      DEV_DIR=$DIR SESSION_KEY=$SESSION_KEY DAEMON_TOKEN=$DAEMON_TOKEN DAEMON_API_PORT=$DAEMON_API_PORT
      LOGIN_PER_MINUTE=${DEV_UI_LOGIN_PER_MINUTE:-120} WRITE_PER_MINUTE=${DEV_UI_WRITE_PER_MINUTE:-30}
      . "$DIR/server.env"
      set +a
      exec "$DIR/bin/cistern" server -c "$DIR/cistern.yaml" --listen "127.0.0.1:$PORT" --insecure-dev-login
    ) >> "$DIR/audit.jsonl" 2>> "$DIR/server.log" &
    echo $! > "$DIR/server.pid"
    wait $! || true
    sleep 0.2
  done
}
supervise &
SUPERVISOR_PID=$!
for _ in $(seq 1 50); do curl -fs -o /dev/null "$BASE/" && break; sleep 0.2; done
curl -fs -o /dev/null "$BASE/" || { cat "$DIR/server.log"; fail "server did not start"; }

if [ "$SEED" = 1 ]; then
  step "history: a backup from the UI API and a pending production restore"
  JAR=$DIR/seed.cookies
  CSRF=$(curl -fs -c "$JAR" "$BASE/api/v1/auth/dev-login?email=olivia@example.com&name=Olivia%20Operator&roles=operator" | jq -r .csrf_token)
  curl -fs -b "$JAR" -H "X-Cistern-CSRF: $CSRF" -X POST "$BASE/api/v1/jobs/crm-db/backup" >/dev/null
  CSRF=$(curl -fs -c "$JAR" "$BASE/api/v1/auth/dev-login?email=rita@example.com&name=Rita%20Restorer&roles=restorer" | jq -r .csrf_token)
  curl -fs -b "$JAR" -H "X-Cistern-CSRF: $CSRF" -H 'Content-Type: application/json' \
    -d '{"snapshot":"latest","target":"production","reason":"Customer reported missing orders after the 02:00 migration","confirm_job":"shop-db"}' \
    "$BASE/api/v1/jobs/shop-db/restore" >/dev/null
  rm -f "$JAR"
fi

cat <<EOF

Cistern dev backend is running.

  UI and API      $BASE  (API under $BASE/api/v1)
  Dev login       $BASE/api/v1/auth/dev-login?email=you@example.com&name=You&roles=admin&return_to=/
                  roles: any of viewer,operator,restorer,approver,admin (comma separated)
  Daemon          http://127.0.0.1:$DAEMON_PORT/status (metrics port; internal API on 127.0.0.1:$DAEMON_API_PORT)
  Files           $DIR (repo/, cistern.yaml, server.log, daemon.log, audit.jsonl)
  Restart server  kill \$(cat $DIR/server.pid)   (restarted with the variables in $DIR/server.env)
  Postgres        docker exec -it $NAME psql -U postgres shop

Press Ctrl-C to stop.
EOF

export DEV_UI_BASE=$BASE DEV_UI_CONTAINER=$NAME DEV_UI_DIR=$DIR DEV_UI_PG_PASSWORD=$PG_PASSWORD
export DEV_UI_METRICS_URL=http://127.0.0.1:$DAEMON_PORT DEV_UI_DAEMON_API_URL=http://127.0.0.1:$DAEMON_API_PORT
if [ -n "$RUN" ]; then
  set +e
  bash -c "$RUN"
  status=$?
  [ $status = 0 ] || { echo "--- server.log"; tail -50 "$DIR/server.log"; echo "--- daemon.log"; tail -50 "$DIR/daemon.log"; }
  exit $status
fi
wait "$SUPERVISOR_PID"
