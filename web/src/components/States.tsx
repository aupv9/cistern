import type { ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import { ApiError, errorMessage } from '~/api/client'
import { ErrorIcon } from './Icons'

export function ErrorState({ error, onRetry, title }: { error: unknown; onRetry?: () => void; title?: string }) {
  const notFound = error instanceof ApiError && error.code === 'not_found'
  return (
    <div className="state" role="alert">
      <span style={{ color: 'var(--fail-fg)' }}>
        <ErrorIcon />
      </span>
      <p className="title">{title ?? (notFound ? 'Not found' : 'Could not load this')}</p>
      <p className="muted">{errorMessage(error)}</p>
      {onRetry && !notFound ? (
        <button type="button" className="btn btn-sm" onClick={onRetry}>
          Try again
        </button>
      ) : null}
      {notFound ? (
        <Link to="/" className="btn btn-sm">
          Go to overview
        </Link>
      ) : null}
    </div>
  )
}

export function EmptyState({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="state">
      <p className="title">{title}</p>
      {children}
    </div>
  )
}

export function InlineError({ error }: { error: unknown }) {
  if (!error) return null
  return (
    <div className="callout err box" role="alert">
      <ErrorIcon />
      <span className="text">{errorMessage(error)}</span>
    </div>
  )
}
