import { useSyncExternalStore } from 'react'
import { applyTheme, currentTheme, storeTheme, type Theme } from '~/lib/theme'
import { MoonIcon, SunIcon } from './Icons'

const EVENT = 'cistern-theme'

function subscribe(onChange: () => void) {
  let mq: MediaQueryList | null = null
  try {
    mq = window.matchMedia('(prefers-color-scheme: dark)')
  } catch {
    mq = null
  }
  mq?.addEventListener('change', onChange)
  window.addEventListener(EVENT, onChange)
  return () => {
    mq?.removeEventListener('change', onChange)
    window.removeEventListener(EVENT, onChange)
  }
}

export function setTheme(theme: Theme) {
  applyTheme(theme)
  storeTheme(theme)
  window.dispatchEvent(new Event(EVENT))
}

export function ThemeToggle() {
  const theme = useSyncExternalStore<Theme | null>(subscribe, currentTheme, () => null)
  const next: Theme = theme === 'dark' ? 'light' : 'dark'
  const label = `Switch to ${next} theme`
  return (
    <button type="button" className="btn btn-ghost btn-icon" aria-label={label} title={label} data-testid="theme-toggle" onClick={() => setTheme(next)}>
      {theme === 'dark' ? <SunIcon /> : <MoonIcon />}
    </button>
  )
}
