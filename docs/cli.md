# CLI reference

```
cistern <command> [flags]
```

Flags follow the command (`cistern backup -c cistern.yaml --job orders-db`). Every command that reads
the configuration accepts `-c FILE` (or `--config FILE`); without it Cistern uses `$CISTERN_CONFIG`, then
`/etc/cistern/cistern.yaml`.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | The operation failed (including verify finding a corrupted artifact, a drill check failing, or any job failing with `--all`) |
| `2` | Usage error: unknown command or flag, missing `--job`, both `--job` and `--all` |

## `backup`

```
cistern backup --job NAME
cistern backup --all
```

Takes a snapshot now and prints `job<TAB>snapshot-id`. With `--all`, jobs run one after another; a
failing job does not stop the others, and the exit code is 1 if any job failed. When
`prune_after_backup` is enabled, retention is applied right after a successful backup.

A backup takes the job lock in the repository. A second backup of the same job fails with
`job is locked: backup by HOST (pid PID) since TIME` until the first finishes or its lock expires
(job timeout + 10 minutes).

## `list`

```
cistern list --job NAME [--json]
```

Lists completed snapshots, oldest first. `--json` prints the full manifests.

## `verify`

```
cistern verify --job NAME [--snapshot latest|ID|PREFIX|all] [--json]
```

Checks snapshots of a job (default: the newest).

- With `encryption.identity_file`, or for unencrypted snapshots, every artifact is decrypted and
  decompressed and both the stored and the original SHA-256 are checked (`MODE full`).
- Without the private key, only the stored size and SHA-256 are checked (`MODE stored`).
- Snapshots listed in `depends_on` must still exist.

Exits with code 1 if any check fails.

## `drill`

```
cistern drill --job NAME [--snapshot latest|ID|PREFIX] [--json]
cistern drills --job NAME [--json]
```

Runs a [restore drill](drills.md): restores the snapshot (default: the newest) into the job's
`drill.target` under `drill.restore_suffix`, runs the checks and drops what it created. Needs a
`drill` section in the job and the private key for encrypted snapshots. Prints one line per check
(`CHECK KIND STATUS EXPECTED ACTUAL MESSAGE`), the ten largest tables with their row counts and a
summary; `--json` prints the full result. Exits with code 1 when any check fails, like `verify`.
The drill takes the job lock and writes a run record, so it appears in the Admin UI.

`cistern drills` lists the past drills of a job from the run records, newest first.

## `restore`

```
cistern restore --job NAME [--snapshot latest|ID|PREFIX] [--set key=value]... [--yes]
```

Restores a snapshot through the job's driver. `--snapshot` accepts `latest` (default), a full ID or a
unique prefix of one. `--set` overrides job parameters for this restore only; the configuration file
is not changed. Typical overrides:

| Driver | Example |
| --- | --- |
| `postgres`, `mysql` | `--set restore_suffix=_restored`, `--set host=staging-db`, `--set databases=orders`, `--set clean=true` |
| `s3` | `--set restore_prefix=restored/`, `--set restore_bucket=other`, `--set only=images/`, `--set overwrite=true` |

On a terminal, Cistern asks you to type the job name. In scripts, pass `--yes`; without a terminal
and without `--yes`, restore refuses to run. Restores do not take the job lock and can run while a
backup of the same job is in progress.

## `prune`

```
cistern prune --job NAME [--dry-run] [--json]
cistern prune --all [--dry-run] [--json]
```

Applies the job's retention policy. `--dry-run` shows the decision for every snapshot without taking
the lock or deleting anything. Prune also removes incomplete snapshots (no manifest) older than one
hour. On an S3 repository with Object Lock, deletions create delete markers and locked versions stay
until their retention expires.

