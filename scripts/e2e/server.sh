#!/usr/bin/env bash
set -euo pipefail
step() { echo; echo "==> $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
: "${DEV_UI_BASE:?run through scripts/dev-ui.sh --run}"
BASE=$DEV_UI_BASE
JARS=$(mktemp -d)
trap 'rm -rf "$JARS"' EXIT

login() {
  curl -fsS -c "$JARS/$1" "$BASE/api/v1/auth/dev-login?email=$1@example.com&name=$1&roles=$2" | jq -r .csrf_token > "$JARS/$1.csrf"
}
api() {
  local who=$1 method=$2 path=$3 body=${4:-}
  local args=(-sS -o "$JARS/body" -w '%{http_code}' -b "$JARS/$who" -X "$method")
  [ "$method" != GET ] && args+=(-H "X-Cistern-CSRF: $(cat "$JARS/$who.csrf")")
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' -d "$body")
  CODE=$(curl "${args[@]}" "$BASE/api/v1$path")
  BODY=$(cat "$JARS/body")
}
expect() {
  [ "$CODE" = "$1" ] || fail "$2: HTTP $CODE, want $1: $BODY"
}
wait_run() {
  local who=$1 id=$2 state
  for _ in $(seq 1 120); do
    api "$who" GET "/runs/$id"
    expect 200 "run $id"
    state=$(jq -r .state <<<"$BODY")
    case $state in succeeded|failed) break ;; esac
    sleep 1
  done
  [ "$state" = succeeded ] || fail "run $id: $BODY"
  echo "run $id ($(jq -r .op <<<"$BODY") $(jq -r .job <<<"$BODY")): $state, $(jq '.log_tail | length' <<<"$BODY") log lines"
}
psql_c() { docker exec -e PGPASSWORD="$DEV_UI_PG_PASSWORD" "$DEV_UI_CONTAINER" psql -At -h 127.0.0.1 -U postgres "$@"; }

step "anonymous requests are rejected"
api nobody GET /me
expect 401 "anonymous /me"

step "dev login"
login olivia operator
login rita restorer
login victor viewer
login anna approver
login ralph "restorer,approver"

step "backup from the API"
api olivia POST /jobs/shop-db/backup
expect 202 "backup"
wait_run olivia "$(jq -r .run_id <<<"$BODY")"
api victor POST /jobs/shop-db/backup
expect 403 "viewer backup"
api olivia POST /jobs/shop-db/backup '{}'
api olivia POST /jobs/shop-db/backup
[ "$CODE" = 409 ] || [ "$CODE" = 202 ] || fail "second backup: $CODE $BODY"
sleep 3

step "list snapshots"
api victor GET /jobs/shop-db/snapshots
expect 200 "snapshots"
SNAP=$(jq -r '.snapshots[0].id' <<<"$BODY")
[ -n "$SNAP" ] && [ "$SNAP" != null ] || fail "no snapshot: $BODY"
echo "newest snapshot $SNAP of $(jq '.snapshots | length' <<<"$BODY")"
api victor GET "/jobs/shop-db/snapshots/$SNAP"
expect 200 "manifest"

step "restore drill from the API"
login dora restorer
login ada admin
api olivia POST /jobs/shop-db/drill '{}'
expect 403 "operator drill"
api dora POST /jobs/legacy-db/drill '{}'
expect 422 "drill without a drill section"
api dora POST /jobs/shop-db/drill "{\"snapshot\":\"$SNAP\"}"
expect 202 "drill"
DRILL=$(jq -r .run_id <<<"$BODY")
wait_run dora "$DRILL"
[ "$(jq -r '.drill.state' <<<"$BODY")" = succeeded ] || fail "drill result: $BODY"
[ "$(jq -r '[.drill.checks[] | select(.status == "passed")] | length' <<<"$BODY")" = 5 ] || fail "drill checks: $(jq -c .drill.checks <<<"$BODY")"
[ "$(jq -r '.drill.cleaned_up' <<<"$BODY")" = true ] || fail "drill not cleaned up"
[ "$(jq -r '.drill.total_rows' <<<"$BODY")" = "$(psql_c -d shop -c 'SELECT count(*) FROM orders')" ] || fail "drill row count: $(jq -c .drill.tables <<<"$BODY")"
[ "$(psql_c -c "SELECT count(*) FROM pg_database WHERE datname = 'shop_drill'")" = 0 ] || fail "shop_drill left behind"
jq -r '.drill.checks[] | "  \(.status)\t\(.name)\t\(.actual // "")"' <<<"$BODY"
api victor GET /jobs/shop-db/drills
expect 200 "drills"
[ "$(jq -r '.drills[0].run_id' <<<"$BODY")" = "$DRILL" ] || fail "drill history: $BODY"

step "a drill never touches a database without its marker"
psql_c -c "CREATE DATABASE shop_drill" >/dev/null
api dora POST /jobs/shop-db/drill '{}'
expect 202 "drill over a foreign database"
id=$(jq -r .run_id <<<"$BODY")
for _ in $(seq 1 60); do
  api dora GET "/runs/$id"
  case $(jq -r .state <<<"$BODY") in succeeded|failed) break ;; esac
  sleep 1
