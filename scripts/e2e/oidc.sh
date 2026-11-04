#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
PG_IMAGE=${PG_IMAGE:-postgres:16.15@sha256:5db6dac2ef5dcff512f4f00a009c3c332b9f2f28f24277175bf3e4a3d02ef83a}
KEYCLOAK_IMAGE=${KEYCLOAK_IMAGE:-quay.io/keycloak/keycloak:26.8.0@sha256:27b3230fbabb8c9ff3c66c864cf09dcd8a41aca82e5100679d109ef0eeb05741}
PORT=${OIDC_E2E_PORT:-18443}
LOGIN_LIMIT=12
NAME=cistern-e2e-oidc-$$
TMP_BASE=${CISTERN_E2E_TMP:-$ROOT/.e2e-tmp}
mkdir -p "$TMP_BASE"
DIR=$(mktemp -d "$TMP_BASE/oidc.XXXXXX")
DIR=$(cd "$DIR" && pwd -P)
JARS=$DIR/jars
BASE=https://127.0.0.1:$PORT
SERVER_PID=""
PG_PASSWORD=oidc-e2e-postgres-password
CLIENT_SECRET=oidc-e2e-client-secret-$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')
SESSION_KEY=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
DAEMON_TOKEN=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')

SUB_VERA=00000000-0000-4000-8000-0000000e2e01
SUB_DAN=00000000-0000-4000-8000-0000000e2e02
SUB_ALICE=00000000-0000-4000-8000-0000000e2e03
SUB_ADAM=00000000-0000-4000-8000-0000000e2e04
SUB_NORA=00000000-0000-4000-8000-0000000e2e05

stop_server() {
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  SERVER_PID=""
}
cleanup() {
  stop_server
  docker rm -f -v "$NAME-kc" "$NAME-pg" >/dev/null 2>&1 || true
  rm -rf "$DIR"
}
trap cleanup EXIT
trap 'exit 130' INT TERM
step() { echo; echo "==> $*"; }
fail() {
  echo "FAIL: $*" >&2
  echo "--- server.log" >&2; tail -40 "$DIR/server.log" >&2 2>/dev/null || true
  echo "--- daemon.log" >&2; tail -20 "$DIR/daemon.log" >&2 2>/dev/null || true
  echo "--- keycloak" >&2; docker logs --tail 20 "$NAME-kc" >&2 2>/dev/null || true
  exit 1
}
for tool in docker go curl jq openssl; do command -v "$tool" >/dev/null || fail "$tool is required"; done

step "build cistern (host and linux)"
mkdir -p "$DIR/bin" "$DIR/repo" "$DIR/import" "$JARS"
(cd "$ROOT" && go build -o "$DIR/bin/cistern" ./cmd/cistern)
ARCH=$(docker version -f '{{.Server.Arch}}')
(cd "$ROOT" && GOOS=linux GOARCH=$ARCH CGO_ENABLED=0 go build -o "$DIR/bin/cistern-linux" ./cmd/cistern)
"$DIR/bin/cistern" keygen --out "$DIR/identity.txt" > "$DIR/recipient.txt" 2>/dev/null
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 -subj /CN=127.0.0.1 \
  -addext subjectAltName=IP:127.0.0.1 -keyout "$DIR/tls.key" -out "$DIR/tls.crt" >/dev/null 2>&1
TLS=(--cacert "$DIR/tls.crt")

step "start $KEYCLOAK_IMAGE with realm cistern"
jq --arg cb "$BASE/api/v1/auth/callback" --arg secret "$CLIENT_SECRET" \
  '.clients[0].redirectUris = [$cb] | .clients[0].secret = $secret' \
  "$ROOT/scripts/e2e/oidc/realm.json" > "$DIR/import/cistern-realm.json"
docker create --name "$NAME-kc" -p 127.0.0.1::8080 "$KEYCLOAK_IMAGE" start-dev --import-realm >/dev/null
docker cp "$DIR/import" "$NAME-kc:/opt/keycloak/data/import"
docker start "$NAME-kc" >/dev/null
KC_PORT=$(docker port "$NAME-kc" 8080/tcp | head -1 | sed 's/.*://')
ISSUER=http://127.0.0.1:$KC_PORT/realms/cistern
for _ in $(seq 1 180); do
  curl -fs -o /dev/null "$ISSUER/.well-known/openid-configuration" && break
  sleep 1
