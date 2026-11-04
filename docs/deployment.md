# Deployment

Cistern is one binary with two ways to run:

| Mode | Command | Scheduling | Metrics |
| --- | --- | --- | --- |
| **Daemon** | `cistern daemon` | `schedule` and `verify_schedule` in the configuration | scrape `/metrics` |
| **One-shot** | `cistern backup --all` (and `verify --job J`, `prune --all`) | Kubernetes CronJob, systemd timer, cron | Pushgateway or node_exporter textfile |

Both use the same configuration and the same repository, so you can switch modes or mix them (for
example, a daemon for backups and a separate CronJob with prune credentials).

The optional [Admin UI](admin-ui.md) is a third process, `cistern server`. It holds no private key
and sends every operation to the daemon, so it needs daemon mode.

## Container image

The [Dockerfile](../Dockerfile) builds a multi-architecture image (`linux/amd64`, `linux/arm64`):

| | |
| --- | --- |
| Base | `oraclelinux:9-slim` |
| Cistern | static binary with embedded time zone data and the Admin UI, `/usr/local/bin/cistern` |
| PostgreSQL client | `pg_dump`, `pg_dumpall`, `pg_restore`, `psql` from the PGDG repository, major version `PG_MAJOR` (default `16`) |
| MySQL client | `mysqldump`, `mysql` 8.4 LTS from the MySQL repository; also used for MariaDB servers |
| User | UID/GID `10001`, no shell login, `HOME=/tmp` |
| Defaults | `ENTRYPOINT ["cistern"]`, `CMD ["daemon"]`, `CISTERN_CONFIG=/etc/cistern/cistern.yaml`, `CISTERN_TMPDIR=/tmp`, ports `9464` (daemon) and `8443` (server) |

The UI is built in a first stage from `node:22.23.4-alpine`, pinned by digest, with `npm ci` and
`npm run build`, and embedded with `go build -tags ui`. Packages are installed with GPG verification against the MySQL and PGDG signing keys. The image
works with a read-only root filesystem as long as `/tmp` is writable.

Published images are at `ghcr.io/aupv9/cistern`, built by CI only after every test has passed:

| Tag | Points to |
| --- | --- |
| `vX.Y` and `latest` | a release |
| `main` | the newest commit on `main` |
| `sha-<commit>` | a specific commit |

```sh
docker pull ghcr.io/aupv9/cistern:main
```

To build your own, for example with another PostgreSQL major:

```sh
docker build --build-arg VERSION=v0.2.0 -t registry.example.com/cistern:v0.2.0 .
docker build --build-arg PG_MAJOR=17 -t registry.example.com/cistern:v0.2.0-pg17 .
```

Build the image with the PostgreSQL major version of your newest server: `pg_dump` must be at least
as new as the server (Cistern refuses older clients), and `pg_restore` from a newer major can fail
against an older server. If your fleet spans several majors, build one image per major.

`make e2e-image` builds the image and runs backup, verify and restore against PostgreSQL, MySQL and
MariaDB with it, as UID 10001 on a read-only root filesystem.

## Kubernetes

[deploy/k8s](../deploy/k8s/) contains four kustomizations:

| Directory | Contents |
| --- | --- |
| `base` | Daemon: Deployment (1 replica, `Recreate`), Service `cistern` with ports `metrics` (9464) and `api` (9465), NetworkPolicy (9464 from anywhere for scrapers, 9465 only from `cistern-server` pods), ServiceAccount without a token |
| `cronjob` | One-shot mode: CronJobs `cistern-backup` (`backup --all`) and `cistern-prune` (`prune --all`, separate credentials) |
| `monitoring` | `PrometheusRule` with the alerts from [Observability](observability.md) (requires the Prometheus Operator) |
| `server` | Admin UI: Deployment `cistern-server` (no keys secret), Service `cistern-server:8443`, NetworkPolicy, ServiceAccount without a token |

