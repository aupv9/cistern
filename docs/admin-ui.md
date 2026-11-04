# Admin UI

`cistern server` serves a web UI and its REST API (`/api/v1`, described in
[api/openapi.yaml](../api/openapi.yaml)). It shows jobs, snapshots, run history and alerts, starts
backups, verifies and restores, handles approvals for production restores and keeps an audit log.

## Architecture

```
 browser ──HTTPS──▶ ingress / proxy ──▶ cistern server ──bearer token──▶ cistern daemon ──▶ sources
                                          │  (no private key)              │  (holds the key)
                                          │ read                           │ read/write
                                          ▼                                ▼
                                   repository: snapshots, _admin/{runs,audit,approvals,sessions}
```

- **The server holds no private key.** It never reads `encryption.identity_file`. It reads the
  repository (manifests, run records) and writes only audit entries, approval objects and session
  revocations.
- **Every operation runs in the daemon.** The server sends backup, verify and restore requests to the
  daemon's internal API (`POST /internal/v1/runs`) on its own listener (`daemon.api_listen`,
  `:9465`), separate from the metrics port, authenticated with a bearer token and optionally TLS.
  The daemon runs them through the same engine path as scheduled runs: job lock, metrics, webhooks,
  run records.
- **The UI cannot break schedules.** The daemon does not depend on the server. A stopped server only
  means nobody can use the UI.
- **Read-only without a daemon.** When `server.daemon` is not configured or the daemon cannot be
  reached, every page still works and write endpoints answer `503 daemon_unavailable`.
- **No database.** Run records, approvals and the audit log are JSON objects in the repository under
  `<prefix>/_admin/`. Job names cannot start with `_`, so they never clash with snapshots.

## Roles

Roles come from the OIDC groups claim through `server.roles`. A user with no mapped role is sent to
`/denied` and the attempt is audited as `auth.denied`. A session without roles gets
`403 forbidden` from `GET /api/v1/me`, which the UI also treats as `/denied`.

| Role | Can | Includes |
| --- | --- | --- |
| `viewer` | Read everything: overview, jobs, snapshots, runs, approvals, audit log | — |
| `operator` | Start backups and verifies | viewer |
| `restorer` | Restore into non-production targets; request restores into production targets | operator, viewer |
| `admin` | Everything above, and decide approvals | restorer, operator, viewer |
| `approver` | Approve or reject restore requests | viewer |

- `viewer < operator < restorer < admin` are cumulative; `approver` is separate.
- Nobody decides their own request: the requester's OIDC subject is compared, not the email.
- **Restores into a production target always need an approval**, also for admins.
- Authorization is checked on the server for every endpoint. A refused request answers `403` and is
  audited as `access.denied`.

| Endpoint | Role |
| --- | --- |
| `GET /api/v1/me`, `POST /api/v1/auth/logout` | any signed-in user |
| `GET /api/v1/overview`, `/jobs/…`, `/runs/…`, `/approvals/…`, `/audit`, `/audit/export` | viewer |
| `POST /api/v1/jobs/{job}/backup`, `POST /api/v1/jobs/{job}/verify` | operator |
| `POST /api/v1/jobs/{job}/restore/preview`, `POST /api/v1/jobs/{job}/restore` | restorer |
| `POST /api/v1/approvals/{id}/approve`, `…/reject` | approver or admin, not the requester |

## Restore targets

The UI never accepts a free-form host: that would let a user send a copy of production data to a
server of their own. A restore chooses one of the job's `restore_targets` and, for drivers that
support it (`postgres`, `mysql`), a `restore_suffix`.

```yaml
jobs:
  - name: orders-db
    driver: postgres
    params: { host: db.internal, user: backup, password: "${PGPASSWORD}", databases: [orders] }
    restore_targets:
      - name: staging
        description: staging-db.internal:5432
        production: false
        set: { host: staging-db.internal, password: "${STAGING_PGPASSWORD}" }
      - name: production
        production: true
```

- A job without `restore_targets` has one implicit target `original`: production, no overrides.
- `production` defaults to `true` when omitted, so a forgotten flag fails safe.
- `restore_suffix` is `_restored` unless the request or the target sets it; an empty string restores
  under the original database names. Other drivers reject `restore_suffix` with `422`.
- The preview shows the resolved overrides and the equivalent CLI command
  (`cistern restore --job J --snapshot ID --set k=v … --yes`). Keys that look secret (password,
  secret, token, key, credential, …) are shown as `<redacted>`.
- Starting a restore needs a reason (3–500 characters) and `confirm_job` equal to the job name.
- The daemon resolves the target again from its own configuration and refuses the run if it differs
  from what the server sent. Secret values never travel from the server to the daemon.

## Approvals

A restore into a production target creates an approval request instead of running:

