# Changelog

All notable changes to this project are documented in this file. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to follow
[Semantic Versioning](https://semver.org/) once it reaches 1.0.

## [Unreleased]

## [0.3] - 2026-11-03

Adds the Admin UI: a web interface and REST API to watch jobs, run backups and verifies, and restore
with two-person approval for production, served by a new `cistern server` that holds no private key.

### Added

- Container image for `linux/amd64` and `linux/arm64` on `oraclelinux:9-slim` with the PostgreSQL
  client from PGDG (`PG_MAJOR`, default 16) and the MySQL 8.4 client, GPG-verified; runs as UID 10001
  on a read-only root filesystem. The MySQL client also backs up MariaDB servers.
- Kubernetes manifests in `deploy/k8s`: `base` (daemon Deployment, Service, NetworkPolicy,
  ServiceAccount without a token), `cronjob` (`backup --all`, `prune --all` with separate
  credentials) and `monitoring` (PrometheusRule). Pods run non-root with a read-only root
  filesystem, no capabilities and `seccompProfile: RuntimeDefault`.
- End-to-end target `image` and `scripts/e2e-k8s.sh` (k3d), both run in CI.
- Images published to `ghcr.io/aupv9/cistern` (`linux/amd64`, `linux/arm64`) by CI after all tests
  pass: `main` and `sha-<commit>` for `main`, `vX.Y` and `latest` for release tags.
- **Admin UI**: `cistern server` serves a web UI and the REST API in `api/openapi.yaml`: overview
  with health and alerts, job details, snapshots, run history with live progress and log tail,
  backups, verifies and restores from the browser. OIDC sign-in (code flow with PKCE, RS256/ES256),
  group-to-role mapping (`viewer`, `operator`, `restorer`, `approver`, `admin`), encrypted session
  cookies with absolute and idle timeouts, CSRF header, strict CSP with hashed inline scripts. The
  server holds no private key; `--insecure-dev-login` for local development. See
  [docs/admin-ui.md](docs/admin-ui.md).
- Daemon internal API (`/internal/v1/runs`, enabled by `daemon.api_token`): the server's
  operations run through the same engine path as scheduled ones; `/status` adds `running`,
  `timezone` and `api`.
- Run records: every backup, verify, restore and prune, from the CLI, a schedule or the UI, writes
  `<prefix>/_admin/runs/<job>/<id>.json`.
- `jobs[].restore_targets`: named restore destinations; the UI can restore only into these (plus
  `restore_suffix`). Restores into production targets need an approval by someone else, stored under
  `<prefix>/_admin/approvals/`.
- Append-only audit log in the repository (`<prefix>/_admin/audit/`, one object per entry, Object
  Lock retention `server.audit_retention_days`), also written to stdout as JSON lines; filters,
  paging and CSV/JSON Lines export.
- `make dev-ui` (`scripts/dev-ui.sh`): one command for a local UI backend with seeded history;
  `make ui`, `make build-ui`.
- `deploy/k8s/server` kustomization for the Admin UI; the daemon reads `CISTERN_DAEMON_TOKEN` from
  the optional Secret `cistern-daemon-token`.
- CI: a `web` job (lint, typecheck, test, build) that the image depends on, and the `server`
  end-to-end target (`scripts/e2e/server.sh`).

### Changed

- `deploy/k8s` is split into `base`, `cronjob` and `monitoring`; the previous single CronJob and
  kustomization are removed.
- The Dockerfile builds the UI in a `node:22.23.4-alpine` stage pinned by digest and embeds it with
  `-tags ui`; the image exposes port 8443 as well.
- `--config FILE` is accepted as an alias of `-c FILE`.
- Environment expansion is process-specific: an unset `${VAR}` in a section a process does not use
  expands to an empty string (for example, the server needs no database passwords and the daemon no
  session key). See [Configuration](docs/configuration.md#environment-variables).
- Local repositories list only the directory of the requested prefix instead of the whole tree.

### Fixed

- The daemon skips a scheduled run when another operation of the same job is already running,
  instead of logging it as a failed run (backup and verify coming due in the same tick).

## [0.2] - 2026-11-03

Everything planned for the first production milestone is now implemented: PostgreSQL, MySQL/MariaDB
and S3 sources, local and S3 repositories with Object Lock, and a scheduling daemon.

### Added

- **S3 driver**: copies the objects of an S3-compatible bucket/prefix, one artifact per
  object plus `index.json`. Incremental by ETag and size: unchanged objects point at the snapshot
  holding their data, which is recorded in `depends_on` so retention keeps it. Objects that change
  during a backup fail the job (`If-Match`). Restores into `restore_bucket` / `restore_prefix`, can
  be filtered with `only`, refuse to overwrite unless `overwrite=true`, and keep Content-Type.
- **MySQL/MariaDB driver**: `mysqldump` or `mariadb-dump` with `--single-transaction`,
  routines, triggers and events; picks the available tool set (`client: auto`). Credentials go
  through a 0600 option file. Dumps restore under another name (`restore_suffix`) and keep the
  original character set and collation. Refuses a client older than a same-family server; adds
  `--column-statistics=0` for MySQL 8 clients against MariaDB.
- **Scheduler**: `cistern daemon` runs `schedule` (backup) and `verify_schedule` (verify the
  newest snapshot) with five-field cron expressions, `@daily`-style macros and `@every`. Jobs run in
  parallel, overlapping runs of the same job are skipped, and SIGTERM waits up to `--grace` for
  running jobs. `/status` shows each task's next run and last result.
- **S3 repository** with optional **Object Lock**: own S3 client with SigV4,
  multipart uploads bounded to one part in memory, `Content-MD5`, conditional writes for job locks,
  retries on network errors, 5xx and 429, TLS 1.2+, `ca_file` and `session_token`. Artifacts and
  manifests are written with GOVERNANCE or COMPLIANCE retention; `cistern check` verifies that the
  bucket has Object Lock enabled. Least-privilege IAM policies and a lifecycle rule in `deploy/s3`.
- `Sink.Previous()` and access to `depends_on` snapshots for incremental drivers.
- End-to-end test targets `s3`, `mysql`, `mariadb` and `s3-driver` (MinIO checked with the AWS CLI).
- Project documentation in English: README, `docs/`, CONTRIBUTING, SECURITY, CODE_OF_CONDUCT.
- Apache License 2.0 and NOTICE.
- GitHub Actions CI runs gofmt, vet, unit tests (also with the race detector) and every end-to-end
  target; all end-to-end images are pinned to exact versions by digest.

### Changed

- CLI help and messages are in English.

### Fixed

- Empty objects were uploaded with chunked encoding instead of `Content-Length: 0` and rejected by
  S3 and MinIO.
- `docker-compose.yaml` pointed `CISTERN_TMPDIR` at a directory that did not exist.
- End-to-end tests left anonymous Docker volumes behind.

### Known issues

- `hooks` are parsed but not executed yet.
- The container image does not include the PostgreSQL and MySQL clients yet, and the Docker,
  Kubernetes and systemd files in `deploy/` have not been verified.
- S3 credentials come from the configuration or `AWS_*` variables; IRSA and instance profiles are not
  supported yet.

## [0.1] - 2026-11-02

First release: end-to-end PostgreSQL backups to a local repository.

### Added

- YAML configuration with `${VAR}` expansion and strict validation.
- `cistern backup` for one job or all jobs, with job locks in the repository.
- PostgreSQL driver: `pg_dump` per database and `pg_dumpall --globals-only`.
- Local repository with atomic writes.
- `cistern restore` by latest, ID or prefix, with `--set` overrides and confirmation.
- `cistern verify`: full verification with the private key, stored checksums without it.
- GFS retention with `--dry-run` and `prune_after_backup`, keeping dependencies.
- Client-side encryption in the age format, interoperable with the `age` CLI.
- Streaming pipeline with SHA-256 of original and stored bytes.
- Prometheus metrics through the daemon, Pushgateway or textfile, alert rules, and
  Slack/Teams/Telegram/JSON webhooks.
- `cistern keygen`, `cistern check`, `cistern daemon` (HTTP only).

[Unreleased]: https://github.com/aupv9/cistern/compare/v0.3...HEAD
[0.3]: https://github.com/aupv9/cistern/compare/v0.2...v0.3
[0.2]: https://github.com/aupv9/cistern/compare/v0.1...v0.2
[0.1]: https://github.com/aupv9/cistern/releases/tag/v0.1
