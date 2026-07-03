import {
  createContext,
  useCallback,
  useContext,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import type { ToastIntent } from './notifyRouter'

export interface Toast extends ToastIntent {
  id: number
}

interface ToastCtx {
  toasts: Toast[]
  add: (t: ToastIntent) => void
  dismiss: (id: number) => void
}

const Ctx = createContext<ToastCtx | null>(null)

const AUTO_DISMISS_MS = 8000

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const idRef = useRef(0)

  const dismiss = useCallback((id: number) => {
    setToasts((ts) => ts.filter((t) => t.id !== id))
  }, [])

  const add = useCallback(
    (t: ToastIntent) => {
      const id = ++idRef.current
      setToasts((ts) => [...ts, { ...t, id }])
      setTimeout(() => dismiss(id), AUTO_DISMISS_MS)
    },
    [dismiss],
  )

  return (
    <Ctx.Provider value={{ toasts, add, dismiss }}>{children}</Ctx.Provider>
  )
}

export function useToast(): ToastCtx {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('useToast must be used within ToastProvider')
  return ctx
}
