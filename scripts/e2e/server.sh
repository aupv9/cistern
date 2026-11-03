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
for action in auth.login backup.run restore.run restore.request approval.approve approval.reject access.denied run.finish; do
  api victor GET "/audit?action=$action&limit=500"
  expect 200 "audit $action"
  n=$(jq '.entries | length' <<<"$BODY")
  [ "$n" -ge 1 ] || fail "no $action audit entry"
  echo "$action: $n"
done
api victor GET "/audit/export?format=csv"
expect 200 "csv export"
[ "$(find "$DEV_UI_DIR/repo/_admin/audit" -type f | wc -l)" -ge 10 ] || fail "audit objects missing in the repository"

step "no secrets in responses, audit or logs"
if grep -rq "$DEV_UI_PG_PASSWORD" "$DEV_UI_DIR/repo/_admin" "$DEV_UI_DIR/audit.jsonl" "$DEV_UI_DIR/server.log" "$JARS"; then
  fail "database password found"
fi

echo; echo "E2E SERVER: PASS"
