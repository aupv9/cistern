import type { CSSProperties } from 'react'

export function Skel({ w = '100%', h = 14, style }: { w?: number | string; h?: number; style?: CSSProperties }) {
  return <span className="skel" aria-hidden="true" style={{ width: w, height: h, ...style }} />
}

export function TableSkeleton({ rows = 5, cols = 5, label = 'Loading' }: { rows?: number; cols?: number; label?: string }) {
  return (
    <div role="status" aria-label={label} className="scroll-x">
      <table className="t">
        <tbody>
          {Array.from({ length: rows }, (_, r) => (
            <tr key={r}>
              {Array.from({ length: cols }, (_, c) => (
                <td key={c}>
                  <Skel w={c === 0 ? 120 : 72} />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

export function BlockSkeleton({ lines = 3, label = 'Loading' }: { lines?: number; label?: string }) {
  return (
    <div role="status" aria-label={label} style={{ display: 'flex', flexDirection: 'column', gap: 10, padding: '16px 20px' }}>
      {Array.from({ length: lines }, (_, i) => (
        <Skel key={i} w={i === lines - 1 ? '60%' : '100%'} />
      ))}
    </div>
  )
}

export function PageSkeleton({ title = 'Loading' }: { title?: string }) {
  return (
    <div className="page" role="status" aria-label={title}>
      <div className="page-title">
        <Skel w={220} h={32} />
        <Skel w={360} h={14} />
      </div>
      <div className="card">
        <BlockSkeleton lines={4} label={title} />
      </div>
    </div>
  )
}