done
[ "$(curl -fsS "$ISSUER/.well-known/openid-configuration" | jq -r .issuer)" = "$ISSUER" ] || fail "keycloak did not start"
echo "issuer $ISSUER"

step "start $PG_IMAGE with the daemon (internal API on its own listener)"
docker run -d --name "$NAME-pg" -e POSTGRES_PASSWORD="$PG_PASSWORD" -v "$DIR:/e2e" \
  -p 127.0.0.1::9464 -p 127.0.0.1::9465 "$PG_IMAGE" >/dev/null
for _ in $(seq 1 60); do
  docker exec "$NAME-pg" pg_isready -q -h 127.0.0.1 -U postgres && break
  sleep 1
done
docker exec "$NAME-pg" pg_isready -q -h 127.0.0.1 -U postgres || fail "postgres did not start"
docker exec "$NAME-pg" sh -c 'cp /e2e/bin/cistern-linux /usr/local/bin/cistern && chmod 755 /usr/local/bin/cistern'
psql_c() { docker exec -e PGPASSWORD="$PG_PASSWORD" "$NAME-pg" psql -At -h 127.0.0.1 -U postgres "$@"; }
psql_c -q -v ON_ERROR_STOP=1 -c "CREATE DATABASE shop" >/dev/null
psql_c -q -d shop -v ON_ERROR_STOP=1 -c "CREATE TABLE orders AS SELECT g AS id, md5(g::text) AS note FROM generate_series(1, 20000) g" >/dev/null
METRICS_PORT=$(docker port "$NAME-pg" 9464/tcp | head -1 | sed 's/.*://')
API_PORT=$(docker port "$NAME-pg" 9465/tcp | head -1 | sed 's/.*://')

cat > "$DIR/cistern.yaml" <<YAML
repositories:
  local: { type: local, path: "\${E2E_DIR}/repo" }
encryption:
  recipients_file: recipient.txt
  identity_file: identity.txt
defaults:
  repository: local
  timeout: 30m
daemon:
  api_token: \${DAEMON_TOKEN}
server:
  name: oidc-e2e
  listen: 127.0.0.1:$PORT
  public_url: $BASE
  tls: { cert_file: tls.crt, key_file: tls.key }
  session_key: \${SESSION_KEY}
  oidc:
    issuer: $ISSUER
    client_id: cistern
    client_secret: \${OIDC_CLIENT_SECRET}
  roles:
    - { group: cistern-viewers, roles: [viewer] }
    - { group: cistern-dba, roles: [operator, restorer] }
    - { group: cistern-approvers, roles: [approver] }
    - { group: cistern-admins, roles: [admin] }
  rate_limit: { login_per_minute: $LOGIN_LIMIT, write_per_minute: 30, read_per_minute: 300 }
  daemon: { url: "http://127.0.0.1:$API_PORT", token: "\${DAEMON_TOKEN}" }
jobs:
  - name: shop-db
    driver: postgres
    params: { host: 127.0.0.1, user: postgres, password: "\${PGPASSWORD}", databases: [shop] }
    restore_targets:
      - { name: scratch, description: "Same server, database shop_<suffix>", production: false, set: { clean: "true" } }
      - { name: production, description: "The live shop database server", production: true }
YAML

docker exec -d -u "$(id -u):$(id -g)" -e HOME=/tmp -e CISTERN_TMPDIR=/tmp -e E2E_DIR=/e2e -e PGPASSWORD="$PG_PASSWORD" \
  -e DAEMON_TOKEN="$DAEMON_TOKEN" "$NAME-pg" sh -c 'exec cistern daemon -c /e2e/cistern.yaml --listen :9464 --grace 10s > /e2e/daemon.log 2>&1'
for _ in $(seq 1 50); do curl -fs "http://127.0.0.1:$METRICS_PORT/healthz" >/dev/null && break; sleep 0.2; done
curl -fs "http://127.0.0.1:$METRICS_PORT/healthz" >/dev/null || fail "daemon did not start"
for _ in $(seq 1 50); do curl -s -o /dev/null "http://127.0.0.1:$API_PORT/internal/v1/runs" && break; sleep 0.2; done

