# Configuration reference

Cistern reads one YAML file, chosen in this order:

1. `-c FILE` or `--config FILE` on the command line
2. the `CISTERN_CONFIG` environment variable
3. `/etc/cistern/cistern.yaml`

The whole file is validated when it is loaded: unknown keys, duplicate keys, invalid values, unknown
repositories and invalid cron expressions are errors, reported with the path and line number:

```
jobs[0].parms (line 9): unknown key
```

A complete example lives in [examples/cistern.yaml](../examples/cistern.yaml).

## File format

Cistern accepts the subset of YAML that configuration files need:

- block mappings and sequences (indentation with spaces; tabs are rejected), including `- key: value`
- flow collections: `[a, b]` and `{ key: value }`, nested
- plain, `'single-quoted'` and `"double-quoted"` scalars; `#` comments

Not supported: multi-line strings (`|`, `>`), anchors and aliases (`&`, `*`), tags (`!`) and multiple
documents.

### Environment variables

`${VAR}` and `${VAR:-default}` are replaced in every value when the file is loaded:

- `${VAR}` fails if `VAR` is not set, except in sections the running process does not use (see
  below);
- `${VAR:-default}` uses `default` when `VAR` is unset **or empty**;
- values are substituted after parsing, so a variable can never inject YAML structure;
- values under `hooks` are not expanded (they are shell commands).

Each process only needs its own secrets. In these sections an unset `${VAR}` expands to an empty
string instead of failing:

| Process | Sections |
| --- | --- |
| `cistern server` | `jobs[].params`, `jobs[].restore_targets[].set`, `daemon`, `notify` |
| `cistern daemon` | `server` |
| every other command | `server`, `daemon`, `jobs[].restore_targets[].set` |

So the daemon does not need the UI session key, and the server does not need database passwords.

### Relative paths

`path` of local repositories, `ca_file`, `recipients_file`, `identity_file` and
`metrics.textfile_dir` are resolved relative to the directory of the configuration file.

### Durations

Go duration syntax (`90s`, `30m`, `6h`, `1h30m`) or a whole number of days (`7d`).

## `repositories`

A map of repository name to repository. At least one is required.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `type` | string | — | `local` or `s3` |
| `prefix` | string | `""` | Key prefix for everything Cistern writes |
| `path` | string | — | `local`: directory of the repository |
| `endpoint` | string | — | `s3`: `https://host[:port]` or `http://host[:port]` |
| `region` | string | `us-east-1` | `s3`: signing region |
| `bucket` | string | — | `s3`: bucket name |
| `path_style` | bool | `false` | `s3`: `https://endpoint/bucket/key` instead of `https://bucket.endpoint/key` (MinIO needs `true`) |
| `access_key_id`, `secret_access_key`, `session_token` | string | — | `s3`: credentials; when both key fields are empty, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `AWS_SESSION_TOKEN` are used |
| `part_size_mb` | int | `16` | `s3`: multipart part size and upload memory per artifact (minimum 5) |
| `ca_file` | string | — | `s3`: extra PEM CA bundle (added to the system pool) |
| `insecure_skip_verify` | bool | `false` | `s3`: skip TLS verification (test environments only) |
| `object_lock` | map | — | `s3`: `{ mode: GOVERNANCE \| COMPLIANCE, days: N }` — lock every artifact, manifest and run record for `N` days (audit entries and approvals use `server.audit_retention_days`). The bucket must have Object Lock enabled |

See [Repositories](repositories.md).

## `encryption`

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `recipients` | list | — | age public keys (`age1…`) every artifact is encrypted to |
| `recipients_file` | string | — | File with one public key per line (`#` comments allowed); combined with `recipients` |
| `identity_file` | string | — | age private key file; needed to verify fully, restore, and read the previous snapshot for incremental S3 backups |
| `allow_plaintext` | bool | `false` | Allow a configuration without recipients (artifacts stored unencrypted) |

Loading fails when no recipient is configured and `allow_plaintext` is not `true`. `cistern server`
ignores this section: it never encrypts or decrypts, so unset variables here do not matter to it.

## `compression`

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `level` | int | `6` | gzip level 1–9; `0` stores artifacts uncompressed |

## `metrics`

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `listen` | string | `:9464` | Address of the daemon's metrics listener (`/metrics`, `/healthz`, `/status`); never serves the internal API |
| `pushgateway_url` | string | — | One-shot commands push metrics here after each operation |
| `textfile_dir` | string | — | One-shot commands write `cistern_<job>.prom` here for node_exporter |

See [Observability](observability.md).

## `notify`

```yaml
notify:
  webhooks:
    - url: ${SLACK_WEBHOOK_URL}
      format: slack
      on: [failure]
```

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `webhooks[].url` | string | — | Webhook URL |
| `webhooks[].format` | string | `json` | `slack`, `teams`, `telegram` or `json` |
| `webhooks[].chat_id` | string | — | Telegram chat ID (required for `telegram`) |
| `webhooks[].on` | list | `[failure]` | `failure` and/or `success` |

