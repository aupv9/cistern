# Disaster recovery

Use `cistern restore` whenever you can — it checks every checksum on the way. This page is for the
case where Cistern itself is unavailable: everything in a repository can be recovered with
[age](https://age-encryption.org), `gunzip` and the source system's own tools.

Practise this regularly. A backup you have never restored is a hope, not a backup.

## What you need

- The repository (a copy of the directory, or read access to the bucket).
- The private key (`identity.txt`).
- `age`, `gunzip`, `sha256sum`, and the client tools of the system you restore.

## 1. Find the snapshot

```
<prefix>/<job>/<snapshot-id>/manifest.json
<prefix>/<job>/<snapshot-id>/artifacts/...
```

Snapshot IDs start with their UTC start time (`20261102T020000Z-3f9a`), so the newest sorts last. A
directory without `manifest.json` is an incomplete snapshot — skip it.

`manifest.json` is plain JSON. It lists the artifacts with their paths and checksums:

```json
{
  "format": 1, "id": "20261102T020000Z-3f9a", "job": "orders-db", "driver": "postgres",
  "compression": "gzip", "encryption": "age-x25519",
  "artifacts": [
    { "name": "db/orders.dump", "path": "artifacts/db/orders.dump.gz.age",
      "plain_size": 52428800, "stored_size": 10485760,
      "plain_sha256": "…", "stored_sha256": "…" }
  ]
}
```

## 2. Check and decrypt

```sh
cd <snapshot-dir>
sha256sum artifacts/db/orders.dump.gz.age            # must equal stored_sha256
age -d -i identity.txt artifacts/db/orders.dump.gz.age | gunzip > orders.dump
sha256sum orders.dump                                # must equal plain_sha256
```

Suffixes tell you which steps apply: `.gz` means gzip, `.age` means age; an artifact with neither was
stored as is (`compression: none`, `encryption: none`).

## 3. Restore with the native tools

| Driver | Command |
| --- | --- |
| `postgres` | `createdb orders && pg_restore --exit-on-error -d orders orders.dump`; roles: `psql -d postgres -f globals.sql` |
| `mysql` | `mysql -e 'CREATE DATABASE shop' && mysql shop < shop.sql` |
| `s3` | decrypt `index.json`, then for every entry decrypt its artifact and upload it to its key (see [S3 driver](drivers/s3.md#manual-recovery)) |

For S3 snapshots, an index entry with a non-empty `snapshot` is stored in that other snapshot's
directory of the same job.

## Restoring from a locked S3 bucket

Locked objects can be read normally. If a snapshot was pruned, its objects are hidden behind delete
markers but still exist until their retention expires; list them with
`aws s3api list-object-versions --prefix <prefix>/<job>/<snapshot-id>/` and download with
`--version-id`.

## Restore drills: evidence of recoverability

Checksums prove a snapshot is intact; only a restore proves it is usable. Configure a
[restore drill](drills.md) for every critical job: the daemon then regularly restores the newest
snapshot into a scratch target, compares table and row counts with the dump, runs your SQL checks and
drops the copy. Each drill leaves a run record with every check, the row counts and the duration, so
the Admin UI (and `cistern drills --job J`) shows when a backup was last proven restorable and how
long it took. `CisternDrillFailing` and `CisternDrillStale` alert when that evidence is missing.

Automatic drills do not replace practising the manual path below: they use Cistern and the private
key on the backup host.

## Checklist for a restore drill

- [ ] The private key can be found and decrypts an artifact
- [ ] The newest snapshot of every critical job restores into a scratch database or prefix (a passed
      `drill` within `max_age_days` covers this and the next item)
- [ ] Row counts or checksums match the source
- [ ] The time from start to a usable restore is within your RTO
- [ ] The steps above were done without Cistern at least once
