import { http, HttpResponse, delay } from 'msw'
import type { AuditEntry, RestoreInput, Role, SnapshotSummary } from '~/api/queries'
import {
  alertsFor,
  buildManifest,
  createMockDb,
  nextApprovalId,
  refreshDerived,
  startSimulatedRun,
  tick,
  type MockDb,
} from './db'

const API = '*/api/v1'

let db: MockDb = createMockDb()
let latencyMs = 0

export function resetMockDb(options: { now?: number; roles?: Role[]; latencyMs?: number } = {}) {
  db = createMockDb(options.now)
  if (options.roles) {
    db.me.roles = options.roles
    refreshDerived(db)
  }
  latencyMs = options.latencyMs ?? 0
  return db
}

export function getMockDb() {
  return db
}

function fail(status: number, code: string, message: string, fields?: Record<string, string>) {
  return HttpResponse.json({ error: { code, message, ...(fields ? { fields } : {}) } }, { status })
}

async function wait() {
  if (latencyMs > 0) await delay(latencyMs)
}

function checkCsrf(request: Request) {
  if (request.headers.get('X-Cistern-CSRF') !== db.me.csrf_token) {
    return fail(403, 'csrf', 'The security token is missing or old. Reload the page and try again.')
  }
  return null
}

function guard(request: Request, role?: Role) {
  if (!db.signedIn) return fail(401, 'unauthenticated', 'Sign in to continue.')
  if (request.method !== 'GET') {
    const csrf = checkCsrf(request)
    if (csrf) return csrf
  }
  if (role && !db.me.roles.includes(role) && !(role !== 'approver' && db.me.roles.includes('admin'))) {
    return fail(403, 'forbidden', `This needs the ${role} role.`)
  }
  tick(db)
  return null
}

function daemonDown() {
  const d = db.me.deployment.daemon
  if (!d.configured || !d.reachable) return fail(503, 'daemon_unavailable', 'The Cistern daemon is not reachable. Try again when it is back.')
  return null
}

function resolveSnapshot(job: string, ref: string): SnapshotSummary | null {
  const snaps = db.snapshots[job] ?? []
  if (ref === 'latest' || ref === '') return snaps[0] ?? null
  const exact = snaps.find((s) => s.id === ref)
  if (exact) return exact
  const matches = snaps.filter((s) => s.id.startsWith(ref))
  return matches.length === 1 ? (matches[0] ?? null) : null
}

const SUFFIX = /^_[a-z0-9_]{1,30}$/

function previewFor(job: string, input: RestoreInput) {
  const detail = db.jobs.find((j) => j.name === job)
  if (!detail) return { error: fail(404, 'not_found', `There is no job named ${job}.`) }
  const fields: Record<string, string> = {}
  const snap = resolveSnapshot(job, input.snapshot)
  if (!snap) fields.snapshot = `No snapshot of ${job} matches “${input.snapshot}”.`
  const target = detail.restore_targets.find((t) => t.name === input.target)
  if (!target) fields.target = `${job} has no restore target named “${input.target}”.`
  const suffix = input.restore_suffix ?? '_restored'
  if (detail.driver !== 's3' && !SUFFIX.test(suffix)) fields.restore_suffix = 'Use an underscore followed by 1 to 30 lowercase letters, digits or underscores.'
  if (Object.keys(fields).length > 0 || !snap || !target) {
    return { error: fail(422, 'invalid', 'The restore cannot be prepared with these values.', fields) }
  }
  const overrides: Record<string, string> = {}
  if (detail.driver === 's3') {
    const bucket = target.description?.replace(/^Bucket /, '') ?? 'app-uploads-restore'
    overrides.bucket = bucket
    overrides.prefix = target.production ? 'restored/' : ''
  } else {
    const [host, port] = (target.description ?? '').split(':')
    if (host) overrides.host = host
    if (port) overrides.port = port
    overrides.restore_suffix = suffix
    if (detail.source.databases) overrides.databases = detail.source.databases
  }
  const sets = Object.entries(overrides)
    .filter(([, v]) => v !== '')
    .map(([k, v]) => `  --set ${k}=${v}`)
  const cli = [`cistern restore --job ${job}`, `  --snapshot ${snap.id}`, ...sets].join(' \\\n') + ' --yes'
  return {
    preview: {
      job,
      snapshot: snap,
      target,
      needs_approval: target.production,
      overrides,
      cli_command: cli,
    },
  }
}