Every pod runs as UID 10001 with `runAsNonRoot`, a read-only root filesystem, all capabilities
dropped, `seccompProfile: RuntimeDefault` and no service account token. `/tmp` is a 20 GiB
`emptyDir` (64 MiB for `cistern-server`, which never stages backup data). The daemon has `/healthz` probes and `terminationGracePeriodSeconds: 330`, longer than
`--grace 5m`, so a running backup can finish when the pod is stopped.

The manifests expect these objects, which you create:

| Object | Content | Mounted as |
| --- | --- | --- |
| ConfigMap `cistern-config` | `cistern.yaml` | `/etc/cistern/cistern.yaml` |
| Secret `cistern-env` | environment variables referenced as `${VAR}` in the configuration (passwords, S3 keys, `CISTERN_RECIPIENT`) | environment |
| Secret `cistern-keys` (daemon only, optional) | `identity.txt` | `/etc/cistern-keys/identity.txt` |
| Secret `cistern-prune-env` (prune CronJob) | environment for prune, with repository credentials allowed to delete | environment |
| Secret `cistern-daemon-token` (with the Admin UI) | key `token`: the shared daemon API token, at least 32 characters | `CISTERN_DAEMON_TOKEN` in the daemon (optional) and the server |
| Secret `cistern-server-env` (Admin UI) | `CISTERN_SESSION_KEY`, the OIDC client secret and the repository credentials the server needs; not `CISTERN_RECIPIENT`, since the server ignores `encryption` | environment of the server |

The daemon has no delete permission in this layout, which is why pruning from the Admin UI
(`server.prune.enabled`) is off by default: turning it on means giving the daemon the credentials of
`cistern-prune-env`. [Restore drills](drills.md) run in the daemon too, so it needs network access
and credentials for the drill target (through `restore_targets[].set`, referenced from `cistern-env`).

A minimal overlay:

```yaml
# kustomization.yaml
namespace: backup
resources:
  - github.com/aupv9/cistern//deploy/k8s/base?ref=main
configMapGenerator:
  - name: cistern-config
    files: [cistern.yaml]
images:
  - name: cistern
    newName: ghcr.io/aupv9/cistern
    newTag: main
```

```sh
cistern keygen --out identity.txt > recipient.txt
kubectl -n backup create secret generic cistern-keys --from-file=identity.txt
kubectl -n backup create secret generic cistern-env \
  --from-literal=CISTERN_RECIPIENT="$(cat recipient.txt)" --from-literal=PGPASSWORD=...
kubectl apply -k .
```

Put the public key in the configuration with `recipients: ["${CISTERN_RECIPIENT}"]`, so pods
without the private key can still encrypt. Mount `cistern-keys` only where you verify, restore or
run incremental S3 backups; the daemon does all three, the backup CronJob does not need it.

For a local repository, add a PersistentVolumeClaim and mount it into the pods, as the test overlay
in [scripts/e2e/k8s](../scripts/e2e/k8s/) does; for production, prefer an
[S3 repository](repositories.md).

Restore from inside the cluster with:

```sh
kubectl -n backup exec deploy/cistern -- cistern restore --job orders-db --set restore_suffix=_restored --yes
```

### Admin UI

`deploy/k8s/server` runs `cistern server --listen :8443` with the same hardening as the daemon and
the same `cistern-config` ConfigMap, but **without** the `cistern-keys` Secret. Configure the server
behind an ingress that terminates TLS:

```yaml
daemon:
  api_token: ${CISTERN_DAEMON_TOKEN}
server:
  public_url: https://backup.example.com
  tls: { upstream: true }
  session_key: ${CISTERN_SESSION_KEY}
  daemon: { url: "http://cistern:9465", token: "${CISTERN_DAEMON_TOKEN}" }
  trusted_proxies: ["10.0.0.0/8"]   # the ingress controller's pod network
```

```sh
kubectl -n backup create secret generic cistern-daemon-token --from-literal=token="$(openssl rand -hex 32)"
kubectl -n backup create secret generic cistern-server-env \
  --from-literal=CISTERN_SESSION_KEY="$(openssl rand -hex 32)" \
  --from-literal=CISTERN_OIDC_CLIENT_SECRET=... --from-literal=S3_ACCESS_KEY_ID=... --from-literal=S3_SECRET_ACCESS_KEY=...
```

