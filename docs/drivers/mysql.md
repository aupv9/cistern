# MySQL and MariaDB driver (`mysql`)

Dumps each database with `mysqldump` (MySQL) or `mariadb-dump` (MariaDB) in a single consistent
transaction, including routines, triggers and events; restores with the `mysql` or `mariadb` client.

## Requirements

- `mysqldump` and `mysql`, or `mariadb-dump` and `mariadb`, in `PATH`. With `client: auto` (default)
  Cistern uses the MySQL tools when both are present, otherwise the MariaDB tools. MariaDB 11 images
  no longer ship `mysqldump`.
- With a client and server of the same family, the client must be at least as new as the server.
- A user that can read every table and view and dump routines, triggers and events (`EVENT` privilege
  when `events: true`). `--no-tablespaces` is always used, so the `PROCESS` privilege is not needed.

## Parameters

| Parameter | Default | Description |
| --- | --- | --- |
| `host`, `port`, `user`, `password`, `socket` | client defaults | Connection |
| `ssl_mode` | — | MySQL client only (`REQUIRED`, `VERIFY_IDENTITY`, …) |
| `ssl_ca` | — | CA file |
| `client` | `auto` | `auto`, `mysql` or `mariadb` |
| `databases` | all | Comma-separated list or YAML list; empty means every database except `mysql`, `information_schema`, `performance_schema` and `sys` |
| `events` | `true` | Dump events |
| `gtid_purged` | — | `OFF`, `ON` or `AUTO`, passed as `--set-gtid-purged` (MySQL client only) |
| `restore_suffix` | — | Restore `db` as `db<suffix>` |
| `clean` | `false` | Allow restoring into an existing database: tables in the dump are dropped and recreated, other objects are left alone |

Connection settings are written to a 0600 option file in a 0700 directory under `CISTERN_TMPDIR`,
passed with `--defaults-extra-file` and deleted right after each command. They never appear on the
command line or in the environment.

## Artifacts

`db/<database>.sql`, produced with:

```
--single-transaction --quick --routines --triggers [--events] --hex-blob
--no-tablespaces --default-character-set=utf8mb4
```

The dump does **not** contain `CREATE DATABASE` or `USE`, so it can be restored under another name.
Its first statement, `ALTER DATABASE CHARACTER SET … COLLATE …;`, restores the original character
set and collation of the target database. With a MySQL 8 client and a MariaDB server,
`--column-statistics=0` is added automatically.

## Restore

For each database in the snapshot (or in `databases`), the target `<database><restore_suffix>` is
created unless it exists; an existing target needs `clean=true`. If the restore fails and Cistern
created the database, it is dropped again.

```sh
cistern restore --job shop --set restore_suffix=_restored --yes
```

## Limitations

- Views, routines and triggers that name the database explicitly (`` `db`.`table` ``) still point at
  the original database when restored under another name. Unqualified references (the usual case)
  follow the restored database.
- Only InnoDB tables are consistent under `--single-transaction`.
- Users and grants (`mysql.*`) are not dumped.
- Database names must match `[A-Za-z0-9._@=+-]+` and must not start with `-`.

## Manual recovery

```sh
mysql -e 'CREATE DATABASE shop'
age -d -i identity.txt artifacts/db/shop.sql.gz.age | gunzip | mysql shop
```
