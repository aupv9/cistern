import type {
  Actor,
  Alert,
  Approval,
  AuditEntry,
  DrillCheck,
  DrillResult,
  DrillTable,
  JobDetail,
  JobHealth,
  Manifest,
  Me,
  Op,
  Role,
  Run,
  RunState,
  SnapshotSummary,
} from '~/api/queries'

const MIN = 60_000
const HOUR = 60 * MIN
const DAY = 24 * HOUR
const GIB = 1024 ** 3
const MIB = 1024 ** 2
const KIB = 1024

export const TIMEZONE = 'Asia/Ho_Chi_Minh'
const TZ_OFFSET_MS = 7 * HOUR

export const people = {
  an: { id: 'u-an-nguyen', email: 'an.nguyen@example.com', name: 'An Nguyen' },
  linh: { id: 'u-linh-tran', email: 'linh.tran@example.com', name: 'Linh Tran' },
  minh: { id: 'u-minh-le', email: 'minh.le@example.com', name: 'Minh Le' },
  huy: { id: 'u-huy-pham', email: 'huy.pham@example.com', name: 'Huy Pham' },
  quang: { id: 'u-quang-vo', email: 'quang.vo@example.com', name: 'Quang Vo' },
} satisfies Record<string, Actor>

export const scheduler: Actor = { id: 'scheduler', name: 'Scheduler' }

const roleOf: Record<string, Role[]> = {
  [people.an.id]: ['viewer', 'operator', 'restorer', 'approver'],
  [people.linh.id]: ['viewer', 'operator', 'restorer'],
  [people.minh.id]: ['viewer', 'approver'],
  [people.huy.id]: ['viewer', 'operator', 'approver'],
  [people.quang.id]: ['viewer', 'operator', 'restorer', 'admin'],
}

type JobSeed = {
  detail: Omit<
    JobDetail,
    | 'health'
    | 'snapshots'
    | 'stored_bytes'
    | 'recent_backups'
    | 'last_success'
    | 'last_failure'
    | 'last_failure_error'
    | 'last_verify'
    | 'next_run'
    | 'current_run'
    | 'running_op'
    | 'last_drill'
    | 'last_drill_result'
    | 'drill_schedule'
  >
  hour: number
  minute: number
  extraHours?: number[]
  verifyHour?: number
  verifyMinute?: number
  verifyEveryDays: number
  baseBytes: number
  growth: number
  durationMs: number
  failAt: number[]
  verifyLagDays?: number
  artifacts: (bytes: number) => { name: string; plain: number; stored: number }[]
  incremental?: boolean
  history?: number
  historyFailAt?: number[]
  historyError?: string
  host: string
  version: string
}

type Sim = { startMs: number; durationMs: number; outcome: 'succeeded' | 'failed'; finalized: boolean }

export type MockDb = {
  now: () => number
  me: Me
  signedIn: boolean
  jobs: JobDetail[]
  runs: Run[]
  snapshots: Record<string, SnapshotSummary[]>
  approvals: Approval[]
  audit: AuditEntry[]
  sims: Record<string, Sim>
  seeds: Record<string, JobSeed>
  counter: number
  configLoadedAt: string
}

function hex(seed: number, len = 4): string {
  let x = (seed * 2654435761) >>> 0
  let out = ''
  while (out.length < len) {
    x = (x ^ (x << 13)) >>> 0
    x = (x ^ (x >>> 17)) >>> 0
    x = (x ^ (x << 5)) >>> 0
    out += (x % 16).toString(16)
  }
  return out
}

function hash64(seed: number): string {
  let s = ''
  for (let i = 0; i < 16; i += 1) s += hex(seed * 31 + i, 4)
  return s
}

function iso(ms: number) {
  return new Date(ms).toISOString()
}

function snapshotId(startMs: number, seed: number) {
  const d = new Date(startMs + (((seed * 7) % 9) + 1) * 1000).toISOString()
  const stamp = d.slice(0, 19).replace(/[-:]/g, '')
  return `${stamp}Z-${hex(seed)}`
}

function localDayStart(ms: number) {
  const local = ms + TZ_OFFSET_MS
  return Math.floor(local / DAY) * DAY - TZ_OFFSET_MS
}

const seeds: JobSeed[] = [
  {
    detail: {
      name: 'orders-db',
      driver: 'postgres',
      source_label: 'PostgreSQL 16.4',
      repository: 's3-main',
      schedule: '0 2 * * *',
      verify_schedule: '0 3 * * *',
      retention: { daily: 7, weekly: 4, monthly: 6 },
      prune_after_backup: true,
      timeout_ms: 2 * HOUR,
      object_lock: { mode: 'COMPLIANCE', days: 7 },
      encryption: { mode: 'age-x25519', recipients: 1 },
      source: { host: 'pg-orders.internal', port: '5432', databases: 'orders' },
      drill: { schedule: '0 5 * * 0', target: 'staging', restore_suffix: '_drill', max_age_days: 35, keep: false, checks: 2 },
      restore_targets: [
        { name: 'staging', description: 'staging-db.internal:5432', production: false },
        { name: 'production', description: 'pg-orders.internal:5432', production: true },
      ],
    },
    hour: 2,
    minute: 0,
    verifyHour: 3,
    verifyMinute: 0,
    verifyEveryDays: 1,
    baseBytes: 2.7 * GIB,
    growth: 0.014 * GIB,
    durationMs: 3 * MIN + 30_000,
    failAt: [],
    history: 62,
    historyFailAt: [9, 31, 52],
    historyError: 'pg_dump: error: connection to server at "pg-orders.internal" (10.20.3.5), port 5432 failed: timeout expired',
    artifacts: (bytes) => [
      { name: 'db/orders.dump', plain: Math.round(bytes * 3.4), stored: bytes },
      { name: 'globals.sql', plain: 61 * KIB, stored: 18 * KIB },
    ],
    host: 'pg-orders.internal',
    version: '16.4',
  },
  {
    detail: {
      name: 'shop-db',
      driver: 'mysql',
      source_label: 'MySQL 8.4',
      repository: 's3-main',
      schedule: '30 2 * * *',
      verify_schedule: '30 3 * * 0',
      retention: { last: 3, daily: 7, weekly: 4 },
      prune_after_backup: true,
      timeout_ms: HOUR,
      object_lock: { mode: 'COMPLIANCE', days: 7 },
      encryption: { mode: 'age-x25519', recipients: 2 },
      source: { host: 'mysql-shop.internal', port: '3306', databases: 'shop' },
      restore_targets: [{ name: 'staging', description: 'mysql-staging.internal:3306', production: false }],
    },
    hour: 2,
    minute: 30,
    verifyHour: 3,
    verifyMinute: 30,
    verifyEveryDays: 7,
    baseBytes: 1.25 * GIB,
    growth: 0.006 * GIB,
    durationMs: MIN + 40_000,
    failAt: [28, 29],
    artifacts: (bytes) => [{ name: 'db/shop.sql', plain: Math.round(bytes * 5.1), stored: bytes }],
    host: 'mysql-shop.internal',
    version: '8.4.3',
  },
  {
    detail: {
      name: 'billing-db',
      driver: 'postgres',
      source_label: 'PostgreSQL 15.8',
      repository: 's3-main',
      schedule: '0 2,14 * * *',
      verify_schedule: '0 4 * * *',
      retention: { last: 4, daily: 14, monthly: 12 },
      prune_after_backup: true,
      timeout_ms: HOUR,
      object_lock: { mode: 'COMPLIANCE', days: 7 },
      encryption: { mode: 'age-x25519', recipients: 1 },
      source: { host: 'pg-billing.internal', port: '5432', databases: 'billing,ledger' },
      drill: { schedule: '0 6 * * 1', target: 'staging', restore_suffix: '_drill', max_age_days: 35, keep: false, checks: 1 },
      restore_targets: [
        { name: 'staging', description: 'staging-db.internal:5432', production: false },
        { name: 'production', description: 'pg-billing.internal:5432', production: true },
      ],
    },
    hour: 2,
    minute: 0,
    extraHours: [14],
    verifyHour: 4,
    verifyMinute: 0,
    verifyEveryDays: 1,
    baseBytes: 760 * MIB,
    growth: 1.8 * MIB,
    durationMs: 4 * MIN + 20_000,
    failAt: [],
    artifacts: (bytes) => [
      { name: 'db/billing.dump', plain: Math.round(bytes * 0.71 * 3.2), stored: Math.round(bytes * 0.71) },
      { name: 'db/ledger.dump', plain: Math.round(bytes * 0.29 * 3.2), stored: Math.round(bytes * 0.29) },
      { name: 'globals.sql', plain: 44 * KIB, stored: 12 * KIB },
    ],
    host: 'pg-billing.internal',
    version: '15.8',
  },
  {
    detail: {
      name: 'uploads',
      driver: 's3',
      source_label: 'S3 app-uploads/',
      repository: 's3-main',
      schedule: '0 3 * * *',
      verify_schedule: '0 4 * * 6',
      retention: { daily: 14, weekly: 8, monthly: 12 },
      prune_after_backup: false,
      timeout_ms: 6 * HOUR,
      object_lock: { mode: 'COMPLIANCE', days: 7 },
      encryption: { mode: 'age-x25519', recipients: 1 },
      source: { bucket: 'app-uploads', prefix: '', endpoint: 'https://s3.storage.internal' },
      drill: { schedule: '0 6 1 * *', target: 'restore-bucket', restore_suffix: '_drill', max_age_days: 35, keep: false },
      restore_targets: [
        { name: 'restore-bucket', description: 'Bucket app-uploads-restore', production: false },
        { name: 'production', description: 'Bucket app-uploads', production: true },
      ],
    },
    hour: 3,
    minute: 0,
    verifyHour: 4,
    verifyMinute: 0,
    verifyEveryDays: 7,
    baseBytes: 6.2 * GIB,
    growth: 0.05 * GIB,
    durationMs: 11 * MIN + 12_000,
    failAt: [13],
    incremental: true,
    artifacts: (bytes) => [
      { name: 'index.json', plain: 2.9 * MIB, stored: 610 * KIB },
      { name: 'objects (41 new)', plain: Math.round(bytes * 1.02), stored: bytes },
    ],
    host: 's3.storage.internal',
    version: 'S3',
  },
  {
    detail: {
      name: 'users-db',
      driver: 'mysql',
      source_label: 'MariaDB 11.4',
      repository: 's3-main',
      schedule: '45 1 * * *',
      verify_schedule: '30 2 * * 0',
      retention: { daily: 7, weekly: 4, monthly: 3 },
      prune_after_backup: true,
      timeout_ms: HOUR,
      object_lock: { mode: 'COMPLIANCE', days: 7 },
      encryption: { mode: 'age-x25519', recipients: 1 },
      source: { host: 'mariadb-users.internal', port: '3306', databases: 'users' },
      restore_targets: [
        { name: 'staging', description: 'mysql-staging.internal:3306', production: false },
        { name: 'production', description: 'mariadb-users.internal:3306', production: true },
      ],
    },
    hour: 1,
    minute: 45,
    verifyHour: 2,
    verifyMinute: 30,
    verifyEveryDays: 7,
    verifyLagDays: 9,
    baseBytes: 590 * MIB,
    growth: 1.6 * MIB,
    durationMs: 52_000,
    failAt: [],
    artifacts: (bytes) => [{ name: 'db/users.sql', plain: Math.round(bytes * 4.6), stored: bytes }],
    host: 'mariadb-users.internal',
    version: '11.4.4-MariaDB',
  },
]