## `scheduler`

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `timezone` | string | local time | IANA time zone used by schedules and retention periods |

## `defaults`

Values every job inherits unless it sets its own.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `repository` | string | the only repository, if there is one | Repository for jobs that do not name one |
| `timeout` | duration | `6h` | Maximum duration of one backup, verify or restore |
| `retention` | map | — | Retention policy (see below) |
| `prune_after_backup` | bool | `false` | Prune right after each successful backup |
| `run_history_days` | int | `400` | Prune deletes [run records](admin-ui.md#run-records) older than this many days, but always keeps the newest 30 per operation; `0` keeps them forever |

## `jobs`

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `name` | string | — | `[a-z0-9][a-z0-9._-]{0,62}`, unique, not `locks` |
| `driver` | string | — | `postgres`, `mysql` or `s3` (see [Drivers](drivers/)) |
| `repository` | string | `defaults.repository` | Repository name |
| `schedule` | string | — | Cron expression for backups (daemon only) |
| `verify_schedule` | string | — | Cron expression for verifying the newest snapshot (daemon only) |
| `timeout` | duration | `defaults.timeout` | |
| `retention` | map | `defaults.retention` | |
| `prune_after_backup` | bool | `defaults.prune_after_backup` | |
| `run_history_days` | int | `defaults.run_history_days` | |
| `hooks` | map | — | `pre_backup`, `post_backup`, `pre_restore`, `post_restore`. **Accepted but not executed yet** |
| `params` | map | `{}` | Driver parameters. A list value is joined with commas (`databases: [a, b]` → `a,b`) |
| `restore_targets` | list | one implicit target `original` | Where the [Admin UI](admin-ui.md#restore-targets) may restore to (see below) |
| `drill` | map | — | [Restore drill](drills.md) of the job (postgres, mysql and s3 only; see below) |

### Restore targets

```yaml
restore_targets:
  - { name: staging, description: "staging-db:5432", production: false, set: { host: staging-db, password: "${STAGING_PGPASSWORD}" } }
  - { name: production, production: true }
```

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `name` | string | — | `[a-z0-9][a-z0-9._-]{0,62}`, unique within the job |
| `description` | string | — | Shown in the UI; must not contain secrets |
| `production` | bool | `true` | Restores into it need an approval |
| `set` | map | `{}` | Parameter overrides, like `cistern restore --set k=v` |

A job without `restore_targets` has one target, `original`: production, no overrides. The UI can
only choose among these targets plus `restore_suffix` (for `postgres` and `mysql`), never a free-form
host. The CLI does not use restore targets.

### Drill

```yaml
drill:
  schedule: "0 4 * * 0"
  target: scratch
  checks:
    - { name: orders exist, database: orders, sql: "SELECT count(*) FROM orders", min: 1 }
```

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `schedule` | string | — | Cron expression for drills (daemon only); without it drills run only on demand |
| `target` | string | — | A restore target of this job with `production: false` (required) |
| `restore_suffix` | string | `_drill` | Suffix of the drill databases (s3: drill prefix without the `_`); `^_[a-z0-9_]{1,30}$` |
| `max_age_days` | int | `35` | Health `drill_failing` and alert `drill_stale` after this many days without a passed drill |
| `keep` | bool | `false` | Keep the drill databases or objects after the checks; the next drill replaces them |
| `checks` | list | `[]` | SQL checks, postgres and mysql only: `name` (unique), `sql` (one value), `database` (original name; required with several databases), and `min`/`max` (numbers) or `equals` (string) |

See [Restore drills](drills.md) for what each check does and the safety marker.

### Retention

```yaml
retention: { last: 3, hourly: 0, daily: 7, weekly: 4, monthly: 6, yearly: 1 }
```

Each rule keeps the newest snapshot of each of the most recent N periods (hours, days, ISO weeks,
months, years) in `scheduler.timezone`; `last` keeps the N newest snapshots. A snapshot is kept if
any rule keeps it, and snapshots that a kept snapshot depends on are always kept. Counts must not be
negative and must not all be zero. A job without retention is never pruned.

### Schedules

Five-field cron expressions (`minute hour day-of-month month day-of-week`) with `*`, values, ranges
(`1-5`), steps (`*/15`, `9-17/4`), lists (`1,15`) and English month and day names (`jan`, `mon`).
Day of week `0` and `7` are both Sunday. As in classic cron, when both day-of-month and day-of-week
are restricted, either one matching is enough.

Macros: `@yearly` (`@annually`), `@monthly`, `@weekly`, `@daily` (`@midnight`), `@hourly`, and
`@every <duration>` (for example `@every 6h`).

## `daemon`

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `api_token` | string | — | Enables the internal API (`/internal/v1/runs`) used by `cistern server`; at least 32 characters. Without it the API is off and nothing listens on `api_listen` |
| `api_listen` | string | `:9465` when `api_token` is set | Address of the internal API listener (`/internal/v1/*`, `/status` with the token, `/healthz`). Must differ from `metrics.listen`; the metrics listener answers `404` for `/internal/*` |
| `api_tls.cert_file`, `api_tls.key_file` | string | — | Serve the internal API over HTTPS (TLS 1.2+) with this certificate and key; the server then uses an `https://` `daemon.url` |

## `server`

Configuration of `cistern server`, the [Admin UI](admin-ui.md). Ignored by every other command.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `name` | string | `cistern` | Deployment label shown in the UI, for example `production` |
| `listen` | string | `127.0.0.1:8443` | Address to listen on (`--listen` overrides it) |
| `public_url` | string | — | `https://host[:port]` users open, without a path; required except in dev mode. The OIDC redirect URI is `<public_url>/api/v1/auth/callback` |
| `tls.cert_file`, `tls.key_file` | string | — | Serve HTTPS with this certificate and key (TLS 1.2+) |
| `tls.upstream` | bool | `false` | Declare that a proxy terminates TLS; the server speaks HTTP. `X-Forwarded-For` is only used with `trusted_proxies` |
| `trusted_proxies` | list | `[]` | CIDRs or addresses of reverse proxies. When the connection comes from one of them, the client address is the right-most `X-Forwarded-For` entry that is not itself a trusted proxy; otherwise it is the connection's address. Used for the sign-in rate limit and the audit log |
| `session_key` | string | — | Encrypts the session and sign-in cookies; at least 32 bytes, from an environment variable. Required except in dev mode |
| `session_ttl` | duration | `8h` | Absolute session lifetime, between `1m` and `8h` |
| `idle_timeout` | duration | `30m` | Sign out after this much inactivity, at most `session_ttl` |
| `state_repository` | string | `defaults.repository` | Repository for the audit log and approvals |
| `audit_retention_days` | int | `400` | Object Lock retain-until for audit entries and approval objects on S3 repositories with `object_lock`; at least 365 |
| `oidc.issuer` | string | — | Issuer URL; discovery is read from `<issuer>/.well-known/openid-configuration` |
| `oidc.client_id` | string | — | Client ID |
| `oidc.client_secret` | string | — | Client secret; empty for a public client |
| `oidc.scopes` | list | `[openid]` | Extra scopes; `openid` is always requested |
| `oidc.groups_claim` | string | `groups` | ID token claim with the user's groups (list or string) |
| `oidc.email_claim` | string | `email` | ID token claim with the email |
| `oidc.name_claim` | string | `name` | ID token claim with the display name |
| `roles` | list | — | `{ group, roles }` entries; `roles` from `viewer`, `operator`, `restorer`, `approver`, `admin`. At least one is required except in dev mode |
| `daemon.url` | string | — | Base URL of the daemon's internal API (`daemon.api_listen`), for example `http://cistern:9465` or `https://cistern:9465`. Without it the UI is read-only |
| `daemon.token` | string | — | Same value as the daemon's `daemon.api_token`; required with `daemon.url` |
| `daemon.ca_file` | string | — | PEM CA bundle that verifies the daemon's `api_tls` certificate (instead of the system pool); only with an `https://` `daemon.url` |
| `rate_limit.login_per_minute` | int | `10` | Requests per minute to `/api/v1/auth/login`, `/auth/callback` and `/auth/dev-login` per client address; one sign-in uses two (login and callback) |
| `rate_limit.write_per_minute` | int | `30` | Non-GET API requests per minute per user |
| `rate_limit.read_per_minute` | int | `600` | GET API requests per minute per user |
| `prune.enabled` | bool | `false` | Let admins run a prune from the UI. The daemon then needs repository credentials that may delete; keep it off when only a separate prune job holds them. The prune preview works either way |

Exactly one of `tls.cert_file`/`tls.key_file` and `tls.upstream: true` is required unless the server
runs with `--insecure-dev-login`, which in turn needs a loopback `listen` and no `tls`.

Rate limits are token buckets: a client may send up to the per-minute value at once, and the budget
refills evenly over a minute. `0` disables a limit. A request over the limit gets `429` with code
`rate_limited` and a `Retry-After` header in seconds. Buckets live in memory, per server process.

## Environment variables

| Variable | Used for |
| --- | --- |
| `CISTERN_CONFIG` | Configuration file |
| `CISTERN_LOG_FORMAT` | `json` for JSON logs (default: text) |
| `CISTERN_TMPDIR` | Directory for private temporary files such as MySQL option files (default: system temp dir) |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` | S3 credentials when none are configured |
