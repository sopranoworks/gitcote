import {
  createContext,
  useCallback,
  useContext,
  type ComponentType,
  type ReactNode,
} from 'react'

export interface RailItemDef {
  id: string
  label: string
  icon: ReactNode
}

export interface ShellConfig {
  brandName?: string
  railItems: RailItemDef[]
  renderSidebar: (view: string) => ReactNode
  renderSidebarExtra?: () => ReactNode
  renderRailBottom?: (styles: Record<string, string>) => ReactNode
  renderBreadcrumbs?: (styles: Record<string, string>) => ReactNode
  renderCommandPalette?: () => ReactNode
  renderToaster?: () => ReactNode
  renderNotifyBridge?: () => ReactNode
  shellWrapper?: ComponentType<{ children: ReactNode }>
  useRailControls: (
    rail: string,
    sidebarOpen: boolean,
    setRail: (v: string) => void,
    setSidebarOpen: (open: boolean) => void,
  ) => { onSelect: (v: string) => void; disabledItems: string[] }
  useResetRailOnProjectChange: (setRail: (v: string) => void) => void
  deriveActiveRail?: (pathname: string, rail: string) => string
  layoutAutoSaveId?: string
}

const ShellConfigContext = createContext<ShellConfig | null>(null)

export function ShellProvider({
  value,
  children,
}: {
  value: ShellConfig
  children: ReactNode
}) {
  return (
    <ShellConfigContext.Provider value={value}>
      {children}
    </ShellConfigContext.Provider>
  )
}

export function useShellConfig(): ShellConfig {
  const ctx = useContext(ShellConfigContext)
  if (!ctx) throw new Error('useShellConfig must be used within ShellProvider')
  return ctx
}

export function useSimpleRailControls(
  rail: string,
  sidebarOpen: boolean,
  setRail: (v: string) => void,
  setSidebarOpen: (open: boolean) => void,
): { onSelect: (v: string) => void; disabledItems: string[] } {
  const onSelect = useCallback(
    (v: string) => {
      if (v === rail && sidebarOpen) {
        setSidebarOpen(false)
        return
      }
      setRail(v)
      setSidebarOpen(true)
    },
    [rail, sidebarOpen, setRail, setSidebarOpen],
  )
  return { onSelect, disabledItems: [] }
}

export function useNoopRailReset(_setRail: (v: string) => void): void {}
