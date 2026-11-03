import { createFileRoute } from '@tanstack/react-router'
import { loginUrl } from '~/api/client'
import { DOCS_URL } from '~/components/Header'
import { Mark } from '~/components/Icons'

type DeniedSearch = { reason?: string }

const NO_ROLE = {
  title: 'You do not have access',
  body: 'You are signed in, but none of your groups in the identity provider maps to a Cistern role. Ask an administrator to add one of your groups to the role mapping in the server configuration.',
}

const REASONS: Record<string, { title: string; body: string }> = {
  no_role: NO_ROLE,
  login_failed: {
    title: 'Sign-in failed',
    body: 'The identity provider did not complete the sign-in. Try again; if it keeps failing, ask an administrator to check the server log.',
  },
  login_expired: {
    title: 'Sign-in took too long',
    body: 'The sign-in request expired before it finished. Start again.',
  },
  idp_unavailable: {
    title: 'The identity provider is not reachable',
    body: 'Cistern could not reach the single sign-on provider. Try again in a few minutes.',
  },
  sso_not_configured: {
    title: 'Single sign-on is not configured',
    body: 'This server has no OIDC settings. An administrator must add server.oidc to the configuration.',
  },
}

export const Route = createFileRoute('/denied')({
  head: () => ({ meta: [{ title: 'No access · Cistern' }] }),
  validateSearch: (search: Record<string, unknown>): DeniedSearch => ({
    reason: typeof search.reason === 'string' ? search.reason : undefined,
  }),
  component: Denied,
})

function Denied() {
  const { reason } = Route.useSearch()
  const text = REASONS[reason ?? 'no_role'] ?? NO_ROLE
  return (
    <div className="signin">
      <main>
        <Mark size={40} />
        <div className="intro">
          <h1 className="h1">{text.title}</h1>
          <p>{text.body}</p>
        </div>
        <a className="btn cta" href={loginUrl('/')}>
          Sign in with another account
        </a>
        <p className="note">
          Roles are viewer, operator, restorer, approver and admin. See the <a href={DOCS_URL}>documentation</a> for
          how groups map to them.
        </p>
      </main>
    </div>
  )
}
