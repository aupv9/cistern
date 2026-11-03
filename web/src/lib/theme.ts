export type Theme = 'light' | 'dark'

export const THEME_KEY = 'cistern.theme'

export function readStoredTheme(): Theme | null {
  try {
    const v = window.localStorage.getItem(THEME_KEY)
    return v === 'light' || v === 'dark' ? v : null
  } catch {
    return null
  }
}

export function storeTheme(theme: Theme) {
  try {
    window.localStorage.setItem(THEME_KEY, theme)
  } catch {
    return
  }
}

export function systemTheme(): Theme {
  try {
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  } catch {
    return 'light'
  }
}

export function currentTheme(): Theme {
  const attr = document.documentElement.dataset.theme
  if (attr === 'light' || attr === 'dark') return attr
  return systemTheme()
}

export function applyTheme(theme: Theme | null) {
  if (theme) document.documentElement.dataset.theme = theme
  else delete document.documentElement.dataset.theme
}

export const THEME_BOOT_SCRIPT = `try{var t=localStorage.getItem('${THEME_KEY}');if(t==='light'||t==='dark')document.documentElement.dataset.theme=t}catch(e){}`