const SHOP_ERROR =
  "mysqldump: Got error: 1045: Access denied for user 'backup'@'10.20.4.12' (using password: YES) when trying to connect"

function stampAt(ms: number) {
  return new Date(ms + TZ_OFFSET_MS).toISOString().slice(11, 19)
}

function seedRunLog(job: string, op: Op, snapshot: string | undefined, state: RunState, startMs: number, durationMs: number, error?: string): string[] {
  const t0 = stampAt(startMs)
  const t1 = stampAt(startMs + Math.min(durationMs, 2_000))
  const t2 = stampAt(startMs + durationMs)
  const lines = [
    `${t0}  INFO  ${op} started  job=${job}${snapshot ? ` snapshot=${snapshot}` : ''}`,
    `${t1}  INFO  driver connected  job=${job}`,
  ]
  if (state === 'failed') {
    lines.push(`${t2}  ERROR ${op} failed  job=${job} err="${error ?? 'unknown error'}"`)
  } else if (state === 'succeeded') {
    lines.push(`${t2}  INFO  artifacts written  job=${job}`, `${t2}  INFO  manifest committed  job=${job}`, `${t2}  INFO  ${op} finished  job=${job}`)
  }
  return lines
}

export function createMockDb(nowMs: number = Date.now()): MockDb {
  const base = nowMs
  const offset = Date.now() - base
  const now = () => Date.now() - offset
  const day0 = localDayStart(nowMs - 4 * HOUR - 30 * MIN)

  const db: MockDb = {
    now,
    me: {
      user: people.an,
      roles: roleOf[people.an.id] ?? ['viewer'],
      csrf_token: 'mock-csrf-token-3f9a1c07',
      session_expires_at: iso(nowMs + 8 * HOUR),
      deployment: {
        name: 'production',
        version: '0.5.0',
        timezone: TIMEZONE,
        daemon: { configured: true, reachable: true, version: '0.5.0' },
        features: { prune: true, drills: true },
      },
    },
    signedIn: true,
    jobs: [],
    runs: [],
    snapshots: {},
    approvals: [],
    audit: [],
    sims: {},
    seeds: {},
    counter: 1000,
    configLoadedAt: iso(nowMs - 3 * DAY - 5 * HOUR - 12 * MIN),
  }

  let seedN = 1
  for (const seed of seeds) {
    const name = seed.detail.name
    db.seeds[name] = seed
    const slots: number[] = []
    for (let d = 40 + Math.ceil((seed.history ?? 0) / (1 + (seed.extraHours?.length ?? 0))); d >= 0; d -= 1) {
      const dayStart = day0 - d * DAY
      slots.push(dayStart + seed.hour * HOUR + seed.minute * MIN)
      for (const h of seed.extraHours ?? []) slots.push(dayStart + h * HOUR + seed.minute * MIN)
    }
    const finishedSlots = slots.filter((s) => s <= nowMs - 2 * MIN)
    const past = finishedSlots.slice(-30)
    const older = seed.history ? finishedSlots.slice(-30 - seed.history, -30) : []
    older.forEach((start, k) => {
      seedN += 1
      const i = k - older.length
      const failed = seed.historyFailAt?.includes(k) ?? false
      const jitter = (Number.parseInt(hex(seedN * 31, 4), 16) % 41) * 1000
      const duration = Math.round(seed.durationMs + (i / 30) * 0.2 * seed.durationMs + jitter)
      const bytes = Math.round(seed.baseBytes + seed.growth * i + ((seedN * 104729) % 23) * 0.002 * GIB)
      const run: Run = {
        id: `run_${hex(seedN, 8)}`,
        job: name,
        op: 'backup',
        state: failed ? 'failed' : 'succeeded',
        trigger: 'schedule',
        requested_by: scheduler,
        queued_at: iso(start),
        started_at: iso(start),
      }
      if (failed) {
        run.error = seed.historyError ?? 'the run failed'
        run.duration_ms = 30_000 + (seedN % 7) * 1000
      } else {
        run.duration_ms = duration
        run.snapshot = snapshotId(start, seedN)
        run.stored_bytes = bytes
      }
      run.finished_at = iso(start + (run.duration_ms ?? 0))
      run.log_tail = seedRunLog(name, 'backup', run.snapshot, run.state, start, run.duration_ms ?? 0, run.error)
      db.runs.push(run)
    })
    if (name === 'billing-db') {
      past.shift()
      past.push(nowMs - 3 * MIN)
    }
    const snaps: SnapshotSummary[] = []
    let lastSuccess: number | undefined
    let lastFailure: number | undefined
    let prevSnap: string | undefined
    past.forEach((start, i) => {
      seedN += 1
      const isLast = i === past.length - 1
      const running = name === 'billing-db' && isLast
      const failed = seed.failAt.includes(i)
      const jitter = (Number.parseInt(hex(seedN * 31, 4), 16) % 41) * 1000
      const duration = Math.round(seed.durationMs + (i / 30) * 0.2 * seed.durationMs + jitter)
      const bytes = Math.round(seed.baseBytes + seed.growth * i * (seed.extraHours ? 0.5 : 1))
      const runId = `run_${hex(seedN, 8)}`
      const state: RunState = running ? 'running' : failed ? 'failed' : 'succeeded'
      const snapId = failed ? undefined : snapshotId(start, seedN)
      const run: Run = {
        id: runId,
        job: name,
        op: 'backup',
        state,
        trigger: 'schedule',
        requested_by: scheduler,
        queued_at: iso(start),
        started_at: iso(start),
      }
      if (running) {
        run.snapshot = snapshotId(start, seedN)
        db.sims[runId] = { startMs: start, durationMs: 6 * MIN, outcome: 'succeeded', finalized: false }
      } else {
        run.finished_at = iso(start + duration)
        run.duration_ms = duration
        if (failed) {
          run.error = name === 'shop-db' ? SHOP_ERROR : 'object listing failed: connection reset by peer'
          run.duration_ms = 4_000 + (seedN % 5) * 1000
          run.finished_at = iso(start + run.duration_ms)
          lastFailure = start
        } else {
          run.snapshot = snapId
          run.stored_bytes = bytes
          lastSuccess = start
        }
      }
      run.log_tail = seedRunLog(name, 'backup', run.snapshot, state, start, run.duration_ms ?? 0, run.error)
      db.runs.push(run)
      if (snapId && !running) {
        const arts = seed.artifacts(bytes)
        snaps.push({
          id: snapId,
          started_at: iso(start),
          finished_at: iso(start + duration),
          duration_ms: duration,
          stored_bytes: arts.reduce((a, b) => a + b.stored, 0),
          plain_bytes: arts.reduce((a, b) => a + b.plain, 0),
          artifacts: arts.length,
          depends_on: seed.incremental && prevSnap ? [prevSnap] : undefined,
          kept_by: [],
          lock_until: iso(start + duration + 7 * DAY),
        })
        prevSnap = snapId
      }
    })
    snaps.reverse()
    applyRetention(snaps, seed.detail.retention, nowMs)
    const kept = snaps.filter(
      (s) =>
        s.kept_by.length > 0 ||
        snaps.indexOf(s) < 3 ||
        !seed.detail.prune_after_backup ||
        (s.lock_until !== undefined && new Date(s.lock_until).getTime() > nowMs),
    )
    const verifyLag = seed.verifyLagDays ?? 0
    let firstEligible = true
    for (const s of kept) {
      const started = new Date(s.started_at).getTime()
      let vAt = localDayStart(started) + (seed.verifyHour ?? 3) * HOUR + (seed.verifyMinute ?? 0) * MIN + 61_000
      if (vAt < new Date(s.finished_at).getTime()) vAt += DAY
      const age = Math.floor((day0 - localDayStart(started)) / DAY)
      if (vAt > nowMs || age < verifyLag) continue
      const take = firstEligible || age % seed.verifyEveryDays === 0
      firstEligible = false
      if (!take) continue
      s.last_verify = { status: 'succeeded', at: iso(vAt) }
    }
    if (name === 'shop-db') {
      const olderVerified = kept.find((s) => s.last_verify)
      kept.forEach((s) => {
        if (s !== olderVerified) delete s.last_verify
      })
    }
    db.snapshots[name] = kept

    const verifyRuns = kept.filter((s) => s.last_verify).slice(0, 6)
    for (const s of verifyRuns) {
      seedN += 1
      const at = new Date(s.last_verify!.at).getTime() - 61_000
      db.runs.push({
        id: `run_${hex(seedN, 8)}`,
        job: name,
        op: 'verify',
        state: 'succeeded',
        trigger: 'schedule',
        requested_by: scheduler,
        snapshot: s.id,
        queued_at: iso(at),
        started_at: iso(at),
        finished_at: iso(at + 61_000),
        duration_ms: 61_000,
        log_tail: seedRunLog(name, 'verify', s.id, 'succeeded', at, 61_000),
      })
    }

    const detail: JobDetail = {
      ...seed.detail,
      health: 'healthy',
      snapshots: kept.length,
      stored_bytes: kept.reduce((a, b) => a + b.stored_bytes, 0),
      recent_backups: [],
    }
    if (lastSuccess) detail.last_success = iso(lastSuccess)
    if (lastFailure) {
      detail.last_failure = iso(lastFailure)
      detail.last_failure_error = name === 'shop-db' ? SHOP_ERROR : 'object listing failed: connection reset by peer'
    }
    db.jobs.push(detail)
  }

  seedDrills(db, day0, nowMs)
  seedHumanActivity(db, day0, nowMs)
  refreshDerived(db)
  return db
}

