import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterAll, afterEach, beforeAll, vi } from 'vitest'
import { setCsrfToken, setUnauthorizedHandler } from '~/api/client'
import { resetMockDb } from '~/mocks/handlers'
import { server } from '~/mocks/node'

beforeAll(() => {
  server.listen({ onUnhandledFrame: 'error' })
})

afterEach(() => {
  cleanup()
  server.resetHandlers()
  resetMockDb()
  setCsrfToken(null)
  setUnauthorizedHandler(null)
  window.localStorage.clear()
  delete document.documentElement.dataset.theme
  vi.restoreAllMocks()
})

afterAll(() => {
  server.close()
})

if (!window.matchMedia) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }),
  })
}

if (!window.scrollTo || !('mock' in window.scrollTo)) {
  window.scrollTo = vi.fn() as unknown as typeof window.scrollTo
}
