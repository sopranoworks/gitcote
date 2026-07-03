import type { FileNode, TreeNode, ProjectInfo } from './types'

export function toTreeNodes(nodes: FileNode[]): TreeNode[] {
  return nodes.map((n) => ({
    id: n.path,
    name: n.name,
    path: n.path,
    isFile: !n.isDir,
    modifiedAt: n.modifiedAt,
    children: n.isDir ? toTreeNodes(n.children ?? []) : undefined,
  }))
}

export type SortMode = 'name-asc' | 'name-desc' | 'date-asc' | 'date-desc'

export function filterTree(nodes: FileNode[], query: string): FileNode[] {
  if (!query) return nodes
  const q = query.toLowerCase()
  return nodes.flatMap((n) => {
    if (n.isDir) {
      const children = filterTree(n.children ?? [], query)
      return children.length > 0 ? [{ ...n, children }] : []
    }
    return n.name.toLowerCase().includes(q) ? [n] : []
  })
}

export function sortTree(nodes: FileNode[], mode: SortMode): FileNode[] {
  const sorted = [...nodes].sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
    if (mode === 'name-asc' || mode === 'name-desc') {
      const cmp = a.name.localeCompare(b.name)
      return mode === 'name-desc' ? -cmp : cmp
    }
    const ta = a.modifiedAt ?? ''
    const tb = b.modifiedAt ?? ''
    const cmp = ta < tb ? -1 : ta > tb ? 1 : 0
    return mode === 'date-desc' ? -cmp : cmp
  })
  return sorted.map((n) =>
    n.isDir && n.children ? { ...n, children: sortTree(n.children, mode) } : n,
  )
}

export function flattenFilePaths(nodes: FileNode[]): string[] {
  const out: string[] = []
  const walk = (ns: FileNode[]) => {
    for (const n of ns) {
      if (n.isDir) walk(n.children ?? [])
      else out.push(n.path)
    }
  }
  walk(nodes)
  return out
}

export function ancestorDirs(filePath: string): string[] {
  const segs = filePath.split('/').filter(Boolean)
  const dirs: string[] = []
  let accum = ''
  for (let i = 0; i < segs.length - 1; i++) {
    accum = accum ? `${accum}/${segs[i]}` : segs[i]
    dirs.push(accum)
  }
  return dirs
}

export function namespacesOf(projects: ProjectInfo[]): string[] {
  return [...new Set(projects.map((p) => p.namespace))].sort()
}

export function dirOf(path: string): string {
  const i = path.lastIndexOf('/')
  return i < 0 ? '' : path.substring(0, i)
}