done
[ "$(jq -r .state <<<"$BODY")" = failed ] || fail "drill over a foreign database: $BODY"
jq -r .error <<<"$BODY" | grep -q "left untouched" || fail "unexpected drill error: $BODY"
[ "$(psql_c -c "SELECT count(*) FROM pg_database WHERE datname = 'shop_drill'")" = 1 ] || fail "foreign shop_drill was dropped"
psql_c -c "DROP DATABASE shop_drill" >/dev/null
for _ in $(seq 1 40); do
  api victor GET /overview
  [ "$(jq -r '.jobs[] | select(.name == "shop-db") | .health' <<<"$BODY")" = drill_failing ] && break
  sleep 1
done
[ "$(jq -r '.jobs[] | select(.name == "shop-db") | .health' <<<"$BODY")" = drill_failing ] || fail "shop-db health: $(jq -c '.jobs[] | select(.name == "shop-db")' <<<"$BODY")"
jq -r '.alerts[] | select(.kind == "drill_failing") | "alert: \(.message)"' <<<"$BODY"
api dora POST /jobs/shop-db/drill '{}'
expect 202 "drill again"
wait_run dora "$(jq -r .run_id <<<"$BODY")"

step "prune preview and the prune switch"
api victor POST /jobs/shop-db/prune/preview
expect 403 "viewer prune preview"
api olivia POST /jobs/shop-db/prune/preview
expect 200 "prune preview"
jq -c '{kept, removed, freed_bytes, locked, run_records_removed}' <<<"$BODY"
[ "$(jq '.items | length' <<<"$BODY")" -ge 1 ] || fail "prune preview items: $BODY"
api olivia POST /jobs/shop-db/prune '{"reason":"e2e","confirm_job":"shop-db"}'
expect 403 "operator prune"
api ada POST /jobs/shop-db/prune '{"reason":"e2e","confirm_job":"shop-db"}'
expect 403 "prune while server.prune.enabled is false"
jq -r .error.message <<<"$BODY" | grep -q "server.prune.enabled" || fail "prune message: $BODY"

step "configuration view"
api victor GET /config
expect 403 "viewer config"
api olivia GET /config
expect 200 "config"
jq -r .yaml <<<"$BODY" | grep -q 'password: "${PGPASSWORD}"' || fail "config view lost the \${PGPASSWORD} reference"
grep -q "$DEV_UI_PG_PASSWORD" <<<"$BODY" && fail "config view leaked the database password"
echo "config view: $(jq '.yaml | length' <<<"$BODY") bytes, literal secrets: $(jq -c '[.literal_secrets[].path]' <<<"$BODY")"

step "restore into a non-production target"
api rita POST /jobs/shop-db/restore/preview "{\"snapshot\":\"$SNAP\",\"target\":\"scratch\",\"restore_suffix\":\"_e2e\"}"
expect 200 "preview"
jq -r .cli_command <<<"$BODY"
[ "$(jq -r .needs_approval <<<"$BODY")" = false ] || fail "scratch needs approval"
api rita POST /jobs/shop-db/restore "{\"snapshot\":\"$SNAP\",\"target\":\"scratch\",\"restore_suffix\":\"_e2e\",\"reason\":\"e2e\",\"confirm_job\":\"shop-db\"}"
expect 202 "restore scratch"
wait_run rita "$(jq -r .run_id <<<"$BODY")"
[ "$(psql_c -d shop -c 'SELECT count(*) FROM orders')" = "$(psql_c -d shop_e2e -c 'SELECT count(*) FROM orders')" ] || fail "restored row count differs"
echo "shop_e2e has the same rows as shop"
api rita POST /jobs/shop-db/restore "{\"snapshot\":\"$SNAP\",\"target\":\"scratch\",\"reason\":\"e2e\",\"confirm_job\":\"wrong\"}"
expect 422 "wrong confirm_job"

step "production restore needs approval"
api rita POST /jobs/shop-db/restore "{\"snapshot\":\"latest\",\"target\":\"production\",\"restore_suffix\":\"_approved\",\"reason\":\"e2e production restore\",\"confirm_job\":\"shop-db\"}"
expect 202 "production restore request"
[ "$(jq -r .needs_approval <<<"$BODY")" = true ] || fail "production restore did not need approval: $BODY"
AP=$(jq -r .approval_id <<<"$BODY")
api rita POST "/approvals/$AP/approve" '{}'
expect 403 "restorer approves"
api ralph POST /jobs/shop-db/restore "{\"snapshot\":\"latest\",\"target\":\"production\",\"reason\":\"self approval attempt\",\"confirm_job\":\"shop-db\"}"
expect 202 "second request"
OWN=$(jq -r .approval_id <<<"$BODY")
api ralph POST "/approvals/$OWN/approve" '{}'
expect 403 "self approval"
echo "self-approval forbidden"
api anna POST "/approvals/$OWN/reject" '{"note":"duplicate request"}'
expect 200 "reject"
api anna POST "/approvals/$AP/approve" '{"note":"approved in e2e"}'
expect 200 "approve"
[ "$(jq -r .state <<<"$BODY")" = approved ] || fail "approval state: $BODY"
wait_run anna "$(jq -r .run_id <<<"$BODY")"
psql_c -d shop_approved -c 'SELECT 1' >/dev/null || fail "shop_approved not restored"
api anna POST "/approvals/$AP/approve" '{}'
expect 409 "second decision"

