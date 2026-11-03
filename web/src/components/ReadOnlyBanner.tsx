import type { Me } from '~/api/queries'
import { readOnlyReason } from '~/lib/roles'
import { InfoIcon } from './Icons'

export function ReadOnlyBanner({ me }: { me: Me }) {
  const reason = readOnlyReason(me.deployment)
  if (!reason) return null
  return (
    <div className="banner" role="status">
      <div className="banner-in">
        <InfoIcon />
        <span>
          <strong>Read-only.</strong> {reason}
        </span>
      </div>
    </div>
  )
}