```
<prefix>/_admin/approvals/<id>/request.json    job, snapshot (full ID), target, restore_suffix, reason, requester
<prefix>/_admin/approvals/<id>/decision.json   approved | rejected | expired, who, when, note
<prefix>/_admin/approvals/<id>/run.json        run ID of the restore started on approval
```

- Objects are written with `PutIfAbsent`, so exactly one decision wins when two approvers click at
  the same time; the loser gets `409`.
- On an S3 repository with `object_lock`, all three objects get a retain-until of
  `server.audit_retention_days` in the repository's lock mode, like audit entries.
- A request expires 24 hours after it was made. Expiry is computed; the first read after that records
  an `expired` decision and an `approval.expire` audit entry.
- On approval the server checks again that the snapshot and target still exist and the job is idle,
  then dispatches the restore to the daemon on behalf of the requester.

## Audit log

Every write action, sign-in, sign-out and denial creates one entry:

```
<prefix>/_admin/audit/YYYY/MM/DD/<YYYYMMDDTHHMMSS.nnnnnnnnnZ>-<rand>.json
```

- Each entry is its own object, written with `PutIfAbsent`. Cistern has no code path that edits or
  deletes audit entries or approvals; prune only deletes old run records (see below).
- On an S3 repository with `object_lock`, audit objects get a per-object retain-until of
  `server.audit_retention_days` (default 400, minimum 365) in the repository's lock mode.
- Each entry is also written to stdout as one JSON line with `"kind": "cistern.audit"`, for a SIEM.
  Logs go to stderr, so the two streams stay separate.
- **Fail closed:** if the audit entry for a write action cannot be stored, the action is refused with
  `503` and nothing runs.
- Entries never contain secrets. The audit page filters by time, action, job, result and text, pages
  with a cursor, and exports CSV or JSON Lines.

| Action | When |
| --- | --- |
| `auth.login`, `auth.logout` | Sign-in (including dev login), sign-out |
| `auth.denied` | Signed in at the identity provider, but no group maps to a role |
| `access.denied` | A signed-in user called an endpoint their roles do not allow, or tried to decide their own request |
| `backup.run`, `verify.run`, `restore.run` | An operation was accepted (or could not be started) |
| `restore.request` | A production restore is waiting for approval |
| `approval.approve`, `approval.reject`, `approval.expire` | Decisions |
| `run.finish` | An operation started from the UI finished (`succeeded` or `failed`) |
| `prune.run` | Reserved: the UI cannot start prunes yet, so no entry has this action today |

The first page of `GET /api/v1/audit` also returns `total`, the number of matching entries, when
there are at most 10000 of them.

## Run records

After every backup, verify, restore and non-dry-run prune, from the CLI, a schedule or the UI, the
engine writes a run record:

```
<prefix>/_admin/runs/<job>/<YYYYMMDDTHHMMSSZ>-<rand>.json
```

It holds the run ID, job, operation, status, trigger (`schedule`, `cli`, `ui`), who requested it, the
approval, snapshot, target, start and end time, duration, stored bytes and error. If writing it fails,
the failure is logged and the operation keeps its result. On an S3 repository with `object_lock`,
run records are locked for the repository's `object_lock.days`, like snapshots.

`cistern prune` (and `prune_after_backup`) deletes run records older than `run_history_days`
(default 400, per job or in `defaults`; `0` keeps them forever), but always keeps the newest 30
records of each operation of a job, so the sparkline and "last verify" survive long pauses. A record
that cannot be deleted yet because of Object Lock is logged at INFO and skipped. The UI builds run history, the 30-run
sparkline, last failure and the last verify per snapshot from these records; snapshots made before
run records existed still appear as successful backups.

## Health and alerts

The thresholds match [deploy/k8s/monitoring](../deploy/k8s/monitoring/prometheus-rules.yaml):

| Health | Meaning |
| --- | --- |
| `running` | The daemon is running an operation for the job |
| `failing` | The last failed backup is newer than the last success |
| `stale` | No successful backup for 36 hours |
| `verify_overdue` | The job has `verify_schedule` and no successful verify or restore for 8 days |
| `never_run` | No snapshot and no run yet |
| `healthy` | None of the above |

Precedence: running > failing > stale > verify_overdue > healthy. Alerts: `backup_failing`,
`backup_stale`, `verify_failing`, `verify_stale`, `daemon_unreachable`.

The overview is one call that stays fast with hundreds of jobs: repository listings are cached for
about 30 seconds (manifests and run records, which never change, are cached until restart) and the
daemon status for about 5 seconds.

## Security model

