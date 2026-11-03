# Observability

Cistern reports every backup, verify, prune and restore through Prometheus metrics, structured logs
and webhooks.

## Metrics

All per-job metrics carry the label `cistern_job` (not `job`, which Prometheus and the Pushgateway
use themselves).

| Metric | Type | Meaning |
| --- | --- | --- |
| `cistern_backup_last_success_timestamp_seconds` | gauge | When the last successful backup finished |
| `cistern_backup_last_failure_timestamp_seconds` | gauge | When the last backup failed |
| `cistern_backup_failures_total` | counter | Failed backups since the process started |
| `cistern_backup_duration_seconds` | gauge | Duration of the last successful backup |
| `cistern_backup_last_size_bytes` | gauge | Stored size of the newest snapshot |
| `cistern_backup_previous_size_bytes` | gauge | Stored size of the snapshot before it |
| `cistern_snapshots` | gauge | Completed snapshots in the repository |
| `cistern_prune_removed_snapshots_total` | counter | Snapshots removed by prune since the process started |
| `cistern_prune_last_failure_timestamp_seconds` | gauge | When the last prune failed |
| `cistern_verify_last_success_timestamp_seconds` | gauge | Last successful verify or restore |
| `cistern_verify_last_failure_timestamp_seconds` | gauge | Last failed verify |
| `cistern_verify_failures_total` | counter | Artifacts that failed verification since the process started |
| `cistern_build_info{version}` | gauge | Always 1 (daemon only) |

### Getting metrics into Prometheus

| Mode | How |
| --- | --- |
| `cistern daemon` | Scrape `/metrics` on `metrics.listen` (`:9464`). On start, the daemon reads the newest snapshot of every job from the repository, so the last success survives restarts |
| One-shot commands (CronJob, systemd timer) | `metrics.pushgateway_url`: each command POSTs its job's metrics to the group `job=cistern, instance=<job>`. Or `metrics.textfile_dir`: each command writes `cistern_<job>.prom` for node_exporter's textfile collector |

A failed run never erases the last success: the Pushgateway's POST only replaces metrics with the
same name, and textfiles are merged with their previous content.

## Alerts

[deploy/k8s/prometheus-rules.yaml](../deploy/k8s/prometheus-rules.yaml) contains:

| Alert | Fires when |
| --- | --- |
| `CisternBackupFailing` | The last failure is newer than the last success (or there was never a success) |
| `CisternBackupStale` | No successful backup for 36 hours |
| `CisternBackupSizeDrop` | The newest snapshot is less than half the size of the previous one |
| `CisternVerifyStale` | No successful verify or restore for 8 days |
| `CisternVerifyFailing` | The last verify failure is newer than the last success |

The rules compare timestamps instead of using `increase()` on counters, because every CronJob run is
a new process whose counters start at zero.

## Webhooks

```yaml
notify:
  webhooks:
    - { url: "${SLACK_WEBHOOK_URL}", format: slack, on: [failure] }
    - { url: "https://api.telegram.org/bot${TG_TOKEN}/sendMessage", format: telegram, chat_id: "-1001234", on: [failure, success] }
    - { url: "https://hooks.example.com/cistern", format: json }
```

Sent after backup, verify, prune and restore when the outcome matches `on` (default `failure`).

| Format | Body |
| --- | --- |
| `slack`, `teams` | `{"text": "…"}` |
| `telegram` | `{"chat_id": "…", "text": "…"}` |
| `json` | the full event: `job`, `op`, `status`, `snapshot`, `error`, `host`, `started_at`, `finished_at`, `artifacts`, `stored_bytes`, `removed` |

Example text: `❌ cistern backup FAILED: job orders-db on backup-01 (3s)` followed by the error.
Each webhook is tried up to three times on network errors, 5xx and 429, with a 10-second timeout.
A failing webhook is logged and never fails the job; the path of the URL (which usually contains a
token) is removed from log messages.

## Logs

Logs go to stderr as text, or as JSON with `CISTERN_LOG_FORMAT=json`. Every message about a job
carries `job` and, where relevant, `snapshot`.

## `/status`

The daemon's `/status` returns JSON:

```json
{
  "version": "v0.2.0",
  "host": "backup-01",
  "jobs": [
    { "name": "orders-db", "driver": "postgres", "schedule": "0 2 * * *", "snapshots": 14,
      "last_success": "2026-11-02T02:00:41Z", "last_verify_success": "2026-11-01T03:00:12Z" }
  ],
  "tasks": [
    { "job": "orders-db", "op": "backup", "schedule": "0 2 * * *", "next": "2026-11-03T02:00:00+07:00",
      "running": false, "last_started": "…", "last_finished": "…", "skipped": 0 }
  ]
}
```
