#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
CISTERN_IMAGE=${CISTERN_IMAGE:-cistern:dev}
CLUSTER=${CLUSTER:-cistern-e2e}
NS=cistern-e2e
WORK=$(mktemp -d)
export KUBECONFIG=$WORK/kubeconfig
PF_PID=

cleanup() {
  [ -n "$PF_PID" ] && kill "$PF_PID" 2>/dev/null || true
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
    echo "--- events"; kubectl -n "$NS" get events --sort-by=.lastTimestamp | tail -20 || true
  fi
  exit 1
}
k() { kubectl -n "$NS" "$@"; }
pgsql() { k exec deploy/postgres -- psql -At -U postgres "$@"; }

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

step "apply deploy/k8s/base and deploy/k8s/cronjob (via scripts/e2e/k8s)"
kubectl apply -k "$ROOT/scripts/e2e/k8s" | sed 's/^/  /'
k rollout status deploy/cistern --timeout=180s
POD=$(k get pod -l app.kubernetes.io/name=cistern -o jsonpath='{.items[0].metadata.name}')

step "pod hardening"
[ "$(k exec "$POD" -- id -u)" = 10001 ] || fail "not running as uid 10001"
if k exec "$POD" -- sh -c 'touch /probe' 2>/dev/null; then fail "root filesystem is writable"; fi
[ "$(k get pod "$POD" -o jsonpath='{.spec.containers[0].securityContext.capabilities.drop[0]}')" = ALL ] || fail "capabilities not dropped"
[ "$(k get pod "$POD" -o jsonpath='{.spec.automountServiceAccountToken}')" = false ] || fail "service account token mounted"
k exec "$POD" -- sh -c 'test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token' || fail "token present in pod"
echo "uid 10001, read-only root filesystem, no capabilities, no service account token"

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
