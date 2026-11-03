# Contributing to Cistern

Thanks for your interest in improving Cistern. This guide covers the development setup, the test
suite and the conventions the project follows.

## Development setup

Requirements:

- Go 1.24 or newer
- Docker (only for the end-to-end tests)
- GNU make

```sh
make build      # bin/cistern
make test       # unit and integration tests
make race       # the same with the race detector
make lint       # gofmt and go vet
make e2e        # end-to-end tests against real systems in Docker
```

No database or cloud account is needed for `make test`: drivers are tested with fake commands, and
the S3 client and repository are tested against an in-memory S3 server
([internal/s3/s3test](internal/s3/s3test)) that re-verifies SigV4 signatures, payload hashes and
`Content-MD5` and models versioning and Object Lock.

## End-to-end tests

`scripts/e2e-local.sh` builds a Linux binary, starts the systems in Docker and drives the real CLI:

| Target | What it runs |
| --- | --- |
| `postgres` | PostgreSQL 16, local repository, scheduled daemon, metrics, manual recovery with `age` |
| `s3` | PostgreSQL 16, S3 repository on MinIO with Object Lock, checked with the AWS CLI |
| `mysql` | MySQL 8.4, local repository |
| `mariadb` | MariaDB 11.4, local repository, manual recovery with `age` |
| `s3-driver` | S3 driver against MinIO, incremental backups, S3 repository with Object Lock |

```sh
./scripts/e2e-local.sh                  # all targets
./scripts/e2e-local.sh postgres mysql   # selected targets
```

Images can be overridden with `PG_IMAGE`, `MYSQL_IMAGE`, `MARIADB_IMAGE`, `MINIO_IMAGE` and
`AWS_CLI_IMAGE`. All images are pinned to an exact version and digest so CI does not change with
upstream releases; to update them, see the comment above the defaults in `scripts/e2e-local.sh`. The script
removes its containers and volumes when it exits.

CI runs every target as a separate job after the unit tests pass
([.github/workflows/ci.yaml](.github/workflows/ci.yaml)).

## Project layout

| Path | Contents |
| --- | --- |
| `cmd/cistern` | CLI |
| `internal/engine` | Job orchestration: locks, snapshots, verify, restore, prune, metrics, webhooks |
| `internal/driver` | `Driver` interface, registry, helpers for running external commands |
| `internal/driver/<name>` | One package per driver |
| `internal/pipeline` | SHA-256 → gzip → age → SHA-256 streaming pipeline |
| `internal/crypt` | age v1 format (X25519, ChaCha20-Poly1305) |
| `internal/repo` | Repository interface, local and S3 implementations |
| `internal/s3` | Hand-written S3 client (SigV4, multipart, conditional writes, Object Lock) |
| `internal/config` | YAML subset parser, environment expansion, validation |
| `internal/manifest`, `internal/retention`, `internal/cron` | Snapshot format, GFS retention, cron schedules |
| `internal/daemon`, `internal/metrics`, `internal/notify` | Scheduler and HTTP server, Prometheus, webhooks |
| `internal/thirdparty/xcrypto` | Unmodified subset of `golang.org/x/crypto` |
| `deploy/` | Docker, Kubernetes and systemd examples, IAM policies |
| `docs/` | User documentation |

See [docs/architecture.md](docs/architecture.md) for how the pieces fit together.

## Principles

- **Standard library only.** Cistern has no third-party Go modules. Code from `golang.org/x` may be
  vendored into `internal/thirdparty` unmodified, with its license.
- **Restorable without Cistern.** Artifacts must stay decodable with `age`, `gunzip` and the
  source system's own tools. Changes to the snapshot format need a format version bump and a
  migration story.
- **Secrets never reach argv, logs or manifests.** Pass passwords to child processes through
  environment variables or 0600 files in `driver.PrivateDir()`.
- **Fail loudly.** A backup that might be incomplete must fail, not succeed with a warning.

## Adding a driver

A driver implements four methods and registers itself in `init()`:

```go
type Driver interface {
	Name() string
	Check(ctx context.Context) error
	Backup(ctx context.Context, sink Sink) (Info, error)
	Restore(ctx context.Context, src Source) error
}
```

The engine handles compression, encryption, checksums, uploads, locking and metrics; the driver only
writes artifacts to the `Sink` and reads them from the `Source`. Then:

1. Run external tools through `driver.Run` so timeouts kill the whole process group and errors carry
   the tail of stderr.
2. Reject unknown params, validate names that end up in commands or SQL, and refuse to overwrite
   existing data on restore unless the user opts in.
3. Write unit tests with a fake `driver.Runner` (see `internal/driver/postgres/postgres_test.go`).
4. Add an end-to-end target that seeds data, backs up, verifies, restores into a new target and
   compares checksums, corrupts an artifact and checks that verify and restore fail.
5. Document the driver in `docs/drivers/` and add it to the tables in `README.md`.
6. Import the package in `internal/driver/all`.

## Pull requests

- Keep changes focused; one feature or fix per pull request.
- Run `make lint race` and the relevant end-to-end targets before submitting.
- Add or update tests for every behaviour change, and update the documentation and `CHANGELOG.md`
  (under *Unreleased*).
- Write commit messages in the imperative mood with a short summary line, followed by what changed
  and why.

Code comments in the existing code base are mostly in Vietnamese; new comments may be written in
English.

## License

By contributing, you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE).
