import { createContext, useContext } from 'react'
import type { Me } from '~/api/queries'

export const MeContext = createContext<Me | null>(null)

export function useMe(): Me {
  const me = useContext(MeContext)
  if (!me) throw new Error('useMe must be used inside the signed-in layout')
  return me
}