function applyRetention(snaps: SnapshotSummary[], retention: JobDetail['retention'], nowMs: number) {
  const tzDay = (iso: string) => Math.floor((new Date(iso).getTime() + TZ_OFFSET_MS) / DAY)
  if (retention.last) snaps.slice(0, retention.last).forEach((s) => s.kept_by.push('last'))
  const bucket = (rule: 'daily' | 'weekly' | 'monthly', n: number | undefined, key: (s: SnapshotSummary) => number) => {
    if (!n) return
    const seen = new Set<number>()
    for (const s of snaps) {
      const k = key(s)
      if (seen.has(k)) continue
      if (seen.size >= n) break
      seen.add(k)
      s.kept_by.push(rule)
    }
  }
  bucket('daily', retention.daily, (s) => tzDay(s.started_at))
  bucket('weekly', retention.weekly, (s) => Math.floor((tzDay(s.started_at) + 3) / 7))
  bucket('monthly', retention.monthly, (s) => {
    const d = new Date(new Date(s.started_at).getTime() + TZ_OFFSET_MS)
    return d.getUTCFullYear() * 12 + d.getUTCMonth()
  })
  for (const s of snaps) {
    if (s.lock_until && new Date(s.lock_until).getTime() < nowMs - 30 * DAY) delete s.lock_until
  }
}

type TableSeed = [database: string, name: string, rows: number]

const DRILL_TABLES: Record<string, TableSeed[]> = {
  'orders-db': [
    ['orders', 'order_events', 486_212],
    ['orders', 'order_items', 312_904],
    ['orders', 'orders', 128_377],
    ['orders', 'payments', 126_950],
    ['orders', 'shipments', 98_410],
    ['orders', 'addresses', 52_118],
    ['orders', 'customers', 41_207],
    ['orders', 'product_prices', 9_840],
    ['orders', 'refunds', 6_815],
    ['orders', 'products', 3_412],
    ['orders', 'coupons', 1_204],
    ['orders', 'tax_rates', 212],
    ['orders', 'schema_migrations', 187],
    ['orders', 'carriers', 18],
  ],
  'billing-db': [
    ['ledger', 'entries', 4_218_660],
    ['billing', 'invoice_lines', 1_904_331],
    ['billing', 'invoices', 402_118],
    ['billing', 'payments', 389_904],
    ['billing', 'subscriptions', 61_420],
    ['ledger', 'accounts', 12_406],
    ['billing', 'plans', 48],
    ['ledger', 'periods', 37],
    ['billing', 'schema_migrations', 96],
    ['ledger', 'schema_migrations', 41],
  ],
}

type DrillPlan = { at: number; outcome: 'passed' | 'failed' }

function drillPlans(job: string, day0: number, nowMs: number): DrillPlan[] {
  const weekday = (ms: number) => new Date(ms + TZ_OFFSET_MS).getUTCDay()
  const out: DrillPlan[] = []
  if (job === 'orders-db' || job === 'billing-db') {
    const dow = job === 'orders-db' ? 0 : 1
    const hour = job === 'orders-db' ? 5 : 6
    for (let d = 0; d < 60 && out.length < 7; d += 1) {
      const start = day0 - d * DAY
      if (weekday(start) !== dow) continue
      const at = start + hour * HOUR
      if (at > nowMs - 10 * MIN) continue
      out.push({ at, outcome: 'passed' })
    }
    out.reverse()
    if (job === 'orders-db' && out.length > 4) out[out.length - 4]!.outcome = 'failed'
    if (job === 'billing-db' && out.length) out[out.length - 1]!.outcome = 'failed'
  }
  if (job === 'uploads') {
    for (let m = 0; m < 3; m += 1) {
      const n = new Date(nowMs + TZ_OFFSET_MS)
      const at = Date.UTC(n.getUTCFullYear(), n.getUTCMonth() - m, 1) - TZ_OFFSET_MS + 6 * HOUR
      if (at < nowMs - 10 * MIN) out.unshift({ at, outcome: 'passed' })
    }
  }
  return out
}

