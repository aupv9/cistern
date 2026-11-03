# PostgreSQL driver (`postgres`)

Dumps each database with `pg_dump` in custom format and the cluster's roles and tablespaces with
`pg_dumpall --globals-only`; restores with `pg_restore` and `psql`.

## Requirements

- `pg_dump`, `pg_dumpall`, `pg_restore` and `psql` in `PATH`.
- The client's major version must be **at least** the server's. `cistern check` and `cistern backup`
  refuse an older client, because `pg_dump` cannot dump a newer server. Both versions are recorded
  in the manifest (`source.version`, `source.extra.pg_dump_version`).
- A role that can connect to every database and read every object. `pg_dumpall --globals-only` needs
  a superuser unless `no_role_passwords: true` (useful on managed services that hide `pg_authid`);
  set `globals: false` to skip it.

## Parameters

| Parameter | Default | Description |
| --- | --- | --- |
| `host`, `port`, `user`, `password`, `sslmode` | libpq defaults | Connection, passed as `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGSSLMODE` |
| `maintenance_db` | `postgres` | Database used to list, create and drop databases |
| `databases` | all | Comma-separated list or YAML list; empty means every database that allows connections, except templates |
| `globals` | `true` | Dump roles and tablespaces to `globals.sql` |
| `no_role_passwords` | `false` | Add `--no-role-passwords` to `pg_dumpall` |
| `restore_suffix` | — | Restore `db` as `db<suffix>` |
| `clean` | `false` | Allow restoring into an existing database (`pg_restore --clean --if-exists`) |
| `no_owner` | `false` | `pg_restore --no-owner --no-privileges`, for targets without the original roles |
| `restore_globals` | `false` | Run `globals.sql` with `psql` before restoring databases |

## Artifacts

| Artifact | Content |
| --- | --- |
| `db/<database>.dump` | `pg_dump --format=custom` |
| `globals.sql` | `pg_dumpall --globals-only` (when `globals: true`) |

## Restore

For each database in the snapshot (or in `databases`):

1. The target is `<database><restore_suffix>`.
2. If it exists and `clean` is not set, the restore stops with an error. Otherwise it is created.
3. The dump is streamed into `pg_restore --exit-on-error`.
4. If the restore fails and Cistern created the database, the partial database is dropped so a retry
   does not fail with "already exists". An existing database restored with `clean=true` is left as is.

```sh
cistern restore --job orders-db --set restore_suffix=_restored --yes
cistern restore --job orders-db --set host=staging-db --set databases=orders --set no_owner=true --yes
```

## Limitations

- Database names must match `[A-Za-z0-9._@+-]+` (they become artifact names and command arguments).
- Dumps are logical; for very large databases or point-in-time recovery use pgBackRest or WAL-G.
- `pg_restore` reads the dump from stdin, so parallel restore (`-j`) is not used.

## Manual recovery

```sh
age -d -i identity.txt artifacts/db/orders.dump.gz.age | gunzip | pg_restore --exit-on-error -d orders
age -d -i identity.txt artifacts/globals.sql.gz.age | gunzip | psql -d postgres
```
