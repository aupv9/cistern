# Repositories

A repository is where snapshots are stored. Cistern writes only new objects into it: a snapshot is a
directory of artifacts plus a `manifest.json` written last, so a half-written snapshot never looks
complete.

```yaml
repositories:
  local:
    type: local
    path: /var/lib/cistern/repo
  s3main:
    type: s3
    endpoint: https://s3.ap-southeast-1.amazonaws.com
    region: ap-southeast-1
    bucket: company-backups
    prefix: cistern
    object_lock: { mode: COMPLIANCE, days: 7 }
```

All keys are listed in the [configuration reference](configuration.md#repositories).

## Local

Stores objects as files under `path`. Every file is written to a temporary file in the same
directory, synced and renamed into place; job locks are created atomically with `link(2)`. Useful
for development, for a mounted NAS, or as the first copy that another tool replicates off-site.

## S3-compatible

Works with AWS S3 and S3-compatible services (tested with MinIO). The client is part of Cistern and
uses no SDK:

- Signature Version 4, checked against the AWS documentation test vectors;
- single `PutObject` for small artifacts, multipart upload for larger ones, holding at most one part
  (`part_size_mb`, default 16 MiB) in memory; incomplete uploads are aborted when a job fails;
- `Content-MD5` on every upload, so S3 rejects corrupted bodies;
- job locks written with `If-None-Match: *`, so only one host can hold a job's lock;
- automatic retries with backoff on network errors, HTTP 5xx and 429;
- TLS 1.2 or newer, optional `ca_file`, `insecure_skip_verify` only when set explicitly.

Use `path_style: true` for MinIO and most self-hosted services. An artifact can have at most 10,000
parts; raise `part_size_mb` for artifacts larger than about 156 GiB.

## Object Lock

With `object_lock`, every artifact and manifest is written with a retention date `days` days in the
future. Job lock files are never locked, so they can be released.

| Mode | Who can delete a locked version before it expires |
| --- | --- |
| `GOVERNANCE` | Users with `s3:BypassGovernanceRetention` (never grant it to Cistern) |
| `COMPLIANCE` | Nobody, including the root account. Recommended for production |

`cistern check` fails if `object_lock` is configured but the bucket does not have Object Lock
enabled, and S3 itself rejects locked uploads to such a bucket.

### Creating the bucket

Object Lock can only be enabled when a bucket is created (it also enables versioning):

```sh
aws s3api create-bucket --bucket BUCKET --object-lock-enabled-for-bucket \
  --create-bucket-configuration LocationConstraint=ap-southeast-1
aws s3api put-bucket-lifecycle-configuration --bucket BUCKET \
  --lifecycle-configuration file://deploy/s3/lifecycle.json
```

On MinIO: `mc mb --with-lock ALIAS/BUCKET`. A default retention on the bucket is not needed.

### Pruning a locked repository

Because the bucket is versioned, prune deletes create **delete markers**: the snapshot disappears
from `cistern list`, but the locked versions stay — and can still be read by version ID — until
their retention expires. The lifecycle rule in [deploy/s3/lifecycle.json](../deploy/s3/lifecycle.json)
then removes noncurrent versions (`NoncurrentVersionExpiration`; set `NoncurrentDays` to at least
`days`), orphaned delete markers and incomplete multipart uploads.

Choose `days` no longer than your shortest retention period (for example `daily: 7` → `days: 7` or
less). A longer lock does not break pruning, but deleted data is billed until the lock expires.

## Least-privilege credentials

Give `cistern backup` credentials that cannot destroy existing backups, and run prune with separate
credentials. Replace `BUCKET` and `PREFIX` in the policies:

| Credentials | Policy | Used by |
| --- | --- | --- |
| Backup | [iam-backup-policy.json](../deploy/s3/iam-backup-policy.json) | `backup`, `verify`, `restore`, `check` — can write and read, can only delete job lock files |
| Prune | [iam-prune-policy.json](../deploy/s3/iam-prune-policy.json) | a separate `cistern prune` job |

With backup-only credentials:

- set `prune_after_backup: false` and schedule `cistern prune` with the prune credentials;
- a failed backup cannot delete the artifacts it already wrote; Cistern logs a warning and the next
  prune removes the incomplete snapshot once it is older than one hour.

> These policies have been reviewed but not yet exercised against AWS IAM in the test suite.

## Off-site copies

Cistern writes one copy. For the 3-2-1 rule, replicate the repository bucket to another region or
account (S3 Replication, MinIO site replication) or copy a local repository with your usual tools.
Snapshots are immutable once written, which makes them easy to replicate.
