import type { StatusInfo } from '~/lib/status'

export function Status({ info, small, className }: { info: StatusInfo; small?: boolean; className?: string }) {
  const tone = info.tone === 'neutral' ? '' : info.tone
  return <span className={['st', tone, small ? 'sm' : '', className ?? ''].filter(Boolean).join(' ')}>{info.text}</span>
}