function nowIso() {
  return new Date(db.now()).toISOString()
}

function addAudit(e: Omit<AuditEntry, 'id' | 'time' | 'actor' | 'roles'> & { time?: string }) {
  db.counter += 1
  db.audit.push({
    id: `aud_${db.counter}`,
    time: e.time ?? nowIso(),
    actor: db.me.user,
    roles: db.me.roles,
    source_ip: '10.20.4.17',
    ...e,
  })
}

const AUDIT_FIELDS = ['time', 'actor', 'action', 'result', 'job', 'snapshot', 'target', 'run_id', 'approval_id', 'reason', 'summary'] as const

function filterAudit(url: URL) {
  const from = url.searchParams.get('from')
  const to = url.searchParams.get('to')
  const action = url.searchParams.get('action')
  const job = url.searchParams.get('job')
  const result = url.searchParams.get('result')
  const q = (url.searchParams.get('q') ?? '').trim().toLowerCase()
  const fromMs = from ? Date.parse(from) : db.now() - 7 * 86_400_000
  const toMs = to ? Date.parse(to) : db.now() + 60_000
  return [...db.audit]
    .sort((a, b) => b.time.localeCompare(a.time))
    .filter((e) => {
      const t = Date.parse(e.time)
      if (t < fromMs || t > toMs) return false
      if (action && e.action !== action) return false
      if (job && e.job !== job) return false
      if (result && e.result !== result) return false
      if (q) {
        const hay = [e.actor.name, e.actor.email, e.job, e.snapshot].filter(Boolean).join(' ').toLowerCase()
        if (!hay.includes(q)) return false
      }
      return true
    })
}

function csvCell(v: unknown) {
  const s = v == null ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v)
  return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
}

