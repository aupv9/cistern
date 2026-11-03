import { setupWorker } from 'msw/browser'
import type { Role } from '~/api/queries'
import { handlers, resetMockDb } from './handlers'

const ROLES_KEY = 'cistern.mock.roles'

function storedRoles(): Role[] | undefined {
  try {
    const raw = window.localStorage.getItem(ROLES_KEY)
    if (!raw) return undefined
    const roles = raw.split(',').map((r) => r.trim()).filter(Boolean) as Role[]
    return roles.length ? roles : undefined
  } catch {
    return undefined
  }
}

export async function startMocks() {
  resetMockDb({ roles: storedRoles(), latencyMs: 250 })
  const worker = setupWorker(...handlers)
  await worker.start({
    onUnhandledFrame: 'bypass',
    quiet: true,
  })
}
