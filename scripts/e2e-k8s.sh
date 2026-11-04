#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
CISTERN_IMAGE=${CISTERN_IMAGE:-cistern:dev}
CURL_IMAGE=${CURL_IMAGE:-curlimages/curl:8.22.0@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777}
CLUSTER=${CLUSTER:-cistern-e2e}
NS=cistern-e2e
WORK=$(mktemp -d)
export KUBECONFIG=$WORK/kubeconfig
PF_PID=
PFS_PID=

cleanup() {
  [ -n "$PF_PID" ] && kill "$PF_PID" 2>/dev/null || true
  [ -n "$PFS_PID" ] && kill "$PFS_PID" 2>/dev/null || true
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT
step() { echo; echo "==> $*"; }
fail() {
  echo "FAIL: $*" >&2
  if kubectl get namespace "$NS" >/dev/null 2>&1; then
    echo "--- pods"; kubectl -n "$NS" get pods -o wide || true
    echo "--- cistern logs"; kubectl -n "$NS" logs deploy/cistern --tail=80 || true
    echo "--- cistern-server logs"; kubectl -n "$NS" logs deploy/cistern-server --tail=40 || true
    echo "--- events"; kubectl -n "$NS" get events --sort-by=.lastTimestamp | tail -20 || true
  fi
  exit 1
}
k() { kubectl -n "$NS" "$@"; }
pgsql() { k exec deploy/postgres -- psql -At -U postgres "$@"; }
rand() { head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'; }
probe() {
  local name=$1 labels=$2 url=$3
  k run "$name" --image="$CURL_IMAGE" --restart=Never ${labels:+--labels="$labels"} --command -- \
    sh -c "curl -sS -o /dev/null -w '%{http_code}' --connect-timeout 5 --max-time 8 '$url' 2>/dev/null; echo \" exit=\$?\"" >/dev/null
  k wait --for=jsonpath='{.status.phase}'=Succeeded "pod/$name" --timeout=180s >/dev/null || fail "probe pod $name did not finish"
  PROBE=$(k logs "$name")
  k delete pod "$name" --wait=false >/dev/null
  echo "  $name -> $url: $PROBE"
}

docker image inspect "$CISTERN_IMAGE" >/dev/null 2>&1 || fail "image $CISTERN_IMAGE not found: docker build -t $CISTERN_IMAGE ."

step "create k3d cluster $CLUSTER (kubeconfig kept in a temporary file)"
k3d cluster create "$CLUSTER" --no-lb --wait --timeout 180s \
  --kubeconfig-update-default=false --kubeconfig-switch-context=false \
  --k3s-arg "--disable=traefik@server:0" --k3s-arg "--disable=metrics-server@server:0" \
  --k3s-arg "--kubelet-arg=eviction-hard=imagefs.available<1%,nodefs.available<1%@server:0" \
  --k3s-arg "--kubelet-arg=image-gc-high-threshold=99@server:0" \
  --k3s-arg "--kubelet-arg=image-gc-low-threshold=98@server:0" >/dev/null
k3d kubeconfig get "$CLUSTER" > "$KUBECONFIG"
k3d image import "$CISTERN_IMAGE" -c "$CLUSTER" >/dev/null
kubectl get nodes -o wide | cut -c1-80

step "postgres and seed data"
kubectl create namespace "$NS" >/dev/null
k apply -f "$ROOT/scripts/e2e/k8s/postgres.yaml" >/dev/null
k rollout status deploy/postgres --timeout=300s >/dev/null
pgsql -c "CREATE DATABASE app" >/dev/null
pgsql -d app -c "CREATE TABLE t AS SELECT g AS id, md5(g::text) AS v FROM generate_series(1, 20000) g" >/dev/null
SUM=$(pgsql -d app -c "SELECT md5(string_agg(t::text, ',' ORDER BY id)) FROM t")

step "secrets: public key and passwords as env, private key as a file"
docker run --rm "$CISTERN_IMAGE" keygen > "$WORK/identity.txt"
RECIPIENT=$(sed -n 's/^# public key: //p' "$WORK/identity.txt")
k create secret generic cistern-keys --from-file=identity.txt="$WORK/identity.txt" >/dev/null
for s in cistern-env cistern-prune-env; do
  k create secret generic "$s" --from-literal=DB_PASSWORD=e2e-secret --from-literal=CISTERN_RECIPIENT="$RECIPIENT" >/dev/null
done
DAEMON_TOKEN=$(rand)
k create secret generic cistern-daemon-token --from-literal=token="$DAEMON_TOKEN" >/dev/null
k create secret generic cistern-server-env --from-literal=CISTERN_SESSION_KEY="$(rand)" \
  --from-literal=CISTERN_OIDC_CLIENT_SECRET="$(rand)" --from-literal=CISTERN_RECIPIENT="$RECIPIENT" >/dev/null

step "apply deploy/k8s/base, deploy/k8s/cronjob and deploy/k8s/server (via scripts/e2e/k8s)"
kubectl apply -k "$ROOT/scripts/e2e/k8s" | sed 's/^/  /'
k rollout status deploy/cistern --timeout=180s
k rollout status deploy/cistern-server --timeout=180s
POD=$(k get pod -l app.kubernetes.io/name=cistern -o jsonpath='{.items[0].metadata.name}')

step "pod hardening"
[ "$(k exec "$POD" -- id -u)" = 10001 ] || fail "not running as uid 10001"
if k exec "$POD" -- sh -c 'touch /probe' 2>/dev/null; then fail "root filesystem is writable"; fi
[ "$(k get pod "$POD" -o jsonpath='{.spec.containers[0].securityContext.capabilities.drop[0]}')" = ALL ] || fail "capabilities not dropped"
[ "$(k get pod "$POD" -o jsonpath='{.spec.automountServiceAccountToken}')" = false ] || fail "service account token mounted"
k exec "$POD" -- sh -c 'test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token' || fail "token present in pod"
echo "uid 10001, read-only root filesystem, no capabilities, no service account token"

step "server pod hardening: non-root, read-only root filesystem, no private key"
SPOD=$(k get pod -l app.kubernetes.io/name=cistern-server -o jsonpath='{.items[0].metadata.name}')
[ "$(k get pod "$SPOD" -o jsonpath='{.spec.securityContext.runAsNonRoot}')" = true ] || fail "server: runAsNonRoot not set"
[ "$(k exec "$SPOD" -- id -u)" = 10001 ] || fail "server: not running as uid 10001"
[ "$(k get pod "$SPOD" -o jsonpath='{.spec.containers[0].securityContext.readOnlyRootFilesystem}')" = true ] || fail "server: readOnlyRootFilesystem not set"
if k exec "$SPOD" -- sh -c 'touch /probe' 2>/dev/null; then fail "server: root filesystem is writable"; fi
[ "$(k get pod "$SPOD" -o jsonpath='{.spec.containers[0].securityContext.capabilities.drop[0]}')" = ALL ] || fail "server: capabilities not dropped"
[ "$(k get pod "$SPOD" -o jsonpath='{.spec.automountServiceAccountToken}')" = false ] || fail "server: service account token mounted"
if k get pod "$SPOD" -o jsonpath='{.spec.volumes[*].secret.secretName}' | grep -qw cistern-keys; then fail "server: cistern-keys secret mounted"; fi
k exec "$SPOD" -- sh -c 'test ! -e /etc/cistern-keys && test ! -e /etc/cistern-keys/identity.txt' || fail "server: private key present in the pod"
echo "uid 10001, runAsNonRoot, read-only root filesystem, no capabilities, no cistern-keys secret"

step "server endpoints"
k port-forward "pod/$SPOD" 18443:8443 >/dev/null 2>&1 &
PFS_PID=$!
for _ in $(seq 1 20); do curl -fsS localhost:18443/healthz >/dev/null 2>&1 && break; sleep 0.5; done
[ "$(curl -fsS localhost:18443/healthz)" = ok ] || fail "server /healthz"
CSP=$(curl -fsS -o /dev/null -D - localhost:18443/ | tr -d '\r' | sed -n 's/^[Cc]ontent-[Ss]ecurity-[Pp]olicy: //p')
[[ "$CSP" == *"default-src 'self'"* ]] || fail "server: no Content-Security-Policy on /: '$CSP'"
CODE=$(curl -sS -o /dev/null -w '%{http_code}' localhost:18443/api/v1/me)
[ "$CODE" = 401 ] || fail "server: /api/v1/me without a session: HTTP $CODE, want 401"
echo "/healthz ok, CSP: ${CSP:0:60}..., /api/v1/me: 401"
kill "$PFS_PID" 2>/dev/null || true
PFS_PID=

step "network policy: daemon API 9465 only from the server, metrics 9464 from anywhere"
probe np-server app.kubernetes.io/name=cistern-server http://cistern:9465/internal/v1/runs
[ "$PROBE" = "401 exit=0" ] || fail "daemon API from a cistern-server pod: '$PROBE', want 401"
probe np-other "" http://cistern:9465/internal/v1/runs
[[ "$PROBE" == "000 exit="* ]] && [ "$PROBE" != "000 exit=0" ] || fail "daemon API reachable from an unlabeled pod: '$PROBE'"
probe np-metrics "" http://cistern:9464/metrics
[ "$PROBE" = "200 exit=0" ] || fail "metrics from an unlabeled pod: '$PROBE', want 200"
probe np-internal "" http://cistern:9464/internal/v1/runs
[ "$PROBE" = "404 exit=0" ] || fail "/internal on the metrics port: '$PROBE', want 404"

step "daemon runs scheduled backups and verifies"
sleep 70
N=$(k exec "$POD" -- cistern list --job app --json | grep -c '"id"' || true)
echo "snapshots after ~70s: $N (retention last: 2)"
[ "$N" -ge 1 ] || fail "no scheduled backups"
k port-forward "svc/cistern" 19464:9464 >/dev/null 2>&1 &
PF_PID=$!
for _ in $(seq 1 20); do curl -fsS localhost:19464/healthz >/dev/null 2>&1 && break; sleep 0.5; done
METRICS=$(curl -fsS localhost:19464/metrics)
echo "$METRICS" | grep -E '^cistern_(backup_last_success|verify_last_success|snapshots)'
echo "$METRICS" | grep -q 'cistern_verify_last_success_timestamp_seconds{cistern_job="app"}' || fail "scheduled verify did not run"
curl -fsS localhost:19464/status | grep -q '"op": "verify"' || fail "status lacks verify task"
k logs "$POD" | grep -q 'level=ERROR' && { k logs "$POD" | grep 'level=ERROR' | head -3; fail "errors in daemon log"; }

step "one-shot mode: CronJobs run as Jobs"
k scale deploy/cistern --replicas=0 >/dev/null
k wait --for=delete pod -l app.kubernetes.io/name=cistern --timeout=360s >/dev/null 2>&1 || true
for cj in cistern-backup cistern-prune; do
  k create job --from=cronjob/$cj "manual-${cj#cistern-}" >/dev/null
  k wait --for=condition=complete "job/manual-${cj#cistern-}" --timeout=300s >/dev/null || { k logs "job/manual-${cj#cistern-}"; fail "$cj failed"; }
  echo "$cj:"; k logs "job/manual-${cj#cistern-}" | grep -vE 'level=INFO' | sed 's/^/  /'
done
k scale deploy/cistern --replicas=1 >/dev/null
k rollout status deploy/cistern --timeout=180s >/dev/null
POD=$(k get pod -l app.kubernetes.io/name=cistern -o jsonpath='{.items[0].metadata.name}')

step "restore into a new database from inside the cluster"
k exec "$POD" -- cistern restore --job app --set restore_suffix=_restored --yes
[ "$(pgsql -d app_restored -c "SELECT md5(string_agg(t::text, ',' ORDER BY id)) FROM t")" = "$SUM" ] || fail "checksum mismatch after restore"
echo "checksum match: $SUM"

step "graceful shutdown settings"
[ "$(k get deploy cistern -o jsonpath='{.spec.template.spec.terminationGracePeriodSeconds}')" -gt 300 ] || fail "terminationGracePeriodSeconds must exceed --grace"
k delete pod "$POD" --wait=true >/dev/null
echo "pod deleted cleanly"

echo; echo "E2E K8S: PASS"