step "audit entries"
for _ in $(seq 1 20); do
  api victor GET "/audit?action=run.finish&limit=500"
  [ "$(jq '.entries | length' <<<"$BODY")" -ge 3 ] && break
  sleep 1
done
for action in auth.login backup.run drill.run config.view restore.run restore.request approval.approve approval.reject access.denied run.finish; do
  api victor GET "/audit?action=$action&limit=500"
  expect 200 "audit $action"
  n=$(jq '.entries | length' <<<"$BODY")
  [ "$n" -ge 1 ] || fail "no $action audit entry"
  echo "$action: $n"
done
api victor GET "/audit/export?format=csv"
expect 200 "csv export"
[ "$(find "$DEV_UI_DIR/repo/_admin/audit" -type f | wc -l)" -ge 10 ] || fail "audit objects missing in the repository"

step "the internal API is only on the API port"
: "${DEV_UI_METRICS_URL:?}" "${DEV_UI_DAEMON_API_URL:?}"
for path in /internal/v1/runs /internal/v1/runs/20260101T000000Z-abcdef01; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "$DEV_UI_METRICS_URL$path")
  [ "$code" = 404 ] || fail "$path on the metrics port: HTTP $code, want 404"
done
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{"op":"backup","job":"shop-db"}' "$DEV_UI_METRICS_URL/internal/v1/runs")
[ "$code" = 404 ] || fail "POST /internal/v1/runs on the metrics port: HTTP $code, want 404"
curl -fs "$DEV_UI_METRICS_URL/metrics" | grep -q '^cistern_' || fail "metrics missing on the metrics port"
code=$(curl -s -o /dev/null -w '%{http_code}' "$DEV_UI_DAEMON_API_URL/internal/v1/runs")
[ "$code" = 401 ] || fail "/internal/v1/runs without a token on the API port: HTTP $code, want 401"
echo "metrics port: /internal 404; API port: token required"

restart_server() {
  local old
  old=$(cat "$DEV_UI_DIR/server.pid")
  kill "$old"
  for _ in $(seq 1 100); do
    [ "$(cat "$DEV_UI_DIR/server.pid")" != "$old" ] && curl -fs -o /dev/null "$BASE/healthz" && return 0
    sleep 0.2
  done
  fail "the server did not come back after a restart"
}

step "a logged-out session stays revoked after a server restart"
login lena viewer
cp "$JARS/lena" "$JARS/lena.saved"
api lena GET /me
expect 200 "me before logout"
api lena POST /auth/logout
expect 204 "logout"
cp "$JARS/lena.saved" "$JARS/lena"
api lena GET /me
expect 401 "logged-out session"
ls "$DEV_UI_DIR/repo/_admin/sessions/revoked/"*.json >/dev/null 2>&1 || fail "no revocation object in the repository"
echo "WRITE_PER_MINUTE=3" > "$DEV_UI_DIR/server.env"
restart_server
cp "$JARS/lena.saved" "$JARS/lena"
api lena GET /me
expect 401 "logged-out session after a restart"
echo "revoked session rejected after the restart"

step "write rate limit"
login wanda restorer
for i in 1 2 3; do
  api wanda POST /jobs/shop-db/restore/preview '{"snapshot":"latest","target":"scratch"}'
  expect 200 "preview $i"
done
HDRS=$JARS/headers
CODE=$(curl -sS -o "$JARS/body" -D "$HDRS" -w '%{http_code}' -b "$JARS/wanda" -X POST \
  -H "X-Cistern-CSRF: $(cat "$JARS/wanda.csrf")" -H 'Content-Type: application/json' \
  -d '{"snapshot":"latest","target":"scratch"}' "$BASE/api/v1/jobs/shop-db/restore/preview")
BODY=$(cat "$JARS/body")
expect 429 "fourth write"
[ "$(jq -r .error.code <<<"$BODY")" = rate_limited ] || fail "429 body: $BODY"
RETRY=$(tr -d '\r' < "$HDRS" | awk -F': ' 'tolower($1) == "retry-after" { print $2 }')
[ -n "$RETRY" ] && [ "$RETRY" -ge 1 ] || fail "no Retry-After header"
echo "429 rate_limited, Retry-After: $RETRY s"
api rita POST /jobs/shop-db/restore/preview '{"snapshot":"latest","target":"scratch"}'
expect 200 "another user is not limited"
: > "$DEV_UI_DIR/server.env"
restart_server

step "no secrets in responses, audit or logs"
if grep -rq "$DEV_UI_PG_PASSWORD" "$DEV_UI_DIR/repo/_admin" "$DEV_UI_DIR/audit.jsonl" "$DEV_UI_DIR/server.log" "$JARS"; then
  fail "database password found"
fi

echo; echo "E2E SERVER: PASS"
