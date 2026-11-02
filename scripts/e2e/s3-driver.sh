# Chạy trong container AWS CLI (xem run_s3_driver trong scripts/e2e-local.sh): nguồn là bucket "data"
# trên MinIO, repository là bucket "backups" có Object Lock.
set -euo pipefail
step() { echo; echo "==> $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
export AWS_ENDPOINT_URL=http://minio:9000 AWS_DEFAULT_REGION=us-east-1 AWS_PAGER=
export CISTERN_CONFIG=/e2e/cistern.yaml
mkdir -p /e2e/src/dir && cd /e2e
keys() { aws s3api list-objects-v2 --bucket data --prefix "$1" --query 'Contents[].[Key]' --output text | grep -v '^None$' || true; }
hash() { aws s3 cp "s3://data/$1" - | sha256sum | cut -d' ' -f1; }
extra() { cistern list --job site --json | grep -o "\"$1\": \"[^\"]*\"" | sed -n "${2}p" | cut -d'"' -f4; }

step "buckets and source objects (aws cli)"
aws s3api create-bucket --bucket data >/dev/null
aws s3api create-bucket --bucket backups --object-lock-enabled-for-bucket >/dev/null
head -c 12000000 /dev/urandom > src/big.bin
echo "version 1" > src/a.txt
echo "to be deleted" > "src/dir/b c.txt"
printf 'jpeg bytes' > "src/ảnh đẹp.jpg"
: > src/empty
echo plus > "src/a+b=c@d.txt"
aws s3 cp --quiet --recursive src/ s3://data/site/
aws s3 cp --quiet "src/ảnh đẹp.jpg" "s3://data/site/ảnh đẹp.jpg" --content-type image/jpeg
echo outside | aws s3 cp --quiet - s3://data/other/outside.txt
keys site/

step "keygen + config (driver s3 → repository s3 with Object Lock)"
cistern keygen --out identity.txt > recipient.txt
cat > cistern.yaml <<'YAML'
repositories:
  minio:
    type: s3
    endpoint: http://minio:9000
    bucket: backups
    prefix: cistern
    path_style: true
    access_key_id: ${AWS_ACCESS_KEY_ID}
    secret_access_key: ${AWS_SECRET_ACCESS_KEY}
    object_lock: { mode: GOVERNANCE, days: 1 }
encryption:
  recipients_file: recipient.txt
  identity_file: identity.txt     # cần để đọc index của lần trước (incremental)
jobs:
  - name: site
    driver: s3
    timeout: 10m
    retention: { last: 1 }
    params:
      endpoint: http://minio:9000
      bucket: data
      prefix: site/
      path_style: true
      access_key_id: ${AWS_ACCESS_KEY_ID}
      secret_access_key: ${AWS_SECRET_ACCESS_KEY}
YAML
cistern check --job site

step "backup 1 (full)"
cistern backup --job site | tee b1.out
FIRST=$(awk '{print $2}' b1.out)
[ "$(extra copied 1)" = 6 ] || fail "expected 6 objects copied"

step "change source: modify a.txt, add new.txt, delete dir/b c.txt"
echo "version 2" | aws s3 cp --quiet - s3://data/site/a.txt
echo added | aws s3 cp --quiet - s3://data/site/new.txt
aws s3 rm --quiet "s3://data/site/dir/b c.txt"

step "backup 2 (incremental)"
cistern backup --job site
cistern list --job site
echo "backup 2: copied=$(extra copied 2) reused=$(extra reused 2) mode=$(extra mode 2)"
[ "$(extra copied 2)" = 2 ] && [ "$(extra reused 2)" = 4 ] || fail "expected 2 copied (a.txt, new.txt), 4 reused"
cistern list --job site --json | grep -q "\"$FIRST\"" || fail "backup 2 should depend on backup 1"

step "verify all snapshots"
cistern verify --job site --snapshot all

step "restore latest → data/restored/, compare sha256 of every object"
cistern restore --job site --set restore_prefix=restored/ --yes
N=0
while IFS= read -r key; do
  rel=${key#site/}
  [ "$(hash "$key")" = "$(hash "restored/$rel")" ] || fail "content differs: $rel"
  N=$((N + 1))
done < <(keys site/)
[ "$(keys restored/ | wc -l)" -eq "$N" ] || fail "restored object count differs"
echo "$N objects identical (sha256)"
CT=$(aws s3api head-object --bucket data --key "restored/ảnh đẹp.jpg" --query ContentType --output text)
[ "$CT" = image/jpeg ] || fail "content type not preserved: $CT"
[ -z "$(keys restored/dir/)" ] || fail "deleted object restored from latest"

step "restore backup 1 → data/v1/ (deleted object back, old a.txt)"
cistern restore --job site --snapshot "$FIRST" --set restore_prefix=v1/ --yes
[ "$(aws s3 cp s3://data/v1/a.txt -)" = "version 1" ] || fail "v1/a.txt"
[ "$(aws s3 cp "s3://data/v1/dir/b c.txt" -)" = "to be deleted" ] || fail "v1/dir/b c.txt"
[ "$(hash v1/big.bin)" = "$(sha256sum src/big.bin | cut -d' ' -f1)" ] || fail "v1/big.bin"

step "restore onto existing objects is refused without overwrite=true"
if cistern restore --job site --yes 2>err.txt; then fail "overwrite allowed"; fi
grep -q "6 object(s) already exist" err.txt || { cat err.txt; fail "expected all 6 objects reported as existing"; }
grep "already exist" err.txt | tail -1

step "prune (last: 1) keeps backup 1 because backup 2 depends on it"
cistern prune --job site | tee prune.txt
grep -q "removed 0" prune.txt || fail "prune removed a snapshot that is still needed"
grep -q "depends_on" prune.txt || fail "expected depends_on reason"

step "repository objects are locked"
ART=$(aws s3api list-objects-v2 --bucket backups --prefix "cistern/site/$FIRST/artifacts/objects/" --query 'Contents[0].Key' --output text)
[ "$(aws s3api get-object-retention --bucket backups --key "$ART" --query Retention.Mode --output text)" = GOVERNANCE ] || fail "artifact not locked"
echo "artifact $ART is under GOVERNANCE retention"

echo; echo "E2E S3 DRIVER: PASS"
