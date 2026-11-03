import { queryOptions, type QueryClient } from '@tanstack/react-query'
import { api, setCsrfToken, unwrap, type Schemas } from './client'

export type Me = Schemas['Me']
export type Role = Schemas['Role']
export type Overview = Schemas['Overview']
export type JobSummary = Schemas['JobSummary']
export type JobDetail = Schemas['JobDetail']
export type JobHealth = Schemas['JobHealth']
export type RunPoint = Schemas['RunPoint']
export type Run = Schemas['Run']
export type RunState = Schemas['RunState']
export type Op = Schemas['Op']
export type SnapshotSummary = Schemas['SnapshotSummary']
export type Manifest = Schemas['Manifest']
export type Approval = Schemas['Approval']
export type ApprovalState = Schemas['ApprovalState']
export type AuditEntry = Schemas['AuditEntry']
export type AuditAction = Schemas['AuditAction']
export type AuditResult = Schemas['AuditResult']
export type Alert = Schemas['Alert']
export type RestoreTarget = Schemas['RestoreTarget']
export type RestoreInput = Schemas['RestoreInput']
export type RestorePreview = Schemas['RestorePreview']
export type Actor = Schemas['Actor']

export type RestoreRequest = RestoreInput & { reason: string; confirm_job: string }
export type RestoreResult = { needs_approval: boolean; run_id?: string; approval_id?: string }

export const ACTIVE_STATES: ReadonlySet<RunState> = new Set<RunState>(['queued', 'running'])

export function isActive(state: RunState | undefined | null) {
  return state != null && ACTIVE_STATES.has(state)
}

export const keys = {
  me: ['me'] as const,
  overview: ['overview'] as const,
  job: (job: string) => ['job', job] as const,
  snapshots: (job: string) => ['job', job, 'snapshots'] as const,
  manifest: (job: string, snapshot: string) => ['job', job, 'manifest', snapshot] as const,
  jobRuns: (job: string, op?: Op, limit?: number) => ['job', job, 'runs', op ?? 'all', limit ?? 30] as const,
  run: (id: string) => ['run', id] as const,
  approvals: (state: 'pending' | 'decided' | 'all') => ['approvals', state] as const,
  approval: (id: string) => ['approval', id] as const,
  audit: (filters: AuditFilters) => ['audit', filters] as const,
  preview: (job: string, input: RestoreInput) => ['job', job, 'preview', input] as const,
}

export const meQuery = queryOptions({
  queryKey: keys.me,
  queryFn: async () => {
    const me = await unwrap(api.GET('/me'))
    setCsrfToken(me.csrf_token)
    return me
  },
  staleTime: 5 * 60_000,
  refetchOnWindowFocus: true,
})

export const overviewQuery = queryOptions({
  queryKey: keys.overview,
  queryFn: () => unwrap(api.GET('/overview')),
  refetchInterval: 30_000,
})

export const jobQuery = (job: string) =>
  queryOptions({
    queryKey: keys.job(job),
    queryFn: () => unwrap(api.GET('/jobs/{job}', { params: { path: { job } } })),
    refetchInterval: (q) => (isActive(q.state.data?.current_run?.state) ? 2_000 : 30_000),
  })

export const snapshotsQuery = (job: string) =>
  queryOptions({
    queryKey: keys.snapshots(job),
    queryFn: async () => (await unwrap(api.GET('/jobs/{job}/snapshots', { params: { path: { job } } }))).snapshots,
    refetchInterval: 60_000,
  })

export const manifestQuery = (job: string, snapshot: string) =>
  queryOptions({
    queryKey: keys.manifest(job, snapshot),
    queryFn: () => unwrap(api.GET('/jobs/{job}/snapshots/{snapshot}', { params: { path: { job, snapshot } } })),
    staleTime: Infinity,
  })

export const jobRunsQuery = (job: string, op?: Op, limit = 30) =>
  queryOptions({
    queryKey: keys.jobRuns(job, op, limit),
    queryFn: async () =>
      (await unwrap(api.GET('/jobs/{job}/runs', { params: { path: { job }, query: { op, limit } } }))).runs,
  })