step "daemon internal API: only on its own port, only with the token"
code=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$METRICS_PORT/internal/v1/runs")
[ "$code" = 404 ] || fail "/internal/v1/runs on the metrics port: HTTP $code, want 404"
code=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$API_PORT/internal/v1/runs")
[ "$code" = 401 ] || fail "/internal/v1/runs without a token: HTTP $code, want 401"
code=$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $DAEMON_TOKEN" "http://127.0.0.1:$API_PORT/internal/v1/runs")
[ "$code" = 200 ] || fail "/internal/v1/runs with the token: HTTP $code, want 200"
echo "metrics port 404, api port 401 without token, 200 with token"

start_server() {
  E2E_DIR=$DIR SESSION_KEY=$SESSION_KEY DAEMON_TOKEN=$DAEMON_TOKEN OIDC_CLIENT_SECRET=$CLIENT_SECRET \
    "$DIR/bin/cistern" server -c "$DIR/cistern.yaml" >> "$DIR/audit.jsonl" 2>> "$DIR/server.log" &
  SERVER_PID=$!
  for _ in $(seq 1 50); do curl -fs "${TLS[@]}" -o /dev/null "$BASE/healthz" && return; sleep 0.2; done
  fail "server did not start"
}

step "start cistern server on $BASE (TLS, OIDC)"
start_server

oidc_login() {
  local user=$1 jar=$JARS/$1 code loc action
  rm -f "$jar"
  read -r code loc < <(curl -sS "${TLS[@]}" -c "$jar" -b "$jar" -o /dev/null -w '%{http_code} %{redirect_url}\n' "$BASE/api/v1/auth/login?return_to=/jobs")
  [ "$code" = 302 ] || fail "$user: /auth/login HTTP $code"
  [[ "$loc" == "$ISSUER/protocol/openid-connect/auth?"* ]] || fail "$user: /auth/login redirects to $loc"
  for p in response_type=code client_id=cistern code_challenge_method=S256 code_challenge= state= nonce=; do
    [[ "$loc" == *"$p"* ]] || fail "$user: authorization request lacks $p: $loc"
  done
  curl -fsS -c "$jar" -b "$jar" -o "$JARS/page.html" "$loc" || fail "$user: keycloak login page"
  action=$(tr '\n' ' ' < "$JARS/page.html" | grep -o '<form[^>]*kc-form-login[^>]*>' | head -1 | sed -n 's/.*action="\([^"]*\)".*/\1/p' | sed 's/&amp;/\&/g')
  [ -n "$action" ] || fail "$user: no login form on the keycloak page"
  read -r code loc < <(curl -sS -c "$jar" -b "$jar" -o "$JARS/post.html" -w '%{http_code} %{redirect_url}\n' \
    --data-urlencode "username=$user" --data-urlencode "password=$user-e2e-password" --data-urlencode "credentialId=" "$action")
  [ "$code" = 302 ] || fail "$user: keycloak login HTTP $code: $(grep -o '<title>[^<]*' "$JARS/post.html" | head -1)"
  [[ "$loc" == "$BASE/api/v1/auth/callback?"* ]] || fail "$user: keycloak redirects to $loc"
  read -r code loc < <(curl -sS "${TLS[@]}" -c "$jar" -b "$jar" -o /dev/null -w '%{http_code} %{redirect_url}\n' "$loc")
  [ "$code" = 302 ] || fail "$user: /auth/callback HTTP $code"
  LANDED=${loc#"$BASE"}
}

CODE=""
BODY=""
api() {
  local who=$1 method=$2 path=$3 body=${4:-}
  local args=(-sS "${TLS[@]}" -o "$JARS/body" -w '%{http_code}' -X "$method")
  [ -f "$JARS/$who" ] && args+=(-b "$JARS/$who")
  [ "$method" != GET ] && [ -f "$JARS/$who.csrf" ] && args+=(-H "X-Cistern-CSRF: $(cat "$JARS/$who.csrf")")
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' -d "$body")
  CODE=$(curl "${args[@]}" "$BASE/api/v1$path")
  BODY=$(cat "$JARS/body")
}
expect() {
  [ "$CODE" = "$1" ] || fail "$2: HTTP $CODE, want $1: $BODY"
}
wait_run() {
  local who=$1 id=$2 state=""
  for _ in $(seq 1 120); do
    api "$who" GET "/runs/$id"
    expect 200 "run $id"
    state=$(jq -r .state <<<"$BODY")
    case $state in succeeded|failed) break ;; esac
    sleep 1
  done
  [ "$state" = succeeded ] || fail "run $id: $BODY"
  echo "run $id ($(jq -r .op <<<"$BODY") $(jq -r .job <<<"$BODY")): $state"
}