function snapshotBefore(db: MockDb, job: string, at: number): { id: string; bytes: number } | null {
  const runs = db.runs
    .filter((r) => r.job === job && r.op === 'backup' && r.state === 'succeeded' && r.snapshot && new Date(r.finished_at ?? 0).getTime() <= at)
    .sort((a, b) => (b.finished_at ?? '').localeCompare(a.finished_at ?? ''))
  const r = runs[0]
  return r?.snapshot ? { id: r.snapshot, bytes: r.stored_bytes ?? GIB } : null
}

export function buildDrillResult(
  db: MockDb,
  run: Run,
  outcome: 'passed' | 'failed',
  startMs: number,
  durationMs: number,
): DrillResult {
  const job = db.jobs.find((j) => j.name === run.job)
  const seed = db.seeds[run.job]
  const target = job?.drill?.target ?? 'staging'
  const suffix = job?.drill?.restore_suffix ?? '_drill'
  const snap = (run.snapshot
    ? { id: run.snapshot, bytes: (db.snapshots[run.job] ?? []).find((s) => s.id === run.snapshot)?.stored_bytes ?? GIB }
    : snapshotBefore(db, run.job, startMs)) ?? { id: snapshotId(startMs - 4 * HOUR, Math.floor(startMs / 1000) % 9973), bytes: Math.round(seed?.baseBytes ?? GIB) }
  const ageDays = Math.max(0, (db.now() - startMs) / DAY)
  const scale = 1 - ageDays * 0.0012
  const seedTables = DRILL_TABLES[run.job] ?? []
  const tables: DrillTable[] = seedTables.map(([database, name, rows]) => ({
    database,
    name,
    rows: run.job === 'billing-db' && outcome === 'failed' && name === 'entries' ? 0 : Math.round(rows * scale),
  }))
  tables.sort((a, b) => b.rows - a.rows)
  const checks: DrillCheck[] = []
  const restoreBytes = Math.round(snap.bytes * 3.4)
  if (job?.driver === 's3') {
    const objects = 2359 - Math.round(ageDays * 2)
    checks.push(
      { name: `Restore into ${target}`, kind: 'restore', status: 'passed', expected: 'All objects written', actual: `${objects.toLocaleString('en-US')} objects written` },
      { name: 'Object count matches the manifest', kind: 'objects', status: 'passed', expected: objects.toLocaleString('en-US'), actual: objects.toLocaleString('en-US') },
      { name: 'Checksums of a sample of 200 objects', kind: 'objects', status: 'passed', expected: '200 match', actual: '200 match' },
      { name: `Delete the ${suffix} prefix`, kind: 'cleanup', status: 'passed', expected: 'Deleted', actual: 'Deleted' },
    )
  } else {
    const dbs = (job?.source.databases ?? 'app').split(',')
    checks.push({
      name: `Restore ${dbs.map((d) => `${d}${suffix}`).join(', ')} into ${target}`,
      kind: 'restore',
      status: 'passed',
      expected: `${dbs.length} ${dbs.length === 1 ? 'database' : 'databases'}`,
      actual: `${dbs.length} restored`,
    })
    checks.push({ name: 'Table count', kind: 'tables', status: 'passed', expected: `at least ${seedTables.length}`, actual: String(tables.length) })
    if (run.job === 'orders-db') {
      const orders = tables.find((t) => t.name === 'orders')?.rows ?? 0
      checks.push({ name: 'orders.orders has rows', kind: 'rows', status: 'passed', expected: 'at least 100,000', actual: orders.toLocaleString('en-US') })
      checks.push(
        outcome === 'failed'
          ? {
              name: 'Orders in the 24 hours before the snapshot',
              kind: 'sql',
              status: 'failed',
              expected: 'more than 0',
              actual: '0',
              message: 'the snapshot was taken while the order service was paused',
            }
          : { name: 'Orders in the 24 hours before the snapshot', kind: 'sql', status: 'passed', expected: 'more than 0', actual: (1700 + Math.round(ageDays * 7)).toLocaleString('en-US') },
      )
      checks.push({ name: 'No orphaned order items', kind: 'sql', status: 'passed', expected: '0', actual: '0' })
    } else if (run.job === 'billing-db') {
      const entries = tables.find((t) => t.name === 'entries')?.rows ?? 0
      checks.push(
        outcome === 'failed'
          ? { name: 'ledger.entries has rows', kind: 'rows', status: 'failed', expected: 'at least 1,000,000', actual: '0', message: 'the table exists but is empty after the restore' }
          : { name: 'ledger.entries has rows', kind: 'rows', status: 'passed', expected: 'at least 1,000,000', actual: entries.toLocaleString('en-US') },
      )
      checks.push({
        name: 'Ledger balances to zero',
        kind: 'sql',
        status: outcome === 'failed' ? 'skipped' : 'passed',
        expected: '0.00',
        actual: outcome === 'failed' ? undefined : '0.00',
        message: outcome === 'failed' ? 'skipped after a failed check' : undefined,
      })
    }
    checks.push({ name: `Drop the ${suffix} databases`, kind: 'cleanup', status: 'passed', expected: 'Dropped', actual: 'Dropped' })
  }
  const failed = checks.find((c) => c.status === 'failed')
  return {
    run_id: run.id,
    job: run.job,
    snapshot: snap.id,
    target,
    state: failed ? 'failed' : 'succeeded',
    trigger: run.trigger,
    requested_by: run.requested_by,
    started_at: iso(startMs),
    finished_at: iso(startMs + durationMs),
    duration_ms: durationMs,
    restored_bytes: job?.driver === 's3' ? Math.round((seed?.baseBytes ?? GIB) * 1.02) : restoreBytes,
    checks,
    tables: job?.driver === 's3' ? undefined : tables,
    cleaned_up: true,
    error: failed ? `check "${failed.name}" failed: expected ${failed.expected}, got ${failed.actual}` : undefined,
  }
}

function drillLog(result: DrillResult): string[] {
  const t0 = stampAt(new Date(result.started_at).getTime())
  const t1 = stampAt(new Date(result.finished_at ?? result.started_at).getTime())
  return [
    `${t0}  INFO  drill started  job=${result.job} snapshot=${result.snapshot} target=${result.target}`,
    ...result.checks.map((c) => `${t1}  ${c.status === 'failed' ? 'ERROR' : 'INFO '} check ${c.kind} ${c.status}  name="${c.name}"${c.actual ? ` actual="${c.actual}"` : ''}`),
    `${t1}  ${result.state === 'failed' ? 'ERROR drill failed ' : 'INFO  drill passed '} job=${result.job}`,
  ]
}

function seedDrills(db: MockDb, day0: number, nowMs: number) {
  let n = 9000
  for (const job of db.jobs) {
    if (!job.drill) continue
    for (const plan of drillPlans(job.name, day0, nowMs)) {
      n += 1
      const durationMs = (job.driver === 's3' ? 9 * MIN : job.name === 'billing-db' ? 6 * MIN : 4 * MIN) + ((n * 7919) % 50) * 1000
      const run: Run = {
        id: `run_${hex(n, 8)}`,
        job: job.name,
        op: 'drill',
        state: 'succeeded',
        trigger: 'schedule',
        requested_by: scheduler,
        target: job.drill.target,
        queued_at: iso(plan.at),
        started_at: iso(plan.at),
      }
      const result = buildDrillResult(db, run, plan.outcome, plan.at, durationMs)
      run.snapshot = result.snapshot
      run.state = result.state
      run.finished_at = result.finished_at
      run.duration_ms = durationMs
      run.drill = result
      run.error = result.error
      run.log_tail = drillLog(result)
      db.runs.push(run)
      if (nowMs - plan.at < 14 * DAY) {
        addAudit(db, {
          time: result.finished_at!,
          actor: scheduler,
          action: 'run.finish',
          result: result.state === 'succeeded' ? 'succeeded' : 'failed',
          job: job.name,
          snapshot: result.snapshot,
          target: result.target,
          run_id: run.id,
          error: result.error,
          summary: `${job.name} restore drill on schedule ${result.state === 'succeeded' ? 'passed' : 'failed'}`,
        })
      }
    }
  }
}