export const runQuery = (id: string) =>
  queryOptions({
    queryKey: keys.run(id),
    queryFn: () => unwrap(api.GET('/runs/{run}', { params: { path: { run: id } } })),
    refetchInterval: (q) => (isActive(q.state.data?.state) || !q.state.data ? 2_000 : false),
  })

export const approvalsQuery = (state: 'pending' | 'decided' | 'all' = 'all', limit = 50) =>
  queryOptions({
    queryKey: keys.approvals(state),
    queryFn: async () => (await unwrap(api.GET('/approvals', { params: { query: { state, limit } } }))).approvals,
    refetchInterval: 30_000,
  })

export const approvalQuery = (id: string) =>
  queryOptions({
    queryKey: keys.approval(id),
    queryFn: () => unwrap(api.GET('/approvals/{approval}', { params: { path: { approval: id } } })),
    refetchInterval: (q) => (q.state.data?.state === 'pending' ? 15_000 : false),
  })

export type AuditPeriod = '1' | '7' | '30' | '90'

export type AuditFilters = {
  q?: string
  action?: AuditAction
  job?: string
  result?: AuditResult
  period: AuditPeriod
  cursor?: string
}

export function auditRange(period: AuditPeriod, now: Date = new Date()) {
  const from = new Date(now.getTime() - Number(period) * 86_400_000)
  return { from: from.toISOString() }
}

export const auditQuery = (filters: AuditFilters, limit = 50) =>
  queryOptions({
    queryKey: keys.audit(filters),
    queryFn: () =>
      unwrap(
        api.GET('/audit', {
          params: {
            query: {
              ...auditRange(filters.period),
              q: filters.q || undefined,
              action: filters.action,
              job: filters.job || undefined,
              result: filters.result,
              cursor: filters.cursor,
              limit,
            },
          },
        }),
      ),
    placeholderData: (prev) => prev,
  })

export function auditExportUrl(filters: AuditFilters, now: Date = new Date()) {
  const params = new URLSearchParams({ format: 'csv', ...auditRange(filters.period, now) })
  if (filters.q) params.set('q', filters.q)
  if (filters.action) params.set('action', filters.action)
  if (filters.job) params.set('job', filters.job)
  if (filters.result) params.set('result', filters.result)
  return `/api/v1/audit/export?${params.toString()}`
}

export const previewQuery = (job: string, input: RestoreInput, enabled: boolean) =>
  queryOptions({
    queryKey: keys.preview(job, input),
    queryFn: () =>
      unwrap(api.POST('/jobs/{job}/restore/preview', { params: { path: { job } }, body: input })),
    enabled,
    staleTime: 30_000,
    retry: false,
    placeholderData: (prev) => prev,
  })

export function runBackup(job: string) {
  return unwrap(api.POST('/jobs/{job}/backup', { params: { path: { job } } }))
}

export function runVerify(job: string, snapshot = 'latest') {
  return unwrap(api.POST('/jobs/{job}/verify', { params: { path: { job } }, body: { snapshot } }))
}

export function startRestore(job: string, body: RestoreRequest): Promise<RestoreResult> {
  return unwrap(api.POST('/jobs/{job}/restore', { params: { path: { job } }, body }))
}

export function approve(id: string, note?: string) {
  return unwrap(
    api.POST('/approvals/{approval}/approve', {
      params: { path: { approval: id } },
      body: note ? { note } : {},
    }),
  )
}

export function reject(id: string, note: string) {
  return unwrap(api.POST('/approvals/{approval}/reject', { params: { path: { approval: id } }, body: { note } }))
}

export function logout() {
  return unwrap(api.POST('/auth/logout'))
}

export function invalidateAfterAction(client: QueryClient, job?: string) {
  void client.invalidateQueries({ queryKey: keys.overview })
  void client.invalidateQueries({ queryKey: ['approvals'] })
  void client.invalidateQueries({ queryKey: ['audit'] })
  if (job) void client.invalidateQueries({ queryKey: ['job', job] })
}
