# Cistern documentation

## Using Cistern

- [Getting started](getting-started.md) — build, first backup, verify, restore, schedule
- [Configuration reference](configuration.md)
- [CLI reference](cli.md)
- [Drivers](drivers/) — [PostgreSQL](drivers/postgres.md), [MySQL/MariaDB](drivers/mysql.md), [S3](drivers/s3.md)
- [Repositories](repositories.md) — local, S3, Object Lock, least-privilege credentials

## Operating Cistern

- [Deployment](deployment.md) — daemon or one-shot, systemd, Docker, Kubernetes
- [Admin UI](admin-ui.md) — `cistern server`, roles, approvals, audit log, security model, local development
- [Observability](observability.md) — metrics, alerts, webhooks, logs, `/status`
- [Encryption and keys](encryption.md) — key management and threat model
- [Restore drills](drills.md) — automatic restores with table, row and SQL checks
- [Disaster recovery](disaster-recovery.md) — restoring without Cistern

## Developing Cistern

- [Architecture](architecture.md) — snapshot format, pipeline, retention, writing a driver
- [Contributing](../CONTRIBUTING.md)
