# Getting started

This walkthrough builds Cistern, backs up a PostgreSQL database into a local repository, verifies
and restores it, and then runs the same job on a schedule.

## 1. Build

Cistern needs Go 1.24 or newer to build and has no runtime dependencies besides the client tools of
the systems you back up.

```sh
make build                                   # bin/cistern for this machine
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -o cistern ./cmd/cistern          # static binary for a Linux server
```

The PostgreSQL driver needs `pg_dump`, `pg_dumpall`, `pg_restore` and `psql` in `PATH`, with a major
version at least as new as the server's.

## 2. Create an encryption key

```sh
cistern keygen --out identity.txt > recipient.txt
```

- `recipient.txt` holds the **public key** (`age1…`). Every machine that makes backups needs it.
- `identity.txt` holds the **private key** (`AGE-SECRET-KEY-1…`, file mode 0600). Store it outside the
  backup system — a password manager, a vault, or an offline copy. It is only needed to verify
  backups fully and to restore them.

The files use the same format as `age-keygen`, so keys made with either tool work with both. See
[Encryption and keys](encryption.md).

## 3. Configure

```yaml
# cistern.yaml
repositories:
  local:
    type: local
    path: repo                       # relative paths are resolved against this file

encryption:
  recipients_file: recipient.txt
  identity_file: identity.txt

jobs:
  - name: orders-db
    driver: postgres
    retention: { last: 3, daily: 7, weekly: 4 }
    params:
      host: localhost
      user: backup
      password: ${PGPASSWORD}        # expanded from the environment when the file is loaded
      databases: [orders]
```

Cistern finds its configuration in `-c FILE`, then `$CISTERN_CONFIG`, then
`/etc/cistern/cistern.yaml`. Unknown keys and invalid values are rejected when the file is loaded.
The [configuration reference](configuration.md) lists every option.

## 4. Check and back up

```sh
export CISTERN_CONFIG=$PWD/cistern.yaml PGPASSWORD=...
cistern check --job orders-db
cistern backup --job orders-db
```

`check` confirms that the client tools exist, the database is reachable, the client is new enough
for the server and the repository is accessible. `backup` prints the job and the new snapshot ID:

```
orders-db   20261102T072004Z-3434
```

## 5. Inspect and verify

```sh
cistern list --job orders-db
```

```
SNAPSHOT               FINISHED             DURATION  ARTIFACTS  STORED   SOURCE
20261102T072004Z-3434  2026-11-02 07:20:05  0s        3          4.8 MiB  postgres 16.15
```

```sh
cistern verify --job orders-db              # newest snapshot
cistern verify --job orders-db --snapshot all
```

With `identity_file` configured, `verify` decrypts every artifact and checks both the original and
the stored SHA-256. Without it, it checks the stored size and checksum only.

## 6. Restore

Restores never overwrite an existing database unless you ask for it. Restore next to the original
first:

```sh
cistern restore --job orders-db --set restore_suffix=_restored --yes
```

This creates `orders_restored`. `--set key=value` overrides any job parameter for this restore only,
for example `--set host=staging-db` to restore onto another server or `--set databases=orders` to
restore a single database. Without `--yes`, Cistern asks you to type the job name to confirm.

## 7. Apply retention

```sh
cistern prune --job orders-db --dry-run
cistern prune --job orders-db
```

Retention keeps the newest snapshot in each period (see [Architecture](architecture.md#retention)).
Set `prune_after_backup: true` to prune right after every successful backup.

## 8. Run on a schedule

Add a schedule to the job and start the daemon:

```yaml
jobs:
  - name: orders-db
    driver: postgres
    schedule: "0 2 * * *"            # backups at 02:00
    verify_schedule: "@weekly"       # verify the newest snapshot weekly
    prune_after_backup: true
    ...
scheduler:
  timezone: Asia/Ho_Chi_Minh
```

```sh
cistern daemon
curl -s localhost:9464/status
curl -s localhost:9464/metrics | grep cistern_
```

For Kubernetes CronJobs, systemd timers and containers, see [Deployment](deployment.md). To alert on
failed or stale backups, see [Observability](observability.md).

## Next steps

- Store backups in S3 with Object Lock: [Repositories](repositories.md)
- Back up MySQL/MariaDB or an S3 bucket: [Drivers](drivers/)
- Practise a restore without Cistern: [Disaster recovery](disaster-recovery.md)
