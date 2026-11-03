import type { SVGProps } from 'react'

type IconProps = SVGProps<SVGSVGElement> & { size?: number }

function Base({ size = 16, children, ...rest }: IconProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
      {...rest}
    >
      {children}
    </svg>
  )
}

export function Mark({ size = 24 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 28 28" fill="none" aria-hidden="true" focusable="false">
      <rect x="3" y="4" width="22" height="20" rx="6" stroke="currentColor" strokeWidth="2.2" />
      <path d="M3 15c4 2 8 2 11 0s7-2 11 0" stroke="currentColor" strokeWidth="2.2" />
    </svg>
  )
}

export function SearchIcon(p: IconProps) {
  return (
    <Base {...p}>
      <circle cx="11" cy="11" r="7" />
      <path d="M20 20l-3.5-3.5" />
    </Base>
  )
}

export function RefreshIcon(p: IconProps) {
  return (
    <Base {...p}>
      <path d="M21 12a9 9 0 1 1-2.6-6.4" />
      <path d="M21 3v6h-6" />
    </Base>
  )
}

export function ErrorIcon(p: IconProps) {
  return (
    <Base size={18} {...p}>
      <circle cx="12" cy="12" r="9" />
      <path d="M15 9l-6 6" />
      <path d="M9 9l6 6" />
    </Base>
  )
}

export function WarningIcon(p: IconProps) {
  return (
    <Base size={18} {...p}>
      <path d="M12 9v4" />
      <path d="M12 17h.01" />
      <path d="M10.3 3.9L1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z" />
    </Base>
  )
}

export function InfoIcon(p: IconProps) {
  return (
    <Base size={18} {...p}>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 11v5" />
      <path d="M12 8h.01" />
    </Base>
  )
}

export function CheckCircleIcon(p: IconProps) {
  return (
    <Base size={18} strokeWidth={2.4} {...p}>
      <circle cx="12" cy="12" r="9" />
      <path d="M8 12l3 3 5-6" />
    </Base>
  )
}

export function DownloadIcon(p: IconProps) {
  return (
    <Base {...p}>
      <path d="M12 4v11" />
      <path d="M7 10l5 5 5-5" />
      <path d="M5 20h14" />
    </Base>
  )
}

export function CopyIcon(p: IconProps) {
  return (
    <Base size={14} {...p}>
      <rect x="9" y="9" width="12" height="12" rx="2" />
      <path d="M5 15V5a2 2 0 0 1 2-2h10" />
    </Base>
  )
}

export function SunIcon(p: IconProps) {
  return (
    <Base {...p}>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
    </Base>
  )
}

export function MoonIcon(p: IconProps) {
  return (
    <Base {...p}>
      <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" />
    </Base>
  )
}

export function LockIcon(p: IconProps) {
  return (
    <Base size={18} {...p}>
      <rect x="4" y="11" width="16" height="10" rx="2" />
      <path d="M8 11V7a4 4 0 0 1 8 0v4" />
    </Base>
  )
}

export function StepCurrentIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" aria-hidden="true" focusable="false">
      <circle cx="12" cy="12" r="9" strokeWidth="2.4" stroke="currentColor" />
      <circle cx="12" cy="12" r="4" fill="currentColor" />
    </svg>
  )
}

export function StepTodoIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" aria-hidden="true" focusable="false">
      <circle cx="12" cy="12" r="9" strokeWidth="2" stroke="currentColor" />
    </svg>
  )
}

export function Slash() {
  return (
    <svg width="16" height="24" viewBox="0 0 16 24" aria-hidden="true" focusable="false" className="slash">
      <path d="M11 3L5 21" stroke="currentColor" strokeWidth="1.5" />
    </svg>
  )
}
