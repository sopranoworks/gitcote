import { createContext, useContext, type ReactNode } from 'react'
import type { TreeNode } from './types'

export interface ContentConfig {
  renderProjectExtra?: (ns: string, proj: string) => ReactNode
  renderEditButton?: (ns: string, proj: string, path: string, styles: Record<string, string>) => ReactNode
  renderNewFileLink?: (ns: string, proj: string, styles: Record<string, string>) => ReactNode
  renderNewFileButton?: (ns: string, proj: string, launchDir: string) => ReactNode
  renderDropZone?: (ns: string, proj: string, children: ReactNode) => ReactNode
  renderContextMenu?: (props: {
    node: TreeNode
    ns: string
    proj: string
    x: number
    y: number
    onClose: () => void
    onRename: () => void
    onCopyLink: () => void
  }) => ReactNode
  canDrag?: boolean
  canRename?: boolean
  onTreeMove?: (ns: string, proj: string, sourcePath: string, targetPath: string) => void
  onTreeRename?: (ns: string, proj: string, sourcePath: string, newName: string) => void
  reservedPaths?: Set<string>
  dndManager?: unknown
}

const ContentConfigContext = createContext<ContentConfig>({})

export function ContentProvider({
  value,
  children,
}: {
  value?: ContentConfig
  children: ReactNode
}) {
  return (
    <ContentConfigContext.Provider value={value ?? {}}>
      {children}
    </ContentConfigContext.Provider>
  )
}

export function useContentConfig(): ContentConfig {
  return useContext(ContentConfigContext)
}
