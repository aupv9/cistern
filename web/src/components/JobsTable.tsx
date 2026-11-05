import { useId, useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import {
  columnFilteringFeature,
  columnVisibilityFeature,
  createColumnHelper,
  createFilteredRowModel,
  createSortedRowModel,
  globalFilteringFeature,
  metaHelper,
  rowSortingFeature,
  sortFn_alphanumeric,
  sortFn_basic,
  tableFeatures,
  useTable,
  type ColumnFiltersState,
  type SortingState,
  type ColumnVisibilityState,
} from '@tanstack/react-table'
import { ArrowDown, ArrowUp, ChevronsUpDown, Columns3 } from 'lucide-react'
import type { JobHealth, JobSummary, Me } from '~/api/queries'
import { Button } from '~/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '~/components/ui/dropdown-menu'
import { Input } from '~/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '~/components/ui/table'
import { ToggleGroup, ToggleGroupItem } from '~/components/ui/toggle-group'
import { formatBytes, formatElapsed, formatRelative, formatWhen } from '~/lib/format'
import { blockReason, can } from '~/lib/roles'
import { DRILL_STATE, HEALTH } from '~/lib/status'
import { cn } from '~/lib/utils'
import { SearchIcon } from './Icons'
import { RunStrip } from './RunStrip'
import { Status } from './Status'

export type JobFilter = 'all' | 'failing' | 'running' | 'drill' | 'verify'

export const JOB_FILTERS: { id: JobFilter; label: string; match: (j: JobSummary) => boolean }[] = [
  { id: 'all', label: 'All', match: () => true },
  { id: 'failing', label: 'Failing', match: (j) => j.health === 'failing' || j.health === 'stale' },
  { id: 'running', label: 'Running', match: (j) => j.health === 'running' || j.running_op != null },
  { id: 'drill', label: 'Drill failing', match: (j) => j.health === 'drill_failing' || j.last_drill?.state === 'failed' },
  { id: 'verify', label: 'Verify overdue', match: (j) => j.health === 'verify_overdue' },
]

export function matchesSearch(j: JobSummary, q: string): boolean {
  const needle = q.trim().toLowerCase()
  if (!needle) return true
  return [j.name, j.driver, j.source_label, j.repository].some((v) => v?.toLowerCase().includes(needle))
}

export function LastDrill({ job, tz, now }: { job: JobSummary; tz: string; now: Date }) {
  const d = job.last_drill
  if (!d) return <span className="muted">{job.drill_schedule ? 'Not yet' : 'No drill'}</span>
  const info = DRILL_STATE[d.state]
  const tone = info.tone === 'neutral' ? '' : info.tone
  return (
    <span className={`st ${tone}`} title={`${info.text}, ${formatWhen(d.at, tz, now)}`}>
      <span className="sr">{info.text}, </span>
      {d.state === 'running' || d.state === 'queued' ? info.text : formatRelative(d.at, now)}
    </span>
  )
}

function runningSince(j: JobSummary): string | undefined {
  const last = j.recent_backups.at(-1)
  if (last && (last.status === 'running' || last.status === 'queued')) return last.started_at
  return undefined
}

function isRunning(j: JobSummary): boolean {
  return j.health === 'running' || j.running_op != null
}

/** Ascending status order puts the jobs that need attention first. */
const HEALTH_RANK: Record<JobHealth, number> = {
  failing: 0,
  stale: 1,
  drill_failing: 2,
  verify_overdue: 3,
  running: 4,
  never_run: 5,
  healthy: 6,
}

function time(iso: string | null | undefined): number | undefined {
  return iso ? Date.parse(iso) : undefined
}

type JobsMeta = {
  tz: string
  now: Date
  busyJob: string | null
  backupBlocked: string | null
  onRunBackup?: (job: string) => void
}

const features = tableFeatures({
  columnVisibilityFeature,
  columnFilteringFeature,
  globalFilteringFeature,
  rowSortingFeature,
  filteredRowModel: createFilteredRowModel(),
  sortedRowModel: createSortedRowModel(),
  sortFns: { alphanumeric: sortFn_alphanumeric, basic: sortFn_basic },
  tableMeta: metaHelper<JobsMeta>(),
})

const col = createColumnHelper<typeof features, JobSummary>()

const ALL_COLUMNS = col.columns([
  col.accessor('name', {
    header: 'Job',
    sortFn: 'alphanumeric',
    enableHiding: false,
    cell: ({ row }) => (
      <div className="cell-name">
        <Link to="/jobs/$job" params={{ job: row.original.name }}>
          {row.original.name}
        </Link>
        <span className="sub">{row.original.source_label ?? row.original.driver}</span>
      </div>
    ),
  }),
  col.accessor((j) => HEALTH_RANK[j.health], {
    id: 'status',
    header: 'Status',
    sortFn: 'basic',
    sortDescFirst: false,
    filterFn: (row, _id, value: JobFilter) => (JOB_FILTERS.find((f) => f.id === value) ?? JOB_FILTERS[0]!).match(row.original),
    cell: ({ row }) => <Status info={HEALTH[row.original.health]} />,
  }),
  col.display({
    id: 'runs',
    header: 'Last 30 runs',
    cell: ({ row, table }) => <RunStrip points={row.original.recent_backups} timeZone={table.options.meta!.tz} />,
  }),
  col.accessor((j) => time(j.last_success), {
    id: 'last_success',
    header: 'Last success',
    sortFn: 'basic',
    sortUndefined: 'last',
    cell: ({ row, table }) => {
      const { tz, now } = table.options.meta!
      return row.original.last_success ? formatWhen(row.original.last_success, tz, now) : 'Never'
    },
  }),
  col.accessor((j) => time(j.last_verify), {
    id: 'last_verify',
    header: 'Last verify',
    sortFn: 'basic',
    sortUndefined: 'last',
    cell: ({ row, table }) => {
      const { tz, now } = table.options.meta!
      const j = row.original
      return j.last_verify ? formatWhen(j.last_verify, tz, now) : j.verify_schedule ? 'Never' : 'Not scheduled'
    },
  }),
  col.accessor((j) => time(j.last_drill?.at), {
    id: 'last_drill',
    header: 'Last drill',
    sortFn: 'basic',
    sortUndefined: 'last',
    cell: ({ row, table }) => <LastDrill job={row.original} tz={table.options.meta!.tz} now={table.options.meta!.now} />,
  }),
  col.accessor((j) => time(j.next_run), {
    id: 'next_run',
    header: 'Next run',
    sortFn: 'basic',
    sortDescFirst: false,
    sortUndefined: 'last',
    cell: ({ row, table }) => {
      const { tz, now } = table.options.meta!
      const j = row.original
      if (isRunning(j)) {
        const since = runningSince(j)
        if (since) return `Running for ${formatElapsed(since, now)}`
        return `${j.running_op ? j.running_op.charAt(0).toUpperCase() + j.running_op.slice(1) : 'Run'} in progress`
      }
      return j.next_run ? formatWhen(j.next_run, tz, now) : j.schedule ? '–' : 'Not scheduled'
    },
  }),
  col.accessor('stored_bytes', {
    header: 'Stored',
    sortFn: 'basic',
    cell: ({ getValue }) => formatBytes(getValue()),
  }),
  col.display({
    id: 'actions',
    header: () => <span className="sr">Actions</span>,
    enableHiding: false,
    cell: ({ row, table }) => {
      const { busyJob, backupBlocked, onRunBackup } = table.options.meta!
      const j = row.original
      const running = isRunning(j)
      return (
        <Button
          variant="outline"
          size="sm"
          className="h-8 px-2.5 text-[13px] pointer-coarse:h-11"
          aria-label={`Run backup of ${j.name}`}
          title={backupBlocked ?? (running ? 'A run is already in progress' : undefined)}
          disabled={backupBlocked !== null || running || busyJob === j.name}
          onClick={() => onRunBackup?.(j.name)}
        >
          {busyJob === j.name ? 'Starting…' : 'Run backup'}
        </Button>
      )
    },
  }),
])

const RIGHT_ALIGNED = new Set(['stored_bytes', 'actions'])

const COLUMNS_KEY = 'cistern.jobs.columns'

function loadVisibility(): ColumnVisibilityState {
  try {
    const raw = window.localStorage.getItem(COLUMNS_KEY)
    const v: unknown = raw ? JSON.parse(raw) : null
    return v && typeof v === 'object' ? (v as ColumnVisibilityState) : {}
  } catch {
    return {}
  }
}

function saveVisibility(v: ColumnVisibilityState) {
  try {
    window.localStorage.setItem(COLUMNS_KEY, JSON.stringify(v))
  } catch {
    // Private mode or blocked storage: the choice just won't survive a reload.
  }
}

function SortIcon({ dir }: { dir: false | 'asc' | 'desc' }) {
  const Icon = dir === 'asc' ? ArrowUp : dir === 'desc' ? ArrowDown : ChevronsUpDown
  return <Icon aria-hidden="true" className={cn('size-3.5', dir ? 'text-foreground' : 'opacity-60')} />
}

export function JobsTable({
  jobs,
  me,
  now,
  title = 'Jobs',
  headingLevel = 2,
  onRunBackup,
  busyJob,
}: {
  jobs: readonly JobSummary[]
  me: Me
  now: Date
  title?: string
  headingLevel?: 2 | 3
  onRunBackup?: (job: string) => void
  busyJob?: string | null
}) {
  const [filter, setFilter] = useState<JobFilter>('all')
  const [q, setQ] = useState('')
  const [sorting, setSorting] = useState<SortingState>([])
  const [visibility, setVisibility] = useState<ColumnVisibilityState>(loadVisibility)
  const headingId = useId()
  const searchId = useId()
  const tz = me.deployment.timezone
  const showActions = can(me.roles, 'backup') && onRunBackup != null
  const Heading = headingLevel === 2 ? 'h2' : 'h3'
  const drills = me.deployment.features.drills || jobs.some((j) => j.last_drill || j.drill_schedule)
  const filters = drills ? JOB_FILTERS : JOB_FILTERS.filter((f) => f.id !== 'drill')

  const columns = useMemo(
    () => ALL_COLUMNS.filter((c) => (c.id !== 'last_drill' || drills) && (c.id !== 'actions' || showActions)),
    [drills, showActions],
  )
  const columnFilters = useMemo<ColumnFiltersState>(() => (filter === 'all' ? [] : [{ id: 'status', value: filter }]), [filter])
  const data = jobs as JobSummary[]

  const table = useTable({
    features,
    columns,
    data,
    getRowId: (j) => j.name,
    meta: { tz, now, busyJob: busyJob ?? null, backupBlocked: blockReason(me, 'backup'), onRunBackup },
    state: { sorting, columnVisibility: visibility, columnFilters, globalFilter: q },
    onSortingChange: setSorting,
    onColumnVisibilityChange: (updater) =>
      setVisibility((prev) => {
        const next = typeof updater === 'function' ? updater(prev) : updater
        saveVisibility(next)
        return next
      }),
    onColumnFiltersChange: (updater) => {
      const next = typeof updater === 'function' ? updater(columnFilters) : updater
      setFilter((next.find((f) => f.id === 'status')?.value as JobFilter | undefined) ?? 'all')
    },
    onGlobalFilterChange: (updater) => setQ(typeof updater === 'function' ? updater(q) : updater),
    // The search box matches several fields of a job, so it runs once per row on the name column.
    getColumnCanGlobalFilter: (column) => column.id === 'name',
    globalFilterFn: (row, _id, value: string) => matchesSearch(row.original, value),
  })

  const rows = table.getRowModel().rows
  const hideable = table.getAllLeafColumns().filter((c) => c.getCanHide())

  return (
    <section aria-labelledby={headingId} className="card card-clip">
      <div className="hd">
        <Heading id={headingId} className="h2">
          {title}
        </Heading>
        <div className="row">
          <div className="relative flex items-center max-sm:w-full">
            <SearchIcon className="pointer-events-none absolute left-2.5 text-muted-foreground" />
            <label htmlFor={searchId} className="sr">
              Search jobs
            </label>
            <Input
              id={searchId}
              type="search"
              placeholder="Search jobs"
              className="h-9 w-[200px] bg-background pl-8 text-sm hover:border-(--border-2) max-sm:w-full pointer-coarse:h-11"
              value={q}
              onChange={(e) => table.setGlobalFilter(e.target.value)}
            />
          </div>
          <ToggleGroup
            type="single"
            aria-label="Filter by status"
            spacing={0}
            value={filter}
            onValueChange={(v) => {
              if (v) setFilter(v as JobFilter)
            }}
            className="flex-wrap gap-0.5 rounded-lg bg-muted p-[3px]"
          >
            {filters.map((f) => (
              <ToggleGroupItem
                key={f.id}
                value={f.id}
                className="h-[30px] rounded-md! border border-transparent px-2.5 text-[13px] hover:bg-transparent data-[state=on]:border-border data-[state=on]:bg-background data-[state=on]:shadow-xs pointer-coarse:h-11"
              >
                {f.label}
                <span className="ml-1 text-muted-foreground tabular-nums">{jobs.filter(f.match).length}</span>
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
          {hideable.length > 0 ? (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" className="h-9 gap-1.5 px-3 text-[13px] pointer-coarse:h-11">
                  <Columns3 aria-hidden="true" />
                  Columns
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-44">
                <DropdownMenuLabel>Show columns</DropdownMenuLabel>
                <DropdownMenuSeparator />
                {hideable.map((c) => (
                  <DropdownMenuCheckboxItem
                    key={c.id}
                    checked={c.getIsVisible()}
                    onCheckedChange={(v) => c.toggleVisibility(v === true)}
                    onSelect={(e) => e.preventDefault()}
                  >
                    {typeof c.columnDef.header === 'string' ? c.columnDef.header : c.id}
                  </DropdownMenuCheckboxItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          ) : null}
        </div>
      </div>
      <Table>
        <TableHeader>
          {table.getHeaderGroups().map((group) => (
            <TableRow key={group.id} className="hover:bg-transparent">
              {group.headers.map((header) => {
                const c = header.column
                const dir = c.getIsSorted()
                const right = RIGHT_ALIGNED.has(c.id)
                return (
                  <TableHead
                    key={header.id}
                    scope="col"
                    aria-sort={c.getCanSort() ? (dir === 'asc' ? 'ascending' : dir === 'desc' ? 'descending' : 'none') : undefined}
                    className={cn(right && 'text-right')}
                  >
                    {header.isPlaceholder ? null : c.getCanSort() ? (
                      <button
                        type="button"
                        className={cn(
                          '-mx-1 inline-flex cursor-pointer items-center gap-1 rounded-sm border-0 bg-transparent px-1 hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none',
                          right && 'flex-row-reverse',
                          dir && 'text-foreground',
                        )}
                        onClick={c.getToggleSortingHandler()}
                      >
                        <table.FlexRender header={header} />
                        <SortIcon dir={dir} />
                      </button>
                    ) : (
                      <table.FlexRender header={header} />
                    )}
                  </TableHead>
                )
              })}
            </TableRow>
          ))}
        </TableHeader>
        <TableBody aria-live="polite">
          {rows.map((row) => (
            <TableRow key={row.id} data-testid={`job-row-${row.original.name}`}>
              {row.getVisibleCells().map((cell) => (
                <TableCell key={cell.id} className={cn(RIGHT_ALIGNED.has(cell.column.id) && 'text-right')}>
                  <table.FlexRender cell={cell} />
                </TableCell>
              ))}
            </TableRow>
          ))}
          {rows.length === 0 ? (
            <TableRow className="hover:bg-transparent">
              <TableCell colSpan={table.getVisibleLeafColumns().length} className="px-4 py-10 text-center whitespace-normal">
                <div className="state" style={{ padding: 0 }}>
                  <span>{jobs.length === 0 ? 'No jobs are configured yet.' : 'No jobs match these filters.'}</span>
                  {jobs.length > 0 ? (
                    <Button
                      variant="outline"
                      className="h-8 px-2.5 text-[13px] pointer-coarse:h-11"
                      onClick={() => {
                        setFilter('all')
                        setQ('')
                      }}
                    >
                      Clear filters
                    </Button>
                  ) : null}
                </div>
              </TableCell>
            </TableRow>
          ) : null}
        </TableBody>
      </Table>
    </section>
  )
}