export function drillResults(db: MockDb, job: string): DrillResult[] {
  return db.runs
    .filter((r) => r.job === job && r.op === 'drill' && r.drill)
    .map((r) => r.drill!)
    .sort((a, b) => b.started_at.localeCompare(a.started_at))
}

function drillAlert(db: MockDb, job: JobDetail, nowMs: number): Alert | null {
  if (!job.drill) return null
  const results = drillResults(db, job.name)
  const newest = results[0]
  if (newest && newest.state === 'failed') {
    return { kind: 'drill_failing', job: job.name, message: newest.error ?? 'The last restore drill failed.', since: newest.finished_at ?? newest.started_at }
  }
  const passed = results.find((r) => r.state === 'succeeded')
  const at = passed ? new Date(passed.finished_at ?? passed.started_at).getTime() : 0
  if (nowMs - at > job.drill.max_age_days * DAY) {
    return { kind: 'drill_stale', job: job.name, message: `No passed restore drill for more than ${job.drill.max_age_days} days.`, since: passed?.finished_at ?? iso(nowMs) }
  }
  return null
}

export function prunePreview(db: MockDb, jobName: string) {
  const nowMs = db.now()
  const snaps = db.snapshots[jobName] ?? []
  const items = snaps.map((s) => ({
    snapshot: s.id,
    finished_at: s.finished_at,
    action: (s.kept_by.length ? 'keep' : 'remove') as 'keep' | 'remove',
    reasons: [...s.kept_by],
    stored_bytes: s.stored_bytes,
    lock_until: s.lock_until,
  }))
  const removed = items.filter((i) => i.action === 'remove')
  const removedIds = new Set(removed.map((i) => i.snapshot))
  return {
    job: jobName,
    items,
    kept: items.length - removed.length,
    removed: removed.length,
    freed_bytes: removed.reduce((a, i) => a + (i.stored_bytes ?? 0), 0),
    locked: removed.filter((i) => i.lock_until && new Date(i.lock_until).getTime() > nowMs).length,
    run_records_removed: db.runs.filter((r) => r.job === jobName && r.snapshot && removedIds.has(r.snapshot) && r.op !== 'restore').length,
  }
}

function finalizePrune(db: MockDb, run: Run) {
  const preview = prunePreview(db, run.job)
  const nowMs = db.now()
  const removable = preview.items.filter((i) => i.action === 'remove' && !(i.lock_until && new Date(i.lock_until).getTime() > nowMs))
  const ids = new Set(removable.map((i) => i.snapshot))
  db.snapshots[run.job] = (db.snapshots[run.job] ?? []).filter((s) => !ids.has(s.id))
  const records = db.runs.filter((r) => r.job === run.job && r.snapshot && ids.has(r.snapshot) && r.op !== 'restore' && r.id !== run.id)
  const recordIds = new Set(records.map((r) => r.id))
  db.runs = db.runs.filter((r) => !recordIds.has(r.id))
  run.prune = {
    kept: preview.kept,
    removed: removable.length,
    errors: preview.locked,
    freed_bytes: removable.reduce((a, i) => a + (i.stored_bytes ?? 0), 0),
    run_records_removed: records.length,
  }
  if (preview.locked) run.error = `${preview.locked} snapshots are still under Object Lock and were not removed`
}

const CONFIG_LINES = [
  '# Cistern configuration, production',
  'repositories:',
  '  s3-main:',
  '    type: s3',
  '    bucket: cistern-backups',
  '    endpoint: https://s3.storage.internal',
  '    region: ap-southeast-1',
  '    access_key_id: ${S3_ACCESS_KEY_ID}',
  '    secret_access_key: ${S3_SECRET_ACCESS_KEY}',
  '    object_lock: { mode: COMPLIANCE, days: 7 }',
  '',
  'encryption:',
  '  recipients_file: /etc/cistern/recipients.txt',
  '',
  'scheduler:',
  '  timezone: Asia/Ho_Chi_Minh',
  '',
  'defaults:',
  '  repository: s3-main',
  '  timeout: 2h',
  '  prune_after_backup: true',
  '',
  'server:',
  '  name: production',
  '  listen: ":8480"',
  '  session_key: ${CISTERN_SESSION_KEY}',
  '  daemon: { url: "http://cistern-daemon:9465", token: "${CISTERN_DAEMON_TOKEN}" }',
  '  prune: { enabled: true }',
  '  oidc:',
  '    issuer: https://sso.example.com/realms/ops',
  '    client_id: cistern',
  '    client_secret: ${OIDC_CLIENT_SECRET}',
  '  roles:',
  '    operator: [backup-operators]',
  '    restorer: [dba]',
  '    approver: [dba-leads]',
  '    admin: [platform-admins]',
  '',
  'jobs:',
  '  - name: orders-db',
  '    driver: postgres',
  '    schedule: "0 2 * * *"',
  '    verify_schedule: "0 3 * * *"',
  '    retention: { daily: 7, weekly: 4, monthly: 6 }',
  '    params: { host: pg-orders.internal, user: backup, password: "${ORDERS_DB_PASSWORD}", databases: [orders] }',
  '    restore_targets:',
  '      - { name: staging, description: "staging-db.internal:5432", set: { host: staging-db.internal } }',
  '      - { name: production, description: "pg-orders.internal:5432", production: true }',
  '    drill:',
  '      schedule: "0 5 * * 0"',
  '      target: staging',
  '      max_age_days: 35',
  '      checks:',
  '        - { name: Orders in the 24 hours before the snapshot, sql: "SELECT count(*) FROM orders WHERE created_at > now() - interval \'1 day\'", expect: "> 0" }',
  '        - { name: No orphaned order items, sql: "SELECT count(*) FROM order_items i LEFT JOIN orders o ON o.id = i.order_id WHERE o.id IS NULL", expect: "= 0" }',
  '',
  '  - name: shop-db',
  '    driver: mysql',
  '    schedule: "30 2 * * *"',
  '    verify_schedule: "30 3 * * 0"',
  '    retention: { last: 3, daily: 7, weekly: 4 }',
  '    params: { host: mysql-shop.internal, user: backup, password: <redacted>, databases: [shop] }',
  '    restore_targets:',
  '      - { name: staging, description: "mysql-staging.internal:3306" }',
  '',
  '  - name: billing-db',
  '    driver: postgres',
  '    schedule: "0 2,14 * * *"',
  '    verify_schedule: "0 4 * * *"',
  '    retention: { last: 4, daily: 14, monthly: 12 }',
  '    params: { host: pg-billing.internal, user: backup, password: "${BILLING_DB_PASSWORD}", databases: [billing, ledger] }',
  '    restore_targets:',
  '      - { name: staging, description: "staging-db.internal:5432" }',
  '      - { name: production, description: "pg-billing.internal:5432", production: true }',
  '    drill:',
  '      schedule: "0 6 * * 1"',
  '      target: staging',
  '      checks:',
  '        - { name: Ledger balances to zero, sql: "SELECT sum(amount) FROM ledger.entries", expect: "= 0" }',
  '',
  '  - name: uploads',
  '    driver: s3',
  '    schedule: "0 3 * * *"',
  '    verify_schedule: "0 4 * * 6"',
  '    prune_after_backup: false',
  '    retention: { daily: 14, weekly: 8, monthly: 12 }',
  '    params: { bucket: app-uploads, endpoint: https://s3.storage.internal, access_key_id: "${UPLOADS_KEY_ID}", secret_access_key: "${UPLOADS_SECRET}" }',
  '    restore_targets:',
  '      - { name: restore-bucket, description: "Bucket app-uploads-restore", set: { bucket: app-uploads-restore } }',
  '      - { name: production, description: "Bucket app-uploads", production: true }',
  '    drill: { schedule: "0 6 1 * *", target: restore-bucket }',
  '',
  '  - name: users-db',
  '    driver: mysql',
  '    schedule: "45 1 * * *"',
  '    verify_schedule: "30 2 * * 0"',
  '    retention: { daily: 7, weekly: 4, monthly: 3 }',
  '    params: { host: mariadb-users.internal, user: backup, password: "${USERS_DB_PASSWORD}", databases: [users] }',
  '    restore_targets:',
  '      - { name: staging, description: "mysql-staging.internal:3306" }',
  '      - { name: production, description: "mariadb-users.internal:3306", production: true }',
]