step "anonymous requests are rejected"
api nobody GET /me
expect 401 "anonymous /me"

step "authorization code flow with PKCE against Keycloak, roles from the groups claim"
check_user() {
  local user=$1 sub=$2 want=$3
  oidc_login "$user"
  [ "$LANDED" = /jobs ] || fail "$user landed on $LANDED, want /jobs"
  api "$user" GET /me
  expect 200 "$user /me"
  jq -r .csrf_token <<<"$BODY" > "$JARS/$user.csrf"
  [ "$(jq -r .user.id <<<"$BODY")" = "$sub" ] || fail "$user: subject $(jq -r .user.id <<<"$BODY"), want $sub"
  [ "$(jq -r .user.email <<<"$BODY")" = "$user@example.com" ] || fail "$user: email $(jq -r .user.email <<<"$BODY")"
  [ "$(jq -r '.roles | sort | join(",")' <<<"$BODY")" = "$want" ] || fail "$user: roles $(jq -c .roles <<<"$BODY"), want $want"
  echo "$user: $(jq -r .user.name <<<"$BODY"), roles $(jq -r '.roles | join(",")' <<<"$BODY")"
}
check_user vera "$SUB_VERA" viewer
check_user dan "$SUB_DAN" operator,restorer,viewer
check_user alice "$SUB_ALICE" approver,viewer
check_user adam "$SUB_ADAM" admin,operator,restorer,viewer
grep -q '__Host-cistern_session' "$JARS/vera" || fail "session cookie is not __Host-cistern_session"

oidc_login nora
[ "$LANDED" = "/denied?reason=no_role" ] || fail "nora landed on $LANDED, want /denied?reason=no_role"
api nora GET /me
[ "$CODE" = 401 ] || [ "$CODE" = 403 ] || fail "nora /me: HTTP $CODE, want 401 or 403"
echo "nora (no mapped group): /denied?reason=no_role, /me $CODE"

step "permissions"
api vera POST /jobs/shop-db/backup
expect 403 "viewer backup"
api dan POST /jobs/shop-db/backup
expect 202 "operator backup"
wait_run dan "$(jq -r .run_id <<<"$BODY")"

step "production restore: requester cannot approve, a second person can"
api dan POST /jobs/shop-db/restore '{"snapshot":"latest","target":"production","restore_suffix":"_approved","reason":"OIDC e2e production restore","confirm_job":"shop-db"}'
expect 202 "production restore request"
[ "$(jq -r .needs_approval <<<"$BODY")" = true ] || fail "production restore did not need approval: $BODY"
AP=$(jq -r .approval_id <<<"$BODY")
api dan POST "/approvals/$AP/approve" '{}'
expect 403 "requester approves"
api adam POST /jobs/shop-db/restore '{"snapshot":"latest","target":"production","reason":"admin self approval attempt","confirm_job":"shop-db"}'
expect 202 "admin production restore request"
OWN=$(jq -r .approval_id <<<"$BODY")
api adam POST "/approvals/$OWN/approve" '{}'
expect 403 "admin approves own request"
api alice POST "/approvals/$OWN/reject" '{"note":"duplicate request"}'
expect 200 "reject"
api alice POST "/approvals/$AP/approve" '{"note":"approved in the OIDC e2e"}'
expect 200 "approve"
[ "$(jq -r .state <<<"$BODY")" = approved ] || fail "approval state: $BODY"
[ "$(jq -r .decided_by.id <<<"$BODY")" = "$SUB_ALICE" ] || fail "decided_by: $(jq -c .decided_by <<<"$BODY")"
wait_run alice "$(jq -r .run_id <<<"$BODY")"
[ "$(psql_c -d shop_approved -c 'SELECT count(*) FROM orders')" = 20000 ] || fail "shop_approved not restored"
echo "restore approved by alice and finished; shop_approved has 20000 rows"

