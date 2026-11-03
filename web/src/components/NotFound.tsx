import { Link } from '@tanstack/react-router'

export function NotFound() {
  return (
    <div className="page">
      <div className="card">
        <div className="state">
          <p className="title">Page not found</p>
          <p className="muted">There is nothing at this address. It may have been moved, or the link may be wrong.</p>
          <Link to="/" className="btn btn-sm">
            Go to overview
          </Link>
        </div>
      </div>
    </div>
  )
}