export function configView(db: MockDb) {
  const line = CONFIG_LINES.findIndex((l) => l.includes('<redacted>')) + 1
  return {
    path: '/etc/cistern/cistern.yaml',
    loaded_at: db.configLoadedAt,
    yaml: CONFIG_LINES.join('\n') + '\n',
    literal_secrets: [{ path: 'jobs[shop-db].params.password', line }],
  }
}

function auditId(db: MockDb) {
  db.counter += 1
  return `aud_${db.counter}`
}

function addAudit(db: MockDb, e: Omit<AuditEntry, 'id'>) {
  db.audit.push({ id: auditId(db), ...e })
}

function rolesFor(actor: Actor): Role[] | undefined {
  return roleOf[actor.id]
}

function seedHumanActivity(db: MockDb, day0: number, nowMs: number) {
  const orders = db.snapshots['orders-db'] ?? []
  const latestOrders = orders[0]
  const billing = db.snapshots['billing-db'] ?? []
  const users = db.snapshots['users-db'] ?? []

  for (const r of db.runs) {
    if (r.trigger !== 'schedule' || r.state === 'running' || r.op === 'drill') continue
    const at = new Date(r.finished_at ?? r.started_at ?? 0).getTime()
    if (nowMs - at > 14 * DAY) continue
    const what =
      r.op === 'backup'
        ? r.state === 'succeeded'
          ? `${r.job} backup on schedule stored ${fmtSize(r.stored_bytes ?? 0)} as ${r.snapshot}`
          : `${r.job} backup on schedule failed: ${shortErr(r.error)}`
        : `${r.job} verify of ${r.snapshot} passed`
    addAudit(db, {
      time: iso(at),
      actor: scheduler,
      action: 'run.finish',
      result: r.state === 'succeeded' ? 'succeeded' : 'failed',
      job: r.job,
      snapshot: r.snapshot,
      run_id: r.id,
      error: r.error,
      summary: what,
    })
  }

  const billingRun = db.runs.find((r) => r.job === 'billing-db' && r.state === 'running')
  if (billingRun) {
    addAudit(db, {
      time: billingRun.started_at!,
      actor: scheduler,
      action: 'backup.run',
      result: 'accepted',
      job: 'billing-db',
      run_id: billingRun.id,
      summary: 'billing-db backup started on schedule',
    })
  }

  const restoreAt = day0 + 11 * HOUR + 5 * MIN
  if (latestOrders && restoreAt < nowMs) {
    const runId = 'run_5e0c71a2'
    db.runs.push({
      id: runId,
      job: 'orders-db',
      op: 'restore',
      state: 'succeeded',
      trigger: 'ui',
      requested_by: people.an,
      snapshot: latestOrders.id,
      target: 'staging',
      queued_at: iso(restoreAt - 4 * MIN - 40_000),
      started_at: iso(restoreAt - 4 * MIN - 40_000),
      finished_at: iso(restoreAt),
      duration_ms: 4 * MIN + 40_000,
      log_tail: [
        `INFO  restore started  job=orders-db snapshot=${latestOrders.id} target=staging`,
        'INFO  creating database  name=orders_restored host=staging-db.internal',
        'INFO  pg_restore finished  database=orders_restored',
        'INFO  restore finished  job=orders-db',
      ],
    })
    addAudit(db, {
      time: iso(restoreAt - 4 * MIN - 40_000),
      actor: people.an,
      roles: rolesFor(people.an),
      action: 'restore.run',
      result: 'accepted',
      job: 'orders-db',
      snapshot: latestOrders.id,
      target: 'staging',
      run_id: runId,
      reason: 'Check yesterday’s refund totals against the staging report',
      params: { restore_suffix: '_restored', host: 'staging-db.internal' },
      source_ip: '10.20.4.17',
      summary: `An Nguyen started a restore of orders-db into staging as orders_restored`,
    })
    addAudit(db, {
      time: iso(restoreAt),
      actor: people.an,
      roles: rolesFor(people.an),
      action: 'run.finish',
      result: 'succeeded',
      job: 'orders-db',
      snapshot: latestOrders.id,
      target: 'staging',
      run_id: runId,
      summary: 'An Nguyen restored orders-db into staging-db.internal as orders_restored, took 4m 40s',
    })
    addAudit(db, {
      time: iso(restoreAt - 18 * MIN),
      actor: people.an,
      roles: rolesFor(people.an),
      action: 'auth.login',
      result: 'succeeded',
      source_ip: '10.20.4.17',
      summary: 'An Nguyen signed in with single sign-on',
    })
  }

  if (latestOrders) {
    const asked = nowMs - 12 * MIN
    db.approvals.push({
      id: 'REQ-0193',
      state: 'pending',
      job: 'orders-db',
      snapshot: latestOrders.id,
      target: { name: 'production', description: 'pg-orders.internal:5432', production: true },
      restore_suffix: '_restored',
      reason: 'INC-4821: order rows deleted by a faulty migration at 09:40',
      requested_by: people.linh,
      requested_at: iso(asked),
      expires_at: iso(asked + 24 * HOUR),
    })
    addAudit(db, {
      time: iso(asked),
      actor: people.linh,
      roles: rolesFor(people.linh),
      action: 'restore.request',
      result: 'pending',
      job: 'orders-db',
      snapshot: latestOrders.id,
      target: 'production',
      approval_id: 'REQ-0193',
      reason: 'INC-4821: order rows deleted by a faulty migration at 09:40',
      params: { restore_suffix: '_restored' },
      source_ip: '10.20.6.41',
      summary: 'Linh Tran asked to restore orders-db into production as orders_restored',
    })
  }

  const b = billing[4]
  if (b) {
    const asked = nowMs - 6 * DAY - 3 * HOUR
    db.runs.push({
      id: 'run_9b1d0e44',
      job: 'billing-db',
      op: 'restore',
      state: 'succeeded',
      trigger: 'ui',
      requested_by: people.an,
      approval_id: 'REQ-0187',
      snapshot: b.id,
      target: 'production',
      queued_at: iso(asked + 22 * MIN),
      started_at: iso(asked + 22 * MIN),
      finished_at: iso(asked + 29 * MIN),
      duration_ms: 7 * MIN,
      log_tail: [`INFO  restore started  job=billing-db snapshot=${b.id} target=production`, 'INFO  restore finished  job=billing-db'],
    })
    db.approvals.push({
      id: 'REQ-0187',
      state: 'approved',
      job: 'billing-db',
      snapshot: b.id,
      target: { name: 'production', description: 'pg-billing.internal:5432', production: true },
      restore_suffix: '_fix',
      reason: 'INC-4790: invoice ledger rows double posted, restore beside the live database to compare',
      requested_by: people.an,
      requested_at: iso(asked),
      expires_at: iso(asked + 24 * HOUR),
      decided_by: people.minh,
      decided_at: iso(asked + 22 * MIN),
      note: 'Checked with finance, go ahead.',
      run_id: 'run_9b1d0e44',
    })
    addAudit(db, {
      time: iso(asked + 22 * MIN),
      actor: people.minh,
      roles: rolesFor(people.minh),
      action: 'approval.approve',
      result: 'succeeded',
      job: 'billing-db',
      snapshot: b.id,
      target: 'production',
      approval_id: 'REQ-0187',
      run_id: 'run_9b1d0e44',
      summary: 'Minh Le approved the restore of billing-db into production requested by An Nguyen',
    })
  }

  const u = users[1]
  if (u) {
    const asked = nowMs - 9 * DAY - 5 * HOUR
    db.approvals.push({
      id: 'REQ-0181',
      state: 'rejected',
      job: 'users-db',
      snapshot: u.id,
      target: { name: 'production', description: 'mariadb-users.internal:3306', production: true },
      restore_suffix: '_restored',
      reason: 'Support ticket 7712: user cannot log in after profile merge',
      requested_by: people.linh,
      requested_at: iso(asked),
      expires_at: iso(asked + 24 * HOUR),
      decided_by: people.huy,
      decided_at: iso(asked + 50 * MIN),
      note: 'One account does not need a full restore. Fix it from the staging copy instead.',
    })
    addAudit(db, {
      time: iso(asked + 50 * MIN),
      actor: people.huy,
      roles: rolesFor(people.huy),
      action: 'approval.reject',
      result: 'succeeded',
      job: 'users-db',
      approval_id: 'REQ-0181',
      reason: 'One account does not need a full restore. Fix it from the staging copy instead.',
      summary: 'Huy Pham rejected the restore of users-db into production requested by Linh Tran',
    })
  }

  const o = orders[2]
  if (o) {
    const asked = nowMs - 12 * DAY - 2 * HOUR
    db.approvals.push({
      id: 'REQ-0176',
      state: 'expired',
      job: 'orders-db',
      snapshot: o.id,
      target: { name: 'production', description: 'pg-orders.internal:5432', production: true },
      restore_suffix: '_audit',
      reason: 'Quarterly audit sample, compare order totals',
      requested_by: people.an,
      requested_at: iso(asked),
      expires_at: iso(asked + 24 * HOUR),
    })
    addAudit(db, {
      time: iso(asked + 24 * HOUR),
      actor: { id: 'cistern', name: 'Cistern' },
      action: 'approval.expire',
      result: 'failed',
      job: 'orders-db',
      approval_id: 'REQ-0176',
      summary: 'The request to restore orders-db into production expired without a decision',
    })
  }

  addAudit(db, {
    time: iso(nowMs - DAY - 7 * HOUR - 42 * MIN),
    actor: people.linh,
    roles: rolesFor(people.linh),
    action: 'access.denied',
    result: 'denied',
    job: 'users-db',
    target: 'production',
    source_ip: '10.20.6.41',
    summary: 'Linh Tran tried to restore users-db into production without an approval',
  })
  addAudit(db, {
    time: iso(nowMs - 2 * DAY - 9 * MIN),
    actor: { id: 'unknown', name: 'Unknown', email: 'contractor@example.net' },
    action: 'auth.denied',
    result: 'denied',
    source_ip: '10.20.9.3',
    summary: 'Single sign-on succeeded, but no group of contractor@example.net maps to a role',
  })
  addAudit(db, {
    time: iso(nowMs - 3 * DAY - 4 * HOUR),
    actor: people.huy,
    roles: rolesFor(people.huy),
    action: 'verify.run',
    result: 'accepted',
    job: 'uploads',
    source_ip: '10.20.5.8',
    summary: 'Huy Pham started a verify of the latest uploads snapshot',
  })
}

