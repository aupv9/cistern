import { createFileRoute } from '@tanstack/react-router'
import { loginUrl } from '~/api/client'
import { DOCS_URL } from '~/components/Header'
import { LockIcon, Mark } from '~/components/Icons'

type SignInSearch = { return_to?: string; signed_out?: boolean }

export const Route = createFileRoute('/signin')({
  validateSearch: (search: Record<string, unknown>): SignInSearch => ({
    return_to: typeof search.return_to === 'string' ? search.return_to : undefined,
    signed_out: search.signed_out === true || search.signed_out === 'true' ? true : undefined,
  }),
  head: () => ({ meta: [{ title: 'Sign in · Cistern' }] }),
  component: SignIn,
})

function SignIn() {
  const { return_to, signed_out } = Route.useSearch()
  return (
    <div className="signin">
      <main>
        <Mark size={40} />
        <div className="intro">
          <h1 className="h1">Sign in to Cistern</h1>
          <p>Use your organization account. What you can do here follows your groups in the identity provider.</p>
        </div>
        <a className="btn btn-p cta" href={loginUrl(return_to ?? '/')}>
          <LockIcon />
          Continue with single sign-on
        </a>
        <p className="note" role={signed_out ? 'status' : undefined}>
          {signed_out ? 'You are signed out.' : 'Sessions end after a period without activity. Sign in again to continue.'}
        </p>
      </main>
      <p className="foot-note">
        <a href={DOCS_URL}>Documentation</a>
      </p>
    </div>
  )
}