Add `deploy/k8s/server` to your overlay next to `base` and route an Ingress to Service
`cistern-server` port `8443`. The NetworkPolicy only admits traffic from the `ingress-nginx`
namespace; patch it for your ingress controller. The server reaches the daemon on `cistern:9465`,
the repository and the identity provider.

The daemon serves two listeners. Port 9464 (`metrics`) has `/metrics`, `/healthz` and `/status`
and stays open to Prometheus. Port 9465 (`api`, `daemon.api_listen`) has the internal run API; it
only listens when `daemon.api_token` is set, checks the token on every request, and the base
NetworkPolicy only admits pods labeled `app.kubernetes.io/name: cistern-server` to it. The
container always declares port 9465; restart the daemon after creating the token Secret. To
encrypt the hop, set `daemon.api_tls` (`cert_file`, `key_file`, for example from a cert-manager
Secret mounted into the daemon), use an `https://` `server.daemon.url` and point
`server.daemon.ca_file` at the issuing CA.

Set `server.trusted_proxies` to the addresses of your ingress controller pods. Only then does the
server take the client address from `X-Forwarded-For`, which the sign-in rate limit and the audit
log use; without it every request appears to come from the ingress controller and shares one
sign-in budget.

With more than one server replica, all replicas must share `server.session_key` and the state
repository. Sign-outs reach the other replicas within 30 seconds.

`make e2e-k8s` (or `scripts/e2e-k8s.sh`) creates a throwaway k3d cluster, deploys `base` and
`cronjob` with PostgreSQL, and checks the pod hardening, scheduled backups and verifies, metrics,
both CronJobs, a restore and the graceful shutdown settings. CI runs it on every push.

## Docker

[deploy/docker/docker-compose.yaml](../deploy/docker/docker-compose.yaml) runs the daemon as UID
10001 with a read-only root filesystem, a tmpfs `/tmp` and a `/healthz` health check.

```sh
docker build -t cistern:dev .
docker compose -f deploy/docker/docker-compose.yaml up -d
```

On SIGTERM the daemon waits up to `--grace` (default 5 minutes) for running jobs; give the container
a longer stop timeout (`stop_grace_period`) than that.

## systemd

[deploy/systemd/cistern.service](../deploy/systemd/cistern.service) runs the daemon as user
`cistern` with `ProtectSystem=strict` and `NoNewPrivileges`. Secrets can go into
`/etc/cistern/cistern.env` (`EnvironmentFile`), referenced as `${VAR}` in the configuration.

```sh
install -m 0755 cistern /usr/local/bin/
install -d -o cistern /var/lib/cistern
systemctl enable --now cistern
```

For one-shot runs, use a timer:

```ini
# /etc/systemd/system/cistern-backup.service
[Service]
Type=oneshot
User=cistern
Environment=CISTERN_CONFIG=/etc/cistern/cistern.yaml
ExecStart=/usr/local/bin/cistern backup --all

# /etc/systemd/system/cistern-backup.timer
[Timer]
OnCalendar=*-*-* 02:00
Persistent=true
[Install]
WantedBy=timers.target
```

Use `metrics.textfile_dir` with node_exporter's textfile collector to export the results of one-shot
runs.

## What a host needs without the image

- The `cistern` binary (static when built with `CGO_ENABLED=0`).
- The client tools of every driver you use, in `PATH` (see [Drivers](drivers/)), with the PostgreSQL
  client major version at least the server's.
- Network access to the sources and the repository; HTTPS outbound for S3 and webhooks.
- A writable `CISTERN_TMPDIR` (default: the system temp directory) for 0600 option files.
- The public key; the private key only where you verify, restore or run incremental S3 backups.

Cistern does not need root.

## Upgrading

Snapshots carry `format: 1` in their manifest. A newer Cistern reads older snapshots of the same
format; the format will not change without a version bump and release notes.
