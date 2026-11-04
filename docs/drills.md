# Restore drills

A verify proves that a snapshot is intact: every artifact decrypts, decompresses and matches its
checksums. It does not prove that the dump inside can be loaded. A **restore drill** does: it
restores a snapshot into a non-production restore target under a drill suffix, checks the result,
and drops what it created. A passed drill is evidence that the backup can actually be restored, and
how long that took.

Drills are supported by the `postgres`, `mysql` (MySQL and MariaDB) and `s3` drivers. Other drivers
reject a `drill` section in the configuration, and starting a drill for them answers `422`.

## Configuration

```yaml
jobs:
  - name: orders-db
    driver: postgres
    params: { host: db-1, user: backup, password: "${PGPASSWORD}", databases: [orders, users] }
    restore_targets:
      - { name: scratch, description: "drill server", production: false, set: { host: drill-db, password: "${DRILL_PGPASSWORD}", no_owner: "true" } }
    drill:
      schedule: "0 4 * * 0"        # weekly; omit to run drills only on demand
      target: scratch               # must have production: false
      restore_suffix: _drill        # default _drill
      max_age_days: 35              # default 35
      keep: false                   # default false
      checks:
        - { name: orders exist, database: orders, sql: "SELECT count(*) FROM orders", min: 1000 }
        - { name: no future orders, database: orders, sql: "SELECT count(*) FROM orders WHERE created_at > now()", equals: "0" }
```

| Key | Default | Description |
| --- | --- | --- |
| `schedule` | — | Cron expression; the daemon runs the drill like a scheduled verify and skips it while the job has another operation running |
| `target` | — | A restore target of the job with `production: false`; its `set` overrides apply (host, credentials, `no_owner`, …) |
| `restore_suffix` | `_drill` | Must start with `_` and match `^_[a-z0-9_]{1,30}$` |
| `max_age_days` | `35` | The job's health becomes `drill_failing` and the alert `drill_stale` fires when no drill passed for this many days |
| `keep` | `false` | Leave the drill databases (or objects) in place after the checks, for inspection. The next drill replaces them |
| `checks` | `[]` | SQL checks (postgres and mysql only), see below |

A drill never uses `clean`, `restore_globals` or `overwrite`, even when the target sets them, and it
does not restore roles (`globals.sql`).

Run one now with `cistern drill --job orders-db [--snapshot S]`, from the job page of the
[Admin UI](admin-ui.md#restore-drills) (restorer), or let the daemon run it on `schedule`. A drill
takes the job lock like a backup, writes a run record (operation `drill`) with the full result,
updates the drill metrics and sends the webhooks configured for its outcome.

## The safety marker

A drill only ever drops what it created itself:

| Driver | Drill copy | Marker |
| --- | --- | --- |
| postgres | database `<db><suffix>` per database of the snapshot | `COMMENT ON DATABASE … IS 'cistern drill <job>'` |
| mysql | database `<db><suffix>` per database of the snapshot | table `_cistern_drill (job, created_at)` in the drill database |
| s3 | objects under `<restore_prefix><suffix without _>/` in the target's `restore_bucket` (default: the source bucket) | object `.cistern-drill` containing `cistern drill <job>` |

Before restoring, the drill looks for each drill database (or the drill prefix). If it exists and
carries the marker of the same job, it is a leftover of an earlier drill (`keep: true` or a failed
cleanup) and is dropped. If it exists **without** the marker, or with another job's marker, the drill
fails at once and leaves it untouched. The cleanup checks the marker again before dropping.

For the s3 driver, the drill prefix is the target's `restore_prefix` followed by the suffix without
its underscore: `restore_prefix: drills/` and the default suffix give `drills/drill/`. A drill
refuses to run when that prefix overlaps the backed-up prefix in the same bucket, so set
`restore_bucket` or a `restore_prefix` outside the source on the drill target.

## Checks

Each drill reports a list of checks, each `passed`, `failed` or `skipped`. The drill passes when no
check failed.

| Kind | postgres | mysql / MariaDB | s3 |
| --- | --- | --- | --- |
| `restore` | `pg_restore --exit-on-error` exited 0 | the client loaded the dump without error | every object was uploaded |
| `tables` | tables in the restored database equal the `TABLE` entries in the dump's table of contents (`pg_restore -l`) | base tables equal the `CREATE TABLE` statements seen in the dump | — |
| `rows` | exact `count(*)` of every table, 10-minute statement timeout | `SELECT COUNT(*)` of every table | — |
| `objects` | — | — | object count and total bytes under the drill prefix equal the snapshot's `index.json` |
| `sql` | each configured check | each configured check | — |
| `cleanup` | drill databases dropped (`skipped` with `keep: true`) | same | drill prefix emptied |

`restore`, `tables` and `rows` appear once per database. When a restore fails, the remaining checks
are skipped, but the cleanup still runs. Row counts are reported per table, largest first (at most
500 in the result; `total_tables` and `total_rows` cover all of them).

### SQL checks

| Key | Description |
| --- | --- |
| `name` | Unique within the drill |
| `sql` | One statement that returns exactly one row with one column. It runs in a read-only transaction in the drill database, with `psql` or the `mysql`/`mariadb` client |
| `database` | The **original** database name; required when the snapshot has several databases |
| `min`, `max` | The value must be a number within these bounds |
| `equals` | The value must equal this string, or this number when both are numeric |

Without `min`, `max` or `equals` a check passes when the query returns one value. `NULL` is
reported as `NULL`. Good checks compare something the application relies on: a row count that only
grows, the newest timestamp being recent, a sum that must not be zero.

## Metrics and alerts

| Metric | Meaning |
| --- | --- |
| `cistern_drill_last_success_timestamp_seconds` | When the last passed drill finished |
| `cistern_drill_last_failure_timestamp_seconds` | When the last drill failed |

The daemon restores both from the run records when it starts.
[prometheus-rules.yaml](../deploy/k8s/monitoring/prometheus-rules.yaml) adds `CisternDrillFailing`
(the last failure is newer than the last success) and `CisternDrillStale` (no passed drill for 35
days). The Admin UI shows the same as job health `drill_failing` and the alerts `drill_failing`
(the newest drill failed) and `drill_stale` (no passed drill within `max_age_days`, only for jobs with
a `drill` section).

## Recommended schedule

- Weekly for critical databases, monthly for the rest; keep `max_age_days` a little above the
  interval (35 days for a monthly drill) so one skipped run does not alert.
- Run drills outside the backup window, on a server sized like production, so the drill duration is
  a realistic restore time (RTO).
- Point the drill target at a separate server when the databases are large: a drill needs as much
  space as the restored data.
- Keep at least one SQL check per database that would catch an empty or truncated restore.
