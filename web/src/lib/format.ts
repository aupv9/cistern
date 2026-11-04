const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'] as const

export function formatBytes(bytes: number | null | undefined): string {
  if (bytes == null || !Number.isFinite(bytes) || bytes < 0) return '–'
  if (bytes < 1024) return `${Math.round(bytes)} B`
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024
    unit += 1
  }
  let text = value < 10 ? value.toFixed(1) : String(Math.round(value))
  if (text === '1024' && unit < BYTE_UNITS.length - 1) {
    text = '1.0'
    unit += 1
  }
  return `${text} ${BYTE_UNITS[unit]}`
}

const pad2 = (n: number) => String(n).padStart(2, '0')

export function formatDuration(ms: number | null | undefined): string {
  if (ms == null || !Number.isFinite(ms) || ms < 0) return '–'
  if (ms < 1000) return `${Math.round(ms)} ms`
  const totalSeconds = Math.round(ms / 1000)
  if (totalSeconds < 60) return `${totalSeconds}s`
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  if (hours === 0) return `${minutes}m ${pad2(seconds)}s`
  return `${hours}h ${pad2(minutes)}m`
}

type Parts = { year: number; month: number; day: number; hour: number; minute: number }

const partsFormatters = new Map<string, Intl.DateTimeFormat>()

function partsFormatter(timeZone: string): Intl.DateTimeFormat {
  let f = partsFormatters.get(timeZone)
  if (!f) {
    try {
      f = new Intl.DateTimeFormat('en-US', {
        timeZone,
        year: 'numeric',
        month: 'numeric',
        day: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
        hourCycle: 'h23',
      })
    } catch {
      f = new Intl.DateTimeFormat('en-US', {
        timeZone: 'UTC',
        year: 'numeric',
        month: 'numeric',
        day: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
        hourCycle: 'h23',
      })
    }
    partsFormatters.set(timeZone, f)
  }
  return f
}

function partsIn(date: Date, timeZone: string): Parts {
  const out: Record<string, number> = {}
  for (const p of partsFormatter(timeZone).formatToParts(date)) {
    if (p.type !== 'literal') out[p.type] = Number(p.value)
  }
  return {
    year: out.year ?? 1970,
    month: out.month ?? 1,
    day: out.day ?? 1,
    hour: (out.hour ?? 0) % 24,
    minute: out.minute ?? 0,
  }
}

export function zonedParts(date: Date, timeZone: string): Parts {
  return partsIn(date, timeZone)
}

export const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

function dayNumber(p: Parts): number {
  return Date.UTC(p.year, p.month - 1, p.day) / 86_400_000
}

function toDate(value: string | Date): Date | null {
  const d = value instanceof Date ? value : new Date(value)
  return Number.isNaN(d.getTime()) ? null : d
}

export function formatTime(value: string | Date | null | undefined, timeZone: string): string {
  if (!value) return '–'
  const d = toDate(value)
  if (!d) return '–'
  const p = partsIn(d, timeZone)
  return `${pad2(p.hour)}:${pad2(p.minute)}`
}

export function formatDay(value: string | Date | null | undefined, timeZone: string, now: Date = new Date()): string {
  if (!value) return '–'
  const d = toDate(value)
  if (!d) return '–'
  const p = partsIn(d, timeZone)
  const n = partsIn(now, timeZone)
  const base = `${MONTHS[p.month - 1]} ${p.day}`
  return p.year === n.year ? base : `${base}, ${p.year}`
}

export function formatWhen(value: string | Date | null | undefined, timeZone: string, now: Date = new Date()): string {
  if (!value) return '–'
  const d = toDate(value)
  if (!d) return '–'
  const p = partsIn(d, timeZone)
  const n = partsIn(now, timeZone)
  const diff = dayNumber(p) - dayNumber(n)
  const time = `${pad2(p.hour)}:${pad2(p.minute)}`
  if (diff === 0) return `Today ${time}`
  if (diff === 1) return `Tomorrow ${time}`
  const base = `${MONTHS[p.month - 1]} ${p.day}`
  return p.year === n.year ? `${base}, ${time}` : `${base}, ${p.year}, ${time}`
}

