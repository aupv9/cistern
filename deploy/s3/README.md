# S3 repository policies

- `iam-backup-policy.json` — credentials for `backup`, `verify`, `restore` and `check`: can write and
  read snapshots, can only delete job lock files.
- `iam-prune-policy.json` — credentials for a separate `cistern prune` job: can delete snapshots and
  write only job locks and run records.
- `iam-server-policy.json` — credentials for `cistern server`: reads the repository, writes only under
  `_admin/` (audit, approvals, session revocations) and deletes only expired session revocations.
- `lifecycle.json` — removes noncurrent versions, orphaned delete markers and incomplete multipart
  uploads.

Replace `BUCKET` and `PREFIX`. See [docs/repositories.md](../../docs/repositories.md) for bucket
setup, Object Lock modes and how pruning works on a locked bucket.
