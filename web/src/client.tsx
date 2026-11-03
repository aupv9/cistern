import { StrictMode, startTransition } from 'react'
import { hydrateRoot } from 'react-dom/client'
import { StartClient } from '@tanstack/react-start/client'

async function boot() {
  if (import.meta.env.VITE_MOCK === '1') {
    const { startMocks } = await import('./mocks/browser')
    await startMocks()
  }
  startTransition(() => {
    hydrateRoot(
      document,
      <StrictMode>
        <StartClient />
      </StrictMode>,
    )
  })
}

void boot()