export function formatWhenLower(value: string | Date | null | undefined, timeZone: string, now: Date = new Date()): string {
  const s = formatWhen(value, timeZone, now)
  if (s.startsWith('Today ')) return `today at ${s.slice(6)}`
  if (s.startsWith('Tomorrow ')) return `tomorrow at ${s.slice(9)}`
  if (s === '–') return s
  return `on ${s}`
}

export function formatRelative(value: string | Date | null | undefined, now: Date = new Date()): string {
  if (!value) return '–'
  const d = toDate(value)
  if (!d) return '–'
  const deltaMs = d.getTime() - now.getTime()
  const future = deltaMs > 0
  const abs = Math.abs(deltaMs)
  const minutes = Math.round(abs / 60_000)
  let text: string
  if (abs < 45_000) return future ? 'in less than a minute' : 'just now'
  if (minutes < 60) text = `${Math.max(1, minutes)} min`
  else if (minutes < 60 * 36) text = `${Math.round(minutes / 60)} h`
  else {
    const days = Math.round(minutes / 1440)
    text = `${days} ${days === 1 ? 'day' : 'days'}`
  }
  return future ? `in ${text}` : `${text} ago`
}

export function formatElapsed(fromIso: string | null | undefined, now: Date = new Date()): string {
  if (!fromIso) return '–'
  const d = toDate(fromIso)
  if (!d) return '–'
  return formatDuration(Math.max(0, now.getTime() - d.getTime()))
}

export function timeZoneLabel(timeZone: string, at: Date = new Date()): string {
  try {
    const part = new Intl.DateTimeFormat('en-US', { timeZone, timeZoneName: 'shortOffset' })
      .formatToParts(at)
      .find((p) => p.type === 'timeZoneName')
    if (!part) return timeZone
    const label = part.value.replace(/^GMT/, 'UTC')
    return label === 'UTC+0' || label === 'UTC' ? 'UTC' : label
  } catch {
    return timeZone
  }
}

export function formatCount(n: number): string {
  return new Intl.NumberFormat('en-US').format(n)
}

export function plural(n: number, one: string, many = `${one}s`): string {
  return `${formatCount(n)} ${n === 1 ? one : many}`
}

export function shortHash(hash: string, head = 16, tail = 8): string {
  if (hash.length <= head + tail + 1) return hash
  return `${hash.slice(0, head)}…${hash.slice(-tail)}`
}

export function median(values: number[]): number | null {
  if (values.length === 0) return null
  const sorted = [...values].sort((a, b) => a - b)
  const mid = Math.floor(sorted.length / 2)
  if (sorted.length % 2 === 1) return sorted[mid] ?? null
  const a = sorted[mid - 1]
  const b = sorted[mid]
  return a == null || b == null ? null : (a + b) / 2
}

export function initials(name: string): string {
  return (
    name
      .split(/\s+/)
      .filter(Boolean)
      .map((w) => w[0] ?? '')
      .join('')
      .slice(0, 2)
      .toUpperCase() || '?'
  )
}

export function capitalize(s: string): string {
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s
}

const DOW = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

export function describeCron(expr: string | null | undefined): string | null {
  if (!expr) return null
  const e = expr.trim()
  const macros: Record<string, string> = {
    '@hourly': 'hourly',
    '@daily': 'daily at 00:00',
    '@midnight': 'daily at 00:00',
    '@weekly': 'weekly on Sunday at 00:00',
    '@monthly': 'monthly on day 1 at 00:00',
  }
  if (macros[e]) return macros[e]
  const f = e.split(/\s+/)
  if (f.length !== 5) return e
  const [min, hour, dom, mon, dow] = f as [string, string, string, string, string]
  const isNum = (s: string) => /^\d+$/.test(s)
  if (!isNum(min)) return e
  if (hour === '*' && dom === '*' && mon === '*' && dow === '*') return `hourly at minute ${Number(min)}`
  const hours = hour.split(',')
  if (!hours.every(isNum)) return e
  const times = hours.map((h) => `${pad2(Number(h))}:${pad2(Number(min))}`).join(' and ')
  if (dom === '*' && mon === '*' && dow === '*') return `daily at ${times}`
  if (dom === '*' && mon === '*' && isNum(dow)) return `weekly on ${DOW[Number(dow) % 7]} at ${times}`
  if (isNum(dom) && mon === '*' && dow === '*') return `monthly on day ${Number(dom)} at ${times}`
  return e
}