Prune also deletes run records (`_admin/runs/<job>/`) older than `run_history_days` (default 400),
but always keeps the newest 30 records of each operation; `0` keeps them forever. Unreadable run
records are kept. A run record that cannot be deleted (for example because of Object Lock) is logged
and skipped and never fails the prune. The summary line ends with `run records removed N` (`would
remove` with `--dry-run`); `--json` reports `run_records_removed`, the `stored_bytes` of each
snapshot and the `freed_bytes` of the removed ones.

## `check`

```
cistern check --job NAME
cistern check --all
```

Checks that the repository is reachable (and has Object Lock enabled when `object_lock` is
configured) and runs the driver's own checks: client tools in `PATH`, source reachable, client
version compatible with the server, configured databases present. Prints `job<TAB>OK` or
`job<TAB>FAIL<TAB>reason`.

## `daemon`

```
cistern daemon [--listen ADDR] [--grace DURATION]
```

Runs every job's `schedule` (backup) and `verify_schedule` (verify the newest snapshot) and serves
two listeners. The metrics listener (`--listen`, default `metrics.listen`, `:9464`):

| Path | Content |
| --- | --- |
| `/metrics` | Prometheus metrics |
| `/healthz` | `ok` |
| `/status` | JSON: jobs, snapshot counts, last success and failure, each scheduled task's next run and last result, running operations (`running`), the scheduler time zone (`timezone`) and whether the internal API is on (`api`) |

`/internal/*` answers `404` there. The internal API listener (`daemon.api_listen`, default `:9465`)
only runs with `daemon.api_token`, optionally over HTTPS (`daemon.api_tls`). Every path except
`/healthz` requires `Authorization: Bearer <api_token>`:

| Path | Content |
| --- | --- |
| `/healthz` | `ok` |
| `/status` | Same as on the metrics listener |
| `/internal/v1/runs` | `POST` starts a backup, verify or restore for `cistern server` (`202`, or `409` if the job is busy); `GET` lists runs (`?active=true`) |
| `/internal/v1/runs/{id}` | State, progress and the last 200 log lines of a run |

Different jobs run in parallel; when a run comes due while the previous run of the same job and
operation is still going, it is skipped and logged. Missed runs are not caught up after a restart.
A scheduled run is also skipped while an operation started from the Admin UI is running for the
same job. The daemon remembers runs for 24 hours. On SIGTERM or SIGINT the daemon stops both listeners,
waits up to `--grace` (default `5m`) for running jobs, then cancels them.

## `server`

```
cistern server [--listen ADDR] [--insecure-dev-login]
```

Serves the [Admin UI](admin-ui.md) and its API from the `server` section of the configuration. It
holds no private key: backups, verifies and restores are sent to the daemon at `server.daemon.url`.

| Flag | Description |
| --- | --- |
| `--listen` | Address, default `server.listen` (`127.0.0.1:8443`) |
| `--insecure-dev-login` | Enable `GET /api/v1/auth/dev-login` for local development; only with a loopback address and without `server.tls` |

Logs go to stderr; audit entries are also written to stdout, one JSON line each with
`"kind": "cistern.audit"`. `/healthz` answers `ok`.

## `keygen`

```
cistern keygen [--out FILE]
```

Creates an age X25519 key pair. With `--out`, writes the private key to `FILE` (mode 0600, refusing
to overwrite an existing file) and prints the public key on stdout. Without `--out`, prints the key
file to stdout. The format is the same as `age-keygen`.

## `drivers`, `version`

`drivers` lists the registered drivers. `version` prints the version, set at build time with
`-ldflags "-X main.version=v0.2.0"` (`make build` uses `git describe`).

## Environment variables

| Variable | Description |
| --- | --- |
| `CISTERN_CONFIG` | Configuration file |
| `CISTERN_LOG_FORMAT` | `json` for JSON logs on stderr; text otherwise (audit lines on stdout are always JSON) |
| `CISTERN_TMPDIR` | Directory for private temporary files (default: system temporary directory) |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` | S3 credentials when the configuration has none |
