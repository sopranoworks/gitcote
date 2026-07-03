import {
  createContext,
  useCallback,
  useContext,
  useState,
  type ReactNode,
} from 'react'
import type { BannerIntent } from './notifyRouter'

interface BannerCtx {
  banner: BannerIntent | null
  show: (b: BannerIntent) => void
  clear: () => void
}

const Ctx = createContext<BannerCtx | null>(null)

export function BannerProvider({ children }: { children: ReactNode }) {
  const [banner, setBanner] = useState<BannerIntent | null>(null)
  const show = useCallback((b: BannerIntent) => setBanner(b), [])
  const clear = useCallback(() => setBanner(null), [])
  return <Ctx.Provider value={{ banner, show, clear }}>{children}</Ctx.Provider>
}

export function useBanner(): BannerCtx {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('useBanner must be used within BannerProvider')
  return ctx
}