export const handlers = [
  http.get(`${API}/me`, async () => {
    await wait()
    if (!db.signedIn) return fail(401, 'unauthenticated', 'Sign in to continue.')
    return HttpResponse.json(db.me)
  }),

  http.post(`${API}/auth/logout`, async ({ request }) => {
    await wait()
    const g = guard(request)
    if (g) return g
    db.signedIn = false
    return new HttpResponse(null, { status: 204 })
  }),

  http.get(`${API}/overview`, async ({ request }) => {
    await wait()
    const g = guard(request)
    if (g) return g
    const jobs = db.jobs.map((j) => ({
      name: j.name,
      driver: j.driver,
      source_label: j.source_label,
      repository: j.repository,
      schedule: j.schedule,
      verify_schedule: j.verify_schedule,
      health: j.health,
      running_op: j.running_op,
      last_success: j.last_success,
      last_failure: j.last_failure,
      last_failure_error: j.last_failure_error,
      last_verify: j.last_verify,
      next_run: j.next_run,
      snapshots: j.snapshots,
      stored_bytes: j.stored_bytes,
      recent_backups: j.recent_backups,
    }))
    const recent = [...db.audit].sort((a, b) => b.time.localeCompare(a.time)).slice(0, 10)
    return HttpResponse.json({
      generated_at: nowIso(),
      jobs,
      alerts: alertsFor(db),
      pending_approvals: db.approvals.filter((a) => a.state === 'pending'),
      recent_activity: recent,
      repositories: [
        {
          name: 's3-main',
          type: 's3',
          stored_bytes: db.jobs.reduce((a, j) => a + j.stored_bytes, 0),
          object_lock: { mode: 'COMPLIANCE', days: 7 },
        },
      ],
    })
  }),

  http.get(`${API}/jobs/:job`, async ({ request, params }) => {
    await wait()
    const g = guard(request)
    if (g) return g
    const job = db.jobs.find((j) => j.name === params.job)
    if (!job) return fail(404, 'not_found', `There is no job named ${String(params.job)}.`)
    return HttpResponse.json(job)
  }),

  http.get(`${API}/jobs/:job/snapshots`, async ({ request, params }) => {
    await wait()
    const g = guard(request)
    if (g) return g
    const job = String(params.job)
    if (!db.jobs.some((j) => j.name === job)) return fail(404, 'not_found', `There is no job named ${job}.`)
    return HttpResponse.json({ snapshots: db.snapshots[job] ?? [] })
  }),

  http.get(`${API}/jobs/:job/snapshots/:snapshot`, async ({ request, params }) => {
    await wait()
    const g = guard(request)
    if (g) return g
    const job = String(params.job)
    const snap = resolveSnapshot(job, String(params.snapshot))
    if (!snap) return fail(404, 'not_found', `Snapshot ${String(params.snapshot)} of ${job} was not found.`)
    return HttpResponse.json(buildManifest(db, job, snap))
  }),

  http.get(`${API}/jobs/:job/runs`, async ({ request, params }) => {
    await wait()
    const g = guard(request)
    if (g) return g
    const url = new URL(request.url)
    const op = url.searchParams.get('op')
    const limit = Math.min(200, Number(url.searchParams.get('limit') ?? 30) || 30)
    const runs = db.runs
      .filter((r) => r.job === params.job && (!op || r.op === op))
      .sort((a, b) => (b.queued_at ?? '').localeCompare(a.queued_at ?? ''))
      .slice(0, limit)
    return HttpResponse.json({ runs })
  }),

  http.post(`${API}/jobs/:job/backup`, async ({ request, params }) => {
    await wait()
    const g = guard(request, 'operator')
    if (g) return g
    const down = daemonDown()
    if (down) return down
    const job = db.jobs.find((j) => j.name === params.job)
    if (!job) return fail(404, 'not_found', `There is no job named ${String(params.job)}.`)
    if (job.current_run) return fail(409, 'conflict', `${job.name} already has a ${job.current_run.op} ${job.current_run.state}. Wait for it to finish.`)
    const run = startSimulatedRun(db, { job: job.name, op: 'backup' }, job.name === 'shop-db' ? 'failed' : 'succeeded', job.name === 'shop-db' ? 6_000 : 14_000)
    addAudit({ action: 'backup.run', result: 'accepted', job: job.name, run_id: run.id, summary: `${db.me.user.name} started a backup of ${job.name}` })
    refreshDerived(db)
    return HttpResponse.json({ run_id: run.id }, { status: 202 })
  }),

  http.post(`${API}/jobs/:job/verify`, async ({ request, params }) => {
    await wait()
    const g = guard(request, 'operator')
    if (g) return g
    const down = daemonDown()
    if (down) return down
    const job = db.jobs.find((j) => j.name === params.job)
    if (!job) return fail(404, 'not_found', `There is no job named ${String(params.job)}.`)
    if (job.current_run) return fail(409, 'conflict', `${job.name} already has a ${job.current_run.op} ${job.current_run.state}. Wait for it to finish.`)
    const body = (await request.json().catch(() => ({}))) as { snapshot?: string }
    const snap = resolveSnapshot(job.name, body.snapshot ?? 'latest')
    if (!snap) return fail(422, 'invalid', 'No snapshot matches.', { snapshot: 'No snapshot matches.' })
    const run = startSimulatedRun(db, { job: job.name, op: 'verify', snapshot: snap.id }, 'succeeded', 10_000)
    addAudit({ action: 'verify.run', result: 'accepted', job: job.name, snapshot: snap.id, run_id: run.id, summary: `${db.me.user.name} started a verify of ${job.name} snapshot ${snap.id}` })
    refreshDerived(db)
    return HttpResponse.json({ run_id: run.id }, { status: 202 })
  }),

  http.post(`${API}/jobs/:job/restore/preview`, async ({ request, params }) => {
    await wait()
    const g = guard(request, 'restorer')
    if (g) return g
    const input = (await request.json()) as RestoreInput
    const res = previewFor(String(params.job), input)
    if (res.error) return res.error
    return HttpResponse.json(res.preview)
  }),

  http.post(`${API}/jobs/:job/restore`, async ({ request, params }) => {
    await wait()
    const g = guard(request, 'restorer')
    if (g) return g
    const down = daemonDown()
    if (down) return down
    const job = String(params.job)
    const body = (await request.json()) as RestoreInput & { reason?: string; confirm_job?: string }
    const res = previewFor(job, body)
    if (res.error) return res.error
    const fields: Record<string, string> = {}
    const reason = (body.reason ?? '').trim()
    if (reason.length < 3 || reason.length > 500) fields.reason = 'Write a reason between 3 and 500 characters.'
    if (body.confirm_job !== job) fields.confirm_job = `Type ${job} exactly to confirm.`
    if (Object.keys(fields).length) return fail(422, 'invalid', 'Some fields need attention.', fields)
    const p = res.preview
    const detail = db.jobs.find((j) => j.name === job)
    if (detail?.current_run) return fail(409, 'conflict', `${job} already has a ${detail.current_run.op} running. Wait for it to finish.`)
    if (p.needs_approval) {
      const id = nextApprovalId(db)
      const nowMs = db.now()
      db.approvals.push({
        id,
        state: 'pending',
        job,
        snapshot: p.snapshot.id,
        target: p.target,
        restore_suffix: body.restore_suffix ?? '_restored',
        reason,
        requested_by: db.me.user,
        requested_at: new Date(nowMs).toISOString(),
        expires_at: new Date(nowMs + 86_400_000).toISOString(),
      })
      addAudit({ action: 'restore.request', result: 'pending', job, snapshot: p.snapshot.id, target: p.target.name, approval_id: id, reason, params: p.overrides, summary: `${db.me.user.name} asked to restore ${job} into ${p.target.name}` })
      refreshDerived(db)
      return HttpResponse.json({ needs_approval: true, approval_id: id }, { status: 202 })
    }
    const run = startSimulatedRun(db, { job, op: 'restore', snapshot: p.snapshot.id, target: p.target.name }, 'succeeded', 12_000)
    addAudit({ action: 'restore.run', result: 'accepted', job, snapshot: p.snapshot.id, target: p.target.name, run_id: run.id, reason, params: p.overrides, summary: `${db.me.user.name} started a restore of ${job} into ${p.target.name}` })
    refreshDerived(db)
    return HttpResponse.json({ needs_approval: false, run_id: run.id }, { status: 202 })
  }),

  http.get(`${API}/runs`, async ({ request }) => {
    await wait()
    const g = guard(request)
    if (g) return g
    const active = new URL(request.url).searchParams.get('active') !== 'false'
    const runs = db.runs.filter((r) => r.trigger === 'ui' && (!active || r.state === 'queued' || r.state === 'running'))
    return HttpResponse.json({ runs })
  }),

  http.get(`${API}/runs/:run`, async ({ request, params }) => {
    await wait()
    const g = guard(request)
    if (g) return g
    const run = db.runs.find((r) => r.id === params.run)
    if (!run) return fail(404, 'not_found', `Run ${String(params.run)} was not found.`)
    return HttpResponse.json(run)
  }),

  http.get(`${API}/approvals`, async ({ request }) => {
    await wait()
    const g = guard(request)
    if (g) return g
    const url = new URL(request.url)
    const state = url.searchParams.get('state') ?? 'all'
    const limit = Math.min(200, Number(url.searchParams.get('limit') ?? 50) || 50)
    const approvals = [...db.approvals]
      .filter((a) => state === 'all' || (state === 'pending' ? a.state === 'pending' : a.state !== 'pending'))
      .sort((a, b) => b.requested_at.localeCompare(a.requested_at))
      .slice(0, limit)
    return HttpResponse.json({ approvals })
  }),

  http.get(`${API}/approvals/:id`, async ({ request, params }) => {
    await wait()
    const g = guard(request)
    if (g) return g
    const a = db.approvals.find((x) => x.id === params.id)
    if (!a) return fail(404, 'not_found', `Approval ${String(params.id)} was not found.`)
    return HttpResponse.json(a)
  }),

  http.post(`${API}/approvals/:id/approve`, async ({ request, params }) => {
    await wait()
    const g = guard(request, 'approver')
    if (g) return g
    const a = db.approvals.find((x) => x.id === params.id)
    if (!a) return fail(404, 'not_found', `Approval ${String(params.id)} was not found.`)
    if (a.requested_by.id === db.me.user.id) return fail(403, 'forbidden', 'You cannot approve your own request.')
    if (a.state !== 'pending') return fail(409, 'conflict', `This request is already ${a.state}.`)
    const note = ((await request.json().catch(() => ({}))) as { note?: string }).note
    const run = startSimulatedRun(db, { job: a.job, op: 'restore', snapshot: a.snapshot, target: a.target.name, approval_id: a.id, requested_by: a.requested_by }, 'succeeded', 12_000)
    a.state = 'approved'
    a.decided_by = db.me.user
    a.decided_at = nowIso()
    if (note) a.note = note
    a.run_id = run.id
    addAudit({ action: 'approval.approve', result: 'succeeded', job: a.job, snapshot: a.snapshot, target: a.target.name, approval_id: a.id, run_id: run.id, reason: note, summary: `${db.me.user.name} approved the restore of ${a.job} into ${a.target.name} requested by ${a.requested_by.name}` })
    refreshDerived(db)
    return HttpResponse.json(a)
  }),

  http.post(`${API}/approvals/:id/reject`, async ({ request, params }) => {
    await wait()
    const g = guard(request, 'approver')
    if (g) return g
    const a = db.approvals.find((x) => x.id === params.id)
    if (!a) return fail(404, 'not_found', `Approval ${String(params.id)} was not found.`)
    if (a.requested_by.id === db.me.user.id) return fail(403, 'forbidden', 'You cannot reject your own request.')
    if (a.state !== 'pending') return fail(409, 'conflict', `This request is already ${a.state}.`)
    const note = (((await request.json().catch(() => ({}))) as { note?: string }).note ?? '').trim()
    if (note.length < 3) return fail(422, 'invalid', 'A note is required to reject.', { note: 'Write at least 3 characters.' })
    a.state = 'rejected'
    a.decided_by = db.me.user
    a.decided_at = nowIso()
    a.note = note
    addAudit({ action: 'approval.reject', result: 'succeeded', job: a.job, approval_id: a.id, reason: note, summary: `${db.me.user.name} rejected the restore of ${a.job} into ${a.target.name} requested by ${a.requested_by.name}` })
    refreshDerived(db)
    return HttpResponse.json(a)
  }),

  http.get(`${API}/audit`, async ({ request }) => {
    await wait()
    const g = guard(request)
    if (g) return g
    const url = new URL(request.url)
    const all = filterAudit(url)
    const limit = Math.min(500, Number(url.searchParams.get('limit') ?? 50) || 50)
    const cursor = url.searchParams.get('cursor')
    const start = cursor ? Number(cursor.replace(/^c/, '')) || 0 : 0
    const entries = all.slice(start, start + limit)
    const next = start + limit < all.length ? `c${start + limit}` : undefined
    return HttpResponse.json(next ? { entries, next_cursor: next } : { entries })
  }),

  http.get(`${API}/audit/export`, async ({ request }) => {
    await wait()
    const g = guard(request)
    if (g) return g
    const rows = filterAudit(new URL(request.url))
    const lines = [AUDIT_FIELDS.join(',')]
    for (const e of rows) {
      lines.push(
        AUDIT_FIELDS.map((f) => (f === 'actor' ? csvCell(e.actor.email ?? e.actor.name) : csvCell(e[f]))).join(','),
      )
    }
    return new HttpResponse(lines.join('\n') + '\n', {
      headers: {
        'Content-Type': 'text/csv; charset=utf-8',
        'Content-Disposition': 'attachment; filename="cistern-audit.csv"',
      },
    })
  }),
]
