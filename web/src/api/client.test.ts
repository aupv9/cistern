import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { server } from '~/mocks/node'
import { ApiError, CSRF_HEADER, api, loginUrl, setUnauthorizedHandler, unwrap } from './client'
import { meQuery, runBackup } from './queries'
import { QueryClient } from '@tanstack/react-query'

async function loadMe() {
  const qc = new QueryClient()
  return qc.fetchQuery(meQuery)
}

describe('api client', () => {
  it('sends the CSRF token from /me on non-GET requests only', async () => {
    const seen: { method: string; csrf: string | null; credentials: RequestCredentials }[] = []
    server.events.on('request:start', ({ request }) => {
      seen.push({ method: request.method, csrf: request.headers.get(CSRF_HEADER), credentials: request.credentials })
    })
    const me = await loadMe()
    await runBackup('orders-db')
    server.events.removeAllListeners()
    const get = seen.find((s) => s.method === 'GET')
    const post = seen.find((s) => s.method === 'POST')
    expect(get?.csrf).toBeNull()
    expect(post?.csrf).toBe(me.csrf_token)
    expect(post?.credentials).toBe('same-origin')
  })

  it('sends the browser to sign-in with the current path on 401', async () => {
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    window.history.pushState({}, '', '/jobs/orders-db?snapshot=abc')
    server.use(http.get('*/api/v1/overview', () => HttpResponse.json({ error: { code: 'unauthenticated', message: 'Sign in.' } }, { status: 401 })))
    await expect(unwrap(api.GET('/overview'))).rejects.toMatchObject({ status: 401, code: 'unauthenticated' })
    expect(handler).toHaveBeenCalledWith('/jobs/orders-db?snapshot=abc')
    expect(loginUrl('/jobs/orders-db?snapshot=abc')).toBe('/api/v1/auth/login?return_to=%2Fjobs%2Forders-db%3Fsnapshot%3Dabc')
    window.history.pushState({}, '', '/')
  })

  it('never builds an open redirect', () => {
    expect(loginUrl('//evil.example')).toBe('/api/v1/auth/login?return_to=%2F')
    expect(loginUrl('https://evil.example')).toBe('/api/v1/auth/login?return_to=%2F')
  })

  it('surfaces error.message and error.fields', async () => {
    await loadMe()
    const err = await unwrap(
      api.POST('/jobs/{job}/restore/preview', { params: { path: { job: 'orders-db' } }, body: { snapshot: 'nope', target: 'mars' } }),
    ).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    const e = err as ApiError
    expect(e.status).toBe(422)
    expect(e.code).toBe('invalid')
    expect(e.message).toBe('The restore cannot be prepared with these values.')
    expect(e.fields.snapshot).toMatch(/No snapshot/)
    expect(e.fields.target).toMatch(/no restore target/)
  })

  it('rejects a POST without the CSRF token', async () => {
    const err = await runBackup('orders-db').catch((e: unknown) => e)
    expect(err).toMatchObject({ status: 403, code: 'csrf' })
  })

  it('reports network failures in plain words', async () => {
    server.use(http.get('*/api/v1/overview', () => HttpResponse.error()))
    await expect(unwrap(api.GET('/overview'))).rejects.toMatchObject({ code: 'network' })
  })
})