step "logout revokes the session"
cp "$JARS/dan" "$JARS/dan-old"
cp "$JARS/dan.csrf" "$JARS/dan-old.csrf"
api dan POST /auth/logout
expect 204 "logout"
api dan-old GET /me
expect 401 "old cookie after logout"
REVOKED=$(find "$DIR/repo/_admin/sessions/revoked" -type f 2>/dev/null | wc -l | tr -d ' ')
[ "$REVOKED" -ge 1 ] || fail "no revocation object under _admin/sessions/revoked/"
echo "old cookie: 401; $REVOKED revocation object(s) in the repository"

step "restart the server: revoked cookie stays revoked, other sessions survive"
stop_server
start_server
api dan-old GET /me
expect 401 "old cookie after restart"
api vera GET /me
expect 200 "vera after restart"
echo "revoked cookie: 401, vera: 200"

step "login rate limit ($LOGIN_LIMIT per minute)"
HIT=0
for i in $(seq 1 $((LOGIN_LIMIT * 3))); do
  code=$(curl -sS "${TLS[@]}" -D "$JARS/headers" -o "$JARS/body" -w '%{http_code}' "$BASE/api/v1/auth/login")
  if [ "$code" = 429 ]; then
    HIT=$i
    break
  fi
  [ "$code" = 302 ] || fail "login request $i: HTTP $code"
done
[ "$HIT" -gt 0 ] || fail "no 429 after $((LOGIN_LIMIT * 3)) login requests"
[ "$HIT" -le $((LOGIN_LIMIT * 2 + 2)) ] || fail "first 429 only at request $HIT (limit $LOGIN_LIMIT)"
RETRY=$(tr -d '\r' < "$JARS/headers" | sed -n 's/^[Rr]etry-[Aa]fter: *//p' | head -1)
[[ "$RETRY" =~ ^[0-9]+$ ]] && [ "$RETRY" -ge 1 ] || fail "429 without a valid Retry-After: '$RETRY'"
[ "$(jq -r .error.code < "$JARS/body")" = rate_limited ] || fail "429 body: $(cat "$JARS/body")"
echo "request $HIT: 429, Retry-After $RETRY, error rate_limited"

step "audit entries carry the Keycloak subjects"
audit_actors() {
  api vera GET "/audit?action=$1&limit=500"
  expect 200 "audit $1"
  jq -r '.entries[].actor.id' <<<"$BODY" | sort -u
}
LOGINS=$(audit_actors auth.login)
for sub in "$SUB_VERA" "$SUB_DAN" "$SUB_ALICE" "$SUB_ADAM"; do
  grep -qx "$sub" <<<"$LOGINS" || fail "no auth.login entry for $sub"
done
grep -qx "$SUB_NORA" <<<"$LOGINS" && fail "auth.login entry for the user without a role"
grep -qx "$SUB_NORA" <<<"$(audit_actors auth.denied)" || fail "no auth.denied entry for nora"
grep -qx "$SUB_DAN" <<<"$(audit_actors auth.logout)" || fail "no auth.logout entry for dan"
grep -qx "$SUB_ALICE" <<<"$(audit_actors approval.approve)" || fail "no approval.approve entry for alice"
grep -qx "$SUB_ADAM" <<<"$(audit_actors access.denied)" || fail "no access.denied entry for adam's self approval"
echo "auth.login for vera, dan, alice, adam; auth.denied for nora; auth.logout, approval.approve, access.denied present"

step "no secrets in the audit log or the server log"
for secret in "$CLIENT_SECRET" "$SESSION_KEY" "$DAEMON_TOKEN" "$PG_PASSWORD"; do
  if grep -rq "$secret" "$DIR/repo/_admin" "$DIR/audit.jsonl" "$DIR/server.log"; then
    fail "a secret was written to the audit log or the server log"
  fi
done

echo; echo "E2E OIDC: PASS"
