# Deployment

Cistern is one binary with two ways to run:

| Mode | Command | Scheduling | Metrics |
| --- | --- | --- | --- |
| **Daemon** | `cistern daemon` | `schedule` and `verify_schedule` in the configuration | scrape `/metrics` |
| **One-shot** | `cistern backup --job J` (and `verify`, `prune`) | Kubernetes CronJob, systemd timer, cron | Pushgateway or node_exporter textfile |

Both use the same configuration and the same repository, so you can switch modes or mix them (for
example, a daemon for backups and a separate CronJob with prune credentials).

> **Status:** the files in [deploy/](../deploy/) are starting points and have not been verified end
> to end yet. In particular the [Dockerfile](../Dockerfile) does not install the PostgreSQL and MySQL
> clients yet, so the `postgres` and `mysql` drivers do not work in that image.

## What the host needs

- The `cistern` binary (static when built with `CGO_ENABLED=0`).
- The client tools of every driver you use, in `PATH` (see [Drivers](drivers/)). For PostgreSQL, the
  client major version must be at least the server's — pin it.
- Network access to the sources and the repository; HTTPS outbound for S3 and webhooks.
- A writable `CISTERN_TMPDIR` (default: the system temp directory) for 0600 option files.
- The public key; the private key only on hosts that verify, restore or run incremental S3 backups.

Cistern does not need root. Run it as a dedicated user with a read-only root filesystem where possible.

## Daemon

### systemd

[deploy/systemd/cistern.service](../deploy/systemd/cistern.service) runs the daemon as user
`cistern` with `ProtectSystem=strict` and `NoNewPrivileges`. Secrets can go into
`/etc/cistern/cistern.env` (`EnvironmentFile`), referenced as `${VAR}` in the configuration.

```sh
install -m 0755 cistern /usr/local/bin/
install -d -o cistern /var/lib/cistern
systemctl enable --now cistern
```

### Docker

[deploy/docker/docker-compose.yaml](../deploy/docker/docker-compose.yaml) runs the daemon as UID
10001 with a read-only root filesystem, a tmpfs `/tmp` and a `/healthz` health check.

```sh
docker build -t cistern:dev .
docker compose -f deploy/docker/docker-compose.yaml up -d
```

On SIGTERM the daemon waits up to `--grace` (default 5 minutes) for running jobs; give the container
a longer stop timeout than that.

## One-shot

### Kubernetes CronJob

[deploy/k8s](../deploy/k8s/) contains a kustomization with a backup CronJob
(`concurrencyPolicy: Forbid`, non-root, read-only root filesystem, a 20 GiB `emptyDir` as
`CISTERN_TMPDIR`) and the `PrometheusRule` with the alerts from [Observability](observability.md).

Recommended layout per job:

| CronJob | Command | Credentials and secrets |
| --- | --- | --- |
| backup | `cistern backup --job J` | source credentials, repository write credentials, public key |
| verify | `cistern verify --job J` | repository read credentials, **private key** |
| prune | `cistern prune --job J` | repository credentials allowed to delete |

Set `metrics.pushgateway_url` so each run reports its result. A NetworkPolicy that denies inbound
traffic is recommended; Cistern needs no inbound connections in one-shot mode.

### systemd timer or cron

```ini
# /etc/systemd/system/cistern-orders.service
[Service]
Type=oneshot
User=cistern
Environment=CISTERN_CONFIG=/etc/cistern/cistern.yaml
ExecStart=/usr/local/bin/cistern backup --job orders-db

# /etc/systemd/system/cistern-orders.timer
[Timer]
OnCalendar=*-*-* 02:00
Persistent=true
[Install]
WantedBy=timers.target
```

Use `metrics.textfile_dir` with node_exporter's textfile collector to export the results.

## Upgrading

Snapshots carry `format: 1` in their manifest. A newer Cistern reads older snapshots of the same
format; the format will not change without a version bump and release notes.
