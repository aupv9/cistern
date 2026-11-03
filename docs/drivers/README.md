# Drivers

A driver knows how to get data out of one kind of system and back in. Everything else — compression,
encryption, checksums, uploads, locking, retention, metrics — is handled by the engine, so every
driver produces the same snapshot format.

| Driver | Systems | Artifacts | Docs |
| --- | --- | --- | --- |
| `postgres` | PostgreSQL | `db/<database>.dump` (pg_dump custom format), `globals.sql` | [postgres.md](postgres.md) |
| `mysql` | MySQL, MariaDB | `db/<database>.sql` | [mysql.md](mysql.md) |
| `s3` | S3-compatible object storage | `objects/<hash>` per object, `index.json` | [s3.md](s3.md) |

Planned: MongoDB, Redis, etcd, Elasticsearch, Kafka, RabbitMQ, NATS JetStream, and `exec` for
anything else. Their names are already registered (`cistern drivers`) but they return
"not implemented".

## Common behaviour

- **Parameters** come from the job's `params` and can be overridden for one restore with
  `--set key=value`. Unknown parameters are rejected.
- **Secrets** never appear on command lines: drivers pass them through environment variables or
  0600 files in a private temporary directory.
- **External commands** run with the job timeout; on timeout the whole process group is terminated,
  and errors include the last 4 KiB of the command's stderr.
- **Restores never overwrite** existing data unless you opt in (`clean=true` or `overwrite=true`).
- **Manual recovery** is always possible with `age`, `gunzip` and the system's own tools; see
  [Disaster recovery](../disaster-recovery.md).

To write a new driver, see [Architecture](../architecture.md#writing-a-driver) and
[CONTRIBUTING.md](../../CONTRIBUTING.md#adding-a-driver).
