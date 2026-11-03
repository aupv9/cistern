import { useEffect, useState } from 'react'
import { CopyIcon } from './Icons'

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle')
  useEffect(() => {
    if (state === 'idle') return
    const id = window.setTimeout(() => setState('idle'), 2000)
    return () => window.clearTimeout(id)
  }, [state])
  return (
    <>
      <button
        type="button"
        className="btn btn-sm"
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(text)
            setState('copied')
          } catch {
            setState('failed')
          }
        }}
      >
        <CopyIcon />
        {state === 'copied' ? 'Copied' : label}
      </button>
      <span className="sr" role="status">
        {state === 'copied' ? 'Copied to the clipboard' : state === 'failed' ? 'Could not copy. Select the text and copy it by hand.' : ''}
      </span>
    </>
  )
}