function fmtSize(bytes: number) {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let v = bytes
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i += 1
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}

function shortErr(err?: string) {
  if (!err) return 'unknown error'
  if (err.includes('1045')) return 'mysqldump error 1045, access denied'
  return err
}

export function cronNext(expr: string | undefined, fromMs: number): string | undefined {
  if (!expr) return undefined
  const f = expr.split(/\s+/)
  if (f.length !== 5) return undefined
  const minute = Number(f[0])
  const hours = (f[1] ?? '').split(',').map(Number)
  const dow = f[4] === '*' ? null : Number(f[4])
  for (let d = 0; d < 8; d += 1) {
    const start = localDayStart(fromMs) + d * DAY
    const weekday = new Date(start + TZ_OFFSET_MS).getUTCDay()
    if (dow !== null && weekday !== dow) continue
    for (const h of hours) {
      const t = start + h * HOUR + minute * MIN
      if (t > fromMs) return iso(t)
    }
  }
  return undefined
}

function computeHealth(job: JobDetail, nowMs: number): JobHealth {
  if (job.current_run && (job.current_run.state === 'running' || job.current_run.state === 'queued')) return 'running'
  const ls = job.last_success ? new Date(job.last_success).getTime() : 0
  const lf = job.last_failure ? new Date(job.last_failure).getTime() : 0
  if (!ls && !lf) return 'never_run'
  if (lf > ls) return 'failing'
  if (nowMs - ls > 36 * HOUR) return 'stale'
  if (job.drill) {
    const r = job.last_drill_result
    const last = job.last_drill
    if ((r && r.state === 'failed') || (last && last.state === 'failed')) return 'drill_failing'
    const passedAt = r && r.state === 'succeeded' ? new Date(r.finished_at ?? r.started_at).getTime() : 0
    if (nowMs - passedAt > job.drill.max_age_days * DAY) return 'drill_failing'
  }
  if (job.verify_schedule) {
    const lv = job.last_verify ? new Date(job.last_verify).getTime() : 0
    if (nowMs - lv > 8 * DAY) return 'verify_overdue'
  }
  return 'healthy'
}

export function tick(db: MockDb) {
  const nowMs = db.now()
  for (const run of db.runs) {
    const sim = db.sims[run.id]
    if (!sim || sim.finalized) continue
    const elapsed = nowMs - sim.startMs
    if (run.trigger === 'schedule') {
      const frac = Math.min(0.95, Math.max(0.05, elapsed / sim.durationMs))
      applyProgress(db, run, frac)
      continue
    }
    if (elapsed < 1500) {
      run.state = 'queued'
      continue
    }
    if (elapsed < sim.durationMs) {
      run.state = 'running'
      run.started_at = run.started_at ?? iso(sim.startMs + 1500)
      applyProgress(db, run, (elapsed - 1500) / (sim.durationMs - 1500))
      continue
    }
    finalizeRun(db, run, sim)
  }
  refreshDerived(db)
}

function applyProgress(db: MockDb, run: Run, frac: number) {
  const seed = db.seeds[run.job]
  const snaps = db.snapshots[run.job] ?? []
  const latest = snaps[0]
  const total = run.op === 'backup' ? Math.round((latest?.stored_bytes ?? 1 * GIB) * 1.01) : (snaps.find((s) => s.id === run.snapshot)?.stored_bytes ?? GIB)
  const artifactsTotal = seed ? seed.artifacts(total).length : 1
  const done = Math.min(artifactsTotal - (frac >= 1 ? 0 : 1), Math.floor(frac * artifactsTotal))
  run.progress = {
    artifacts_done: Math.max(0, done),
    artifacts_total: artifactsTotal,
    bytes_done: Math.round(total * frac),
    bytes_total: total,
  }
  const lines = run.log_tail ?? []
  const stamp = stampAt(db.now())
  const want = 2 + Math.floor(frac * 6)
  while (lines.length < want) {
    const n = lines.length
    lines.push(
      n === 0
        ? `${stamp}  INFO  ${run.op} started  job=${run.job}${run.snapshot ? ` snapshot=${run.snapshot}` : ''}`
        : `${stamp}  INFO  ${run.op === 'verify' ? 'checking' : 'streaming'} artifact  job=${run.job} bytes=${run.progress.bytes_done}`,
    )
  }
  run.log_tail = lines.slice(-200)
}

