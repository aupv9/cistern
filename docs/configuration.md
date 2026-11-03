# Configuration reference

Cistern reads one YAML file, chosen in this order:

1. `-c FILE` on the command line
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

- `${VAR}` fails if `VAR` is not set;
- `${VAR:-default}` uses `default` when `VAR` is unset **or empty**;
- values are substituted after parsing, so a variable can never inject YAML structure;
- values under `hooks` are not expanded (they are shell commands).

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
| `object_lock` | map | — | `s3`: `{ mode: GOVERNANCE \| COMPLIANCE, days: N }` — lock every artifact and manifest for `N` days. The bucket must have Object Lock enabled |

See [Repositories](repositories.md).

## `encryption`

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `recipients` | list | — | age public keys (`age1…`) every artifact is encrypted to |
| `recipients_file` | string | — | File with one public key per line (`#` comments allowed); combined with `recipients` |
| `identity_file` | string | — | age private key file; needed to verify fully, restore, and read the previous snapshot for incremental S3 backups |
| `allow_plaintext` | bool | `false` | Allow a configuration without recipients (artifacts stored unencrypted) |

Loading fails when no recipient is configured and `allow_plaintext` is not `true`.

## `compression`

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `level` | int | `6` | gzip level 1–9; `0` stores artifacts uncompressed |

## `metrics`

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `listen` | string | `:9464` | Address of the daemon's HTTP server (`/metrics`, `/healthz`, `/status`) |
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
| `hooks` | map | — | `pre_backup`, `post_backup`, `pre_restore`, `post_restore`. **Accepted but not executed yet** |
| `params` | map | `{}` | Driver parameters. A list value is joined with commas (`databases: [a, b]` → `a,b`) |

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

## Environment variables

| Variable | Used for |
| --- | --- |
| `CISTERN_CONFIG` | Configuration file |
| `CISTERN_LOG_FORMAT` | `json` for JSON logs (default: text) |
| `CISTERN_TMPDIR` | Directory for private temporary files such as MySQL option files (default: system temp dir) |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` | S3 credentials when none are configured |
