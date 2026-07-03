import {
  createContext,
  useCallback,
  useContext,
  useState,
  type ReactNode,
} from 'react'

export type EditSignal =
  | { kind: 'write'; path: string }
  | { kind: 'delete'; path: string }
  | { kind: 'move'; path: string; to: string }

interface EditSignalCtx {
  signal: EditSignal | null
  set: (s: EditSignal) => void
  clear: () => void
}

const Ctx = createContext<EditSignalCtx | null>(null)

export function EditSignalProvider({ children }: { children: ReactNode }) {
  const [signal, setSignal] = useState<EditSignal | null>(null)
  const set = useCallback((s: EditSignal) => setSignal(s), [])
  const clear = useCallback(() => setSignal(null), [])
  return <Ctx.Provider value={{ signal, set, clear }}>{children}</Ctx.Provider>
}

export function useEditSignal(): EditSignalCtx {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('useEditSignal must be used within EditSignalProvider')
  return ctx
}