| Area | Behaviour |
| --- | --- |
| Sign-in | OIDC authorization code flow with PKCE (S256), `state` and `nonce` in a short-lived encrypted cookie (`SameSite=Lax`); discovery, JWKS cache refreshed on an unknown key ID; ID tokens signed with RS256 or ES256 only; `iss`, `aud`, `azp`, `exp`, `iat` and `nonce` checked |
| Session | AES-GCM sealed cookie `__Host-cistern_session` (`Secure`, `HttpOnly`, `SameSite=Strict`, `Path=/`) with a random session ID; absolute lifetime `session_ttl` (at most 8 h), idle timeout `idle_timeout` (30 min), re-issued on activity |
| Sign-out | `POST /api/v1/auth/logout` writes `_admin/sessions/revoked/<sid>.json` (session ID, subject, revoked and expiry time) to the state repository and rejects the session with `401` from then on. Every server loads the revocations at startup and re-reads them every 30 seconds, so a sign-out survives restarts and reaches all replicas within 30 s. Revocations are never Object-Locked; expired ones are deleted when they are read. If the object cannot be written, the session is revoked on that replica only and the `auth.logout` audit entry says so |
| Rate limits | In-memory token buckets (`server.rate_limit`): sign-in endpoints (`/auth/login`, `/auth/callback`, `/auth/dev-login`) 10 per minute per client address; non-GET API requests 30 per minute per user; GET API requests 600 per minute per user. Over the limit: `429 rate_limited` with `Retry-After`. Idle buckets are dropped and the number of buckets is capped, so memory stays bounded |
| CSRF | Every non-GET API request needs `X-Cistern-CSRF` equal to the `csrf_token` from `GET /api/v1/me`; request bodies must be `application/json` |
| Headers | CSP `default-src 'self'` with `script-src 'self'` plus the SHA-256 hashes of the inline scripts in `_shell.html`, computed at startup (no `unsafe-inline` for scripts; styles allow it), `frame-ancestors 'none'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, HSTS when TLS is on or `tls.upstream` is set |
| Client IP | Taken from the connection. Only when the connection comes from an address in `server.trusted_proxies` is `X-Forwarded-For` used: the right-most entry that is not itself a trusted proxy |
| Secrets | Never in responses, audit entries or logs. Job details show only `host`, `port`, `database`, `databases`, `bucket`, `prefix`, `endpoint` (with user info stripped) |
| Network | `server.listen` defaults to `127.0.0.1:8443`. The daemon's internal API has its own listener (`daemon.api_listen`, `:9465`), only exists with `daemon.api_token`, compares the bearer token in constant time and can use TLS (`daemon.api_tls`, verified by the server with `server.daemon.ca_file`). The metrics port (`:9464`) answers `404` for `/internal/*`, so Prometheus access never exposes it; the Kubernetes NetworkPolicy admits only `cistern-server` pods to 9465 |
| Object Lock | On S3 repositories with `object_lock`: snapshots and run records for `object_lock.days`; audit entries and approval objects (request, decision, run) for `server.audit_retention_days`. Job locks and session revocations are never locked. Local repositories have no Object Lock |

## Configuration

One configuration file can serve the CLI, the daemon and the server. See
[Configuration](configuration.md#server) for every key.

```yaml
daemon:
  api_token: ${CISTERN_DAEMON_TOKEN}
server:
  name: production
  listen: ":8443"
  public_url: https://backup.example.com
  tls: { upstream: true }
  session_key: ${CISTERN_SESSION_KEY}
  oidc:
    issuer: https://sso.example.com/realms/ops
    client_id: cistern
    client_secret: ${CISTERN_OIDC_CLIENT_SECRET}
  roles:
    - { group: backup-viewers, roles: [viewer] }
    - { group: dba, roles: [restorer] }
    - { group: dba-leads, roles: [approver] }
    - { group: platform-admins, roles: [admin] }
  daemon: { url: "http://cistern:9465", token: "${CISTERN_DAEMON_TOKEN}" }
  trusted_proxies: ["10.0.0.0/8"]   # the ingress controller, so the client address is known
```

Register `<public_url>/api/v1/auth/callback` as the redirect URI at the identity provider and make
it put the groups into the ID token (claim `groups`, or set `groups_claim`).

Each process only needs its own secrets: unset `${VAR}` references expand to an empty string where a
process does not use them.

| Process | Tolerates unset variables under |
| --- | --- |
| `cistern server` | `jobs[].params`, `jobs[].restore_targets[].set`, `daemon`, `notify`, `encryption` (the server never encrypts or decrypts and ignores the whole section) |
| `cistern daemon` | `server` |
| other commands | `server`, `daemon`, `jobs[].restore_targets[].set` |

The server still needs the repository credentials, because it reads the repository and writes the
audit log and approvals. Non-secret values in `restore_targets[].set` must resolve to the same value
in the server and the daemon, otherwise the daemon refuses the restore.

## Building

The UI lives in [web/](../web/) (TanStack Start in SPA mode, built to `web/dist/client/`).

```sh
make ui             # npm ci && npm run build in web/ (Node.js 22)
make build-ui       # go build -tags ui: embeds web/dist/client into bin/cistern
make dist           # release archives with the UI embedded, plus SHA256SUMS, in dist/ (needs make ui first)
make release-notes  # dist/release-notes.md: the CHANGELOG section of the current version
```

Without `-tags ui` the binary serves a short page saying the UI was not built; the API works the
same. The container image always includes the UI. Files under `/assets/` are served with a one-year
immutable cache; every other path that is not a file gets `_shell.html` with `no-cache`.

## Development

```sh
make dev-ui     # or scripts/dev-ui.sh
```

One command starts a backend for UI work:

- a `postgres:16` container (the image pinned in `scripts/e2e-local.sh`) that also runs
  `cistern daemon`, with its metrics port on `127.0.0.1:8481` and its internal API on
  `127.0.0.1:8482`;
- `cistern server --insecure-dev-login` on the host at `http://127.0.0.1:8480`, restarted
  automatically when it exits (`kill $(cat .dev-ui/server.pid)` restarts it with the variables in
  `.dev-ui/server.env`); the sign-in limit is raised to 120 per minute;
- a local repository and logs in `.dev-ui/` (`repo/`, `cistern.yaml`, `server.log`, `daemon.log`,
  `audit.jsonl`);
- four jobs: `shop-db` (healthy, schedules, a non-production `scratch` target and a `production`
  target), `crm-db` (verify overdue), `legacy-db` (failing) and `analytics` (never run);
- 30 days of synthetic run history, real snapshots, a backup started through the API and a pending
  production restore request.

PostgreSQL is used because no driver works without an external service. The script needs Docker,
Go, `curl` and `jq`. It bind-mounts `DEV_UI_DIR` into the container, so that directory must be on a
path Docker can share with its VM: with colima's default setup only `$HOME` is shared, so a
directory under `/tmp` or `/private/tmp` does not work. Options: `--no-seed` (no history), `--run CMD` (run `CMD` once the backend is up,
then stop with its exit status), `DEV_UI_PORT`, `DEV_UI_DAEMON_PORT`, `DEV_UI_DAEMON_API_PORT`
(default `DEV_UI_DAEMON_PORT` + 1), `DEV_UI_DIR`, `DEV_UI_LOGIN_PER_MINUTE` (120),
`DEV_UI_WRITE_PER_MINUTE` (30). `--run` exports `DEV_UI_BASE`, `DEV_UI_DIR`, `DEV_UI_CONTAINER`,
`DEV_UI_PG_PASSWORD`, `DEV_UI_METRICS_URL` and `DEV_UI_DAEMON_API_URL` to the command.

Sign in with:

```
http://127.0.0.1:8480/api/v1/auth/dev-login?email=you@example.com&name=You&roles=admin&return_to=/
```

`roles` is a comma-separated list of `viewer`, `operator`, `restorer`, `approver`, `admin` (default
`admin`). Without `return_to` the endpoint returns the same JSON as `GET /api/v1/me`. The session
cookie is then `cistern_session` (not `Secure`).

`--insecure-dev-login` is refused unless `server.listen` is a loopback address and `server.tls` is
not set, and the server logs a loud warning at startup. Never use it outside local development.

`scripts/e2e/server.sh` drives the API end to end against this backend (backup, snapshots, restore
to a non-production target, approval flow, self-approval refused, audit entries, `/internal/*`
absent on the metrics port, a signed-out session still refused after a server restart, `429` with
`Retry-After` over a lowered write limit, no secrets leaked). It runs in CI as the `server` target of
`scripts/e2e-local.sh`.

### End-to-end tests

| Target | Command | What it covers |
| --- | --- | --- |
| `server` | `scripts/e2e-local.sh server` | `scripts/e2e/server.sh` against a `dev-ui.sh` backend (see above) |
| `oidc` | `make e2e-oidc` or `scripts/e2e-local.sh oidc` | `scripts/e2e/oidc.sh`: a real Keycloak, the authorization code flow with PKCE over TLS, roles from the groups claim, approvals by a second person, sign-out revocation across a server restart, the sign-in rate limit, the internal API only on its own port, audit subjects, no secrets in logs |
| `ui` | `make e2e-ui` or `scripts/e2e-local.sh ui` | Builds the UI and runs Playwright (`npm run test:e2e:real` in `web/`) against a real `dev-ui.sh` backend |

`scripts/e2e-local.sh server` and `ui` run their backend on ports 18480–18482 (server, daemon
metrics, daemon API) in a temporary directory under `.e2e-tmp/` in the repository, so they do not
disturb a `make dev-ui` on 8480–8482. Set `CISTERN_E2E_TMP` to use another directory; it must be
shareable with Docker, as above.
