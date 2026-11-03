# Changelog

All notable changes to this project are documented in this file. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to follow
[Semantic Versioning](https://semver.org/) once it reaches 1.0. Requirement IDs (FR-xx, NFR-xx) refer
to the [product requirements](docs/design/PRD.md).

## Unreleased

All P0 requirements of the product requirements are now implemented.

### Added

- **S3 driver** (FR-12): copies the objects of an S3-compatible bucket/prefix, one artifact per
  object plus `index.json`. Incremental by ETag and size: unchanged objects point at the snapshot
  holding their data, which is recorded in `depends_on` so retention keeps it. Objects that change
  during a backup fail the job (`If-Match`). Restores into `restore_bucket` / `restore_prefix`, can
  be filtered with `only`, refuse to overwrite unless `overwrite=true`, and keep Content-Type.
- **MySQL/MariaDB driver** (FR-04): `mysqldump` or `mariadb-dump` with `--single-transaction`,
  routines, triggers and events; picks the available tool set (`client: auto`). Credentials go
  through a 0600 option file. Dumps restore under another name (`restore_suffix`) and keep the
  original character set and collation. Refuses a client older than a same-family server; adds
  `--column-statistics=0` for MySQL 8 clients against MariaDB.
- **Scheduler** (FR-21): `cistern daemon` runs `schedule` (backup) and `verify_schedule` (verify the
  newest snapshot) with five-field cron expressions, `@daily`-style macros and `@every`. Jobs run in
  parallel, overlapping runs of the same job are skipped, and SIGTERM waits up to `--grace` for
  running jobs. `/status` shows each task's next run and last result.
- **S3 repository** (FR-15) with optional **Object Lock** (NFR-04): own S3 client with SigV4,
  multipart uploads bounded to one part in memory, `Content-MD5`, conditional writes for job locks,
  retries on network errors, 5xx and 429, TLS 1.2+, `ca_file` and `session_token`. Artifacts and
  manifests are written with GOVERNANCE or COMPLIANCE retention; `cistern check` verifies that the
  bucket has Object Lock enabled. Least-privilege IAM policies and a lifecycle rule in `deploy/s3`.
- `Sink.Previous()` and access to `depends_on` snapshots for incremental drivers.
- End-to-end test targets `s3`, `mysql`, `mariadb` and `s3-driver` (MinIO checked with the AWS CLI).
- Project documentation in English: README, `docs/`, CONTRIBUTING, SECURITY, CODE_OF_CONDUCT.
- Apache License 2.0 and NOTICE.

### Changed

- CLI help and messages are in English.

### Fixed

- Empty objects were uploaded with chunked encoding instead of `Content-Length: 0` and rejected by
  S3 and MinIO.
- `docker-compose.yaml` pointed `CISTERN_TMPDIR` at a directory that did not exist.
- End-to-end tests left anonymous Docker volumes behind.

## 0.1 — 2026-11-02

First release: end-to-end PostgreSQL backups to a local repository.

### Added

- YAML configuration with `${VAR}` expansion and strict validation (FR-01).
- `cistern backup` for one job or all jobs (FR-02), with job locks in the repository (FR-19).
- PostgreSQL driver: `pg_dump` per database and `pg_dumpall --globals-only` (FR-03).
- Local repository with atomic writes (FR-14).
- `cistern restore` by latest, ID or prefix, with `--set` overrides and confirmation (FR-16).
- `cistern verify`: full verification with the private key, stored checksums without it (FR-17).
- GFS retention with `--dry-run` and `prune_after_backup`, keeping dependencies (FR-18).
- Client-side encryption in the age format, interoperable with the `age` CLI (NFR-01).
- Streaming pipeline with SHA-256 of original and stored bytes (NFR-03, NFR-06).
- Prometheus metrics through the daemon, Pushgateway or textfile (FR-22), alert rules, and
  Slack/Teams/Telegram/JSON webhooks (FR-23).
- `cistern keygen`, `cistern check`, `cistern daemon` (HTTP only).
