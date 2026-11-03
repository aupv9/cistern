import createClient, { type Middleware } from 'openapi-fetch'
import type { components, paths } from './schema'

export type Schemas = components['schemas']
export type ErrorCode = Schemas['Error']['error']['code']
export type ApiErrorCode = ErrorCode | 'network' | 'unknown'

export class ApiError extends Error {
  readonly status: number
  readonly code: ApiErrorCode
  readonly fields: Record<string, string>

  constructor(status: number, code: ApiErrorCode, message: string, fields: Record<string, string> = {}) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.fields = fields
  }
}

export const CSRF_HEADER = 'X-Cistern-CSRF'
export const API_BASE = '/api/v1'

let csrfToken: string | null = null

export function setCsrfToken(token: string | null) {
  csrfToken = token
}

export function getCsrfToken() {
  return csrfToken
}

export function loginUrl(returnTo: string) {
  const safe = returnTo.startsWith('/') && !returnTo.startsWith('//') ? returnTo : '/'
  return `${API_BASE}/auth/login?return_to=${encodeURIComponent(safe)}`
}

export function currentPath() {
  if (typeof window === 'undefined') return '/'
  return `${window.location.pathname}${window.location.search}`
}

type UnauthorizedHandler = (returnTo: string) => void

const defaultUnauthorized: UnauthorizedHandler = (returnTo) => {
  window.location.assign(loginUrl(returnTo))
}

let unauthorizedHandler: UnauthorizedHandler = defaultUnauthorized

export function setUnauthorizedHandler(handler: UnauthorizedHandler | null) {
  unauthorizedHandler = handler ?? defaultUnauthorized
}

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS'])

export const authMiddleware: Middleware = {
  onRequest({ request }) {
    if (!SAFE_METHODS.has(request.method.toUpperCase()) && csrfToken) {
      request.headers.set(CSRF_HEADER, csrfToken)
    }
    return request
  },
  onResponse({ request, response }) {
    if (response.status === 401 && typeof window !== 'undefined' && !request.url.endsWith('/auth/logout')) {
      unauthorizedHandler(currentPath())
    }
    return response
  },
}

function baseUrl() {
  if (typeof window === 'undefined') return `http://localhost${API_BASE}`
  return `${window.location.origin}${API_BASE}`
}

export const api = createClient<paths>({
  baseUrl: baseUrl(),
  credentials: 'same-origin',
  fetch: (request: Request) => globalThis.fetch(request),
})

api.use(authMiddleware)

const KNOWN_CODES: ReadonlySet<string> = new Set<ErrorCode>([
  'unauthenticated',
  'forbidden',
  'not_found',
  'conflict',
  'invalid',
  'daemon_unavailable',
  'csrf',
  'internal',
])

function fallbackMessage(status: number): string {
  if (status === 401) return 'Your session has ended. Sign in again.'
  if (status === 403) return 'You do not have permission to do this.'
  if (status === 404) return 'Not found.'
  if (status === 409) return 'This conflicts with something already in progress.'
  if (status === 422) return 'Some fields are not valid.'
  if (status === 503) return 'The Cistern daemon is not available right now.'
  if (status === 502 || status === 504) return 'The Cistern server is not reachable. Try again in a moment.'
  return `The server answered with status ${status}.`
}

export function toApiError(status: number, body: unknown): ApiError {
  if (body && typeof body === 'object' && 'error' in body) {
    const err = (body as { error: unknown }).error
    if (err && typeof err === 'object') {
      const e = err as { code?: unknown; message?: unknown; fields?: unknown }
      const code = typeof e.code === 'string' && KNOWN_CODES.has(e.code) ? (e.code as ErrorCode) : 'unknown'
      const message = typeof e.message === 'string' && e.message ? e.message : fallbackMessage(status)
      const fields: Record<string, string> = {}
      if (e.fields && typeof e.fields === 'object') {
        for (const [k, v] of Object.entries(e.fields as Record<string, unknown>)) {
          if (typeof v === 'string') fields[k] = v
        }
      }
      return new ApiError(status, code, message, fields)
    }
  }
  return new ApiError(status, 'unknown', fallbackMessage(status))
}

type FetchResult<T> = { data?: T; error?: unknown; response: Response }

export async function unwrap<T>(call: Promise<FetchResult<T>>): Promise<T> {
  let result: FetchResult<T>
  try {
    result = await call
  } catch (cause) {
    if (cause instanceof ApiError) throw cause
    if (cause instanceof DOMException && cause.name === 'AbortError') throw cause
    throw new ApiError(0, 'network', 'Could not reach the Cistern server. Check your connection and try again.')
  }
  const { data, error, response } = result
  if (!response.ok) throw toApiError(response.status, error)
  return data as T
}

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.message
  if (error instanceof Error && error.message) return error.message
  return 'Something went wrong.'
}

export function isApiError(error: unknown, code?: ApiErrorCode): error is ApiError {
  return error instanceof ApiError && (code === undefined || error.code === code)
}