function finalizeRun(db: MockDb, run: Run, sim: Sim) {
  sim.finalized = true
  const nowMs = sim.startMs + sim.durationMs
  run.state = sim.outcome
  run.finished_at = iso(nowMs)
  run.duration_ms = sim.durationMs - 1500
  if (run.op === 'drill') {
    const result = buildDrillResult(db, run, sim.outcome === 'failed' ? 'failed' : 'passed', sim.startMs + 1500, sim.durationMs - 1500)
    run.drill = result
    run.snapshot = result.snapshot
    run.state = result.state
    run.error = result.error
    run.log_tail = [...(run.log_tail ?? []), ...drillLog(result).slice(1)].slice(-200)
    addAudit(db, {
      time: iso(nowMs),
      actor: run.requested_by ?? scheduler,
      roles: run.requested_by ? rolesFor(run.requested_by) : undefined,
      action: 'run.finish',
      result: result.state === 'succeeded' ? 'succeeded' : 'failed',
      job: run.job,
      snapshot: run.snapshot,
      target: run.target,
      run_id: run.id,
      error: run.error,
      summary: `${run.job} restore drill ${result.state === 'succeeded' ? 'passed' : 'failed'}`,
    })
    return
  }
  if (run.op === 'prune') {
    finalizePrune(db, run)
    const p = run.prune!
    run.log_tail = [
      ...(run.log_tail ?? []),
      `${stampAt(nowMs)}  INFO  prune finished  job=${run.job} kept=${p.kept} removed=${p.removed} errors=${p.errors}`,
    ].slice(-200)
    addAudit(db, {
      time: iso(nowMs),
      actor: run.requested_by ?? scheduler,
      roles: run.requested_by ? rolesFor(run.requested_by) : undefined,
      action: 'run.finish',
      result: 'succeeded',
      job: run.job,
      run_id: run.id,
      summary: `${run.job} prune removed ${p.removed} snapshots${p.errors ? `, ${p.errors} still locked` : ''}`,
    })
    return
  }
  const job = db.jobs.find((j) => j.name === run.job)
  const seed = db.seeds[run.job]
  const stamp = stampAt(nowMs)
  const finishedAt = iso(nowMs)
  const lines = run.log_tail ?? []
  if (sim.outcome === 'failed') {
    run.error = run.job === 'shop-db' ? SHOP_ERROR : 'the run failed'
    lines.push(`${stamp}  ERROR ${run.op} failed  job=${run.job} err="${run.error}"`)
    if (job && run.op === 'backup') {
      job.last_failure = run.finished_at
      job.last_failure_error = run.error
    }
  } else {
    lines.push(`${stamp}  INFO  ${run.op} finished  job=${run.job}`)
    if (run.progress) {
      run.progress.artifacts_done = run.progress.artifacts_total
      run.progress.bytes_done = run.progress.bytes_total
    }
    if (run.op === 'backup' && job && seed) {
      const snaps = db.snapshots[run.job] ?? []
      const latest = snaps[0]
      const bytes = Math.round((latest?.stored_bytes ?? GIB) * 1.003)
      const arts = seed.artifacts(bytes)
      const id = snapshotId(sim.startMs, db.counter++)
      run.snapshot = id
      run.stored_bytes = bytes
      snaps.unshift({
        id,
        started_at: iso(sim.startMs),
        finished_at: iso(nowMs),
        duration_ms: sim.durationMs,
        stored_bytes: arts.reduce((a, b) => a + b.stored, 0),
        plain_bytes: arts.reduce((a, b) => a + b.plain, 0),
        artifacts: arts.length,
        depends_on: seed.incremental && latest ? [latest.id] : undefined,
        kept_by: ['last'],
        lock_until: job.object_lock ? iso(nowMs + job.object_lock.days * DAY) : undefined,
      })
      db.snapshots[run.job] = snaps
      job.last_success = run.finished_at
    }
    if (run.op === 'verify' && job) {
      const s = (db.snapshots[run.job] ?? []).find((x) => x.id === run.snapshot)
      if (s) s.last_verify = { status: 'succeeded', at: finishedAt }
      job.last_verify = finishedAt
    }
  }
  run.log_tail = lines.slice(-200)
  addAudit(db, {
    time: finishedAt,
    actor: run.requested_by ?? scheduler,
    roles: run.requested_by ? rolesFor(run.requested_by) : undefined,
    action: 'run.finish',
    result: sim.outcome,
    job: run.job,
    snapshot: run.snapshot,
    target: run.target,
    run_id: run.id,
    error: run.error,
    summary: `${run.job} ${run.op} ${sim.outcome === 'succeeded' ? 'finished' : 'failed'}${run.error ? `: ${shortErr(run.error)}` : ''}`,
  })
}

export function refreshDerived(db: MockDb) {
  const nowMs = db.now()
  for (const job of db.jobs) {
    const runs = db.runs
      .filter((r) => r.job === job.name)
      .sort((a, b) => (a.queued_at ?? '').localeCompare(b.queued_at ?? ''))
    const backups = runs.filter((r) => r.op === 'backup')
    job.recent_backups = backups.slice(-30).map((r) => ({
      status: r.state,
      started_at: r.started_at ?? r.queued_at ?? iso(nowMs),
      duration_ms: r.duration_ms,
    }))
    const active = [...runs].reverse().find((r) => r.state === 'running' || r.state === 'queued')
    job.current_run = active ? structuredClone(active) : undefined
    job.running_op = active?.op
    const snaps = db.snapshots[job.name] ?? []
    job.snapshots = snaps.length
    job.stored_bytes = snaps.reduce((a, b) => a + b.stored_bytes, 0)
    const verified = snaps.map((s) => s.last_verify?.at).filter((x): x is string => !!x).sort()
    job.last_verify = verified.at(-1)
    job.next_run = active ? undefined : cronNext(job.schedule, nowMs)
    job.drill_schedule = job.drill?.schedule
    const drills = runs.filter((r) => r.op === 'drill')
    const newestDrill = drills.at(-1)
    job.last_drill = newestDrill ? { state: newestDrill.state, at: newestDrill.finished_at ?? newestDrill.started_at ?? newestDrill.queued_at ?? iso(nowMs) } : undefined
    const finished = [...drills].reverse().find((r) => r.drill)
    job.last_drill_result = finished?.drill ? structuredClone(finished.drill) : undefined
    job.health = computeHealth(job, nowMs)
  }
  for (const a of db.approvals) {
    if (a.state === 'pending' && new Date(a.expires_at).getTime() <= nowMs) a.state = 'expired'
    a.can_decide = a.state === 'pending' && a.requested_by.id !== db.me.user.id && db.me.roles.includes('approver')
  }
}

export function alertsFor(db: MockDb): Alert[] {
  const out: Alert[] = []
  for (const job of db.jobs) {
    if (job.health === 'failing') {
      out.push({
        kind: 'backup_failing',
        job: job.name,
        message: job.last_failure_error ?? 'The last backup failed.',
        since: job.last_failure ?? iso(db.now()),
      })
    } else if (job.health === 'stale') {
      out.push({ kind: 'backup_stale', job: job.name, message: 'No successful backup for more than 36 hours.', since: job.last_success ?? iso(db.now()) })
    } else if (job.health === 'verify_overdue') {
      out.push({
        kind: 'verify_stale',
        job: job.name,
        message: 'No successful verify for more than 8 days.',
        since: job.last_verify ?? iso(db.now()),
      })
    }
  }
  for (const job of db.jobs) {
    const a = drillAlert(db, job, db.now())
    if (a) out.push(a)
  }
  if (db.me.deployment.daemon.configured && !db.me.deployment.daemon.reachable) {
    out.push({ kind: 'daemon_unreachable', job: '', message: 'The daemon is not answering.', since: iso(db.now()) })
  }
  return out
}

export function nextRunId(db: MockDb) {
  db.counter += 1
  return `run_${hex(db.counter * 13, 8)}`
}

export function nextApprovalId(db: MockDb) {
  const max = db.approvals.reduce((m, a) => Math.max(m, Number(a.id.replace(/\D/g, '')) || 0), 193)
  return `REQ-${String(max + 1).padStart(4, '0')}`
}

export function startSimulatedRun(
  db: MockDb,
  init: Pick<Run, 'job' | 'op'> & Partial<Run>,
  outcome: 'succeeded' | 'failed',
  durationMs: number,
): Run {
  const nowMs = db.now()
  const run: Run = {
    id: nextRunId(db),
    state: 'queued',
    trigger: 'ui',
    requested_by: db.me.user,
    queued_at: iso(nowMs),
    log_tail: [],
    ...init,
  }
  db.runs.push(run)
  db.sims[run.id] = { startMs: nowMs, durationMs, outcome, finalized: false }
  return run
}

export function buildManifest(db: MockDb, job: string, snap: SnapshotSummary): Manifest {
  const seed = db.seeds[job]
  const detail = db.jobs.find((j) => j.name === job)
  const fixed = seed ? seed.artifacts(0).reduce((a, b) => a + b.stored, 0) : 0
  const arts = seed ? seed.artifacts(Math.max(0, snap.stored_bytes - fixed)) : []
  const n = Number.parseInt(snap.id.slice(-4), 16) || 7
  return {
    format: 1,
    id: snap.id,
    job,
    driver: detail?.driver ?? 'postgres',
    status: 'complete',
    started_at: snap.started_at,
    finished_at: snap.finished_at,
    source: {
      version: seed?.version,
      host: seed?.host,
      extra: detail?.driver === 'postgres' ? { pg_dump_version: '16.6' } : detail?.driver === 'mysql' ? { mysqldump_version: '8.4.3' } : { objects: '2359' },
    },
    compression: 'gzip',
    encryption: detail?.encryption.mode ?? 'none',
    recipients: ['age1qyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqs3290gq'],
    depends_on: snap.depends_on,
    artifacts: arts.map((a, i) => ({
      name: a.name,
      path: `artifacts/${a.name.replace(/ \(.*\)$/, '')}.gz.age`,
      plain_size: a.plain,
      stored_size: a.stored,
      plain_sha256: hash64(n * 17 + i),
      stored_sha256: hash64(n * 29 + i + 3),
    })),
    cistern_version: '0.3.0',
    hostname: 'cistern-7c9f4d-x2k8p',
  }
}
