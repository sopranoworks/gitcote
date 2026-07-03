import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import {
  Tree,
  type NodeApi,
  type NodeRendererProps,
  type TreeApi,
} from 'react-arborist'
import { toTreeNodes, ancestorDirs } from '../lib/tree'
import { useContentConfig } from '../lib/contentConfig'
import type { FileNode, TreeNode } from '../lib/types'
import styles from './FileTree.module.css'

export { styles as fileTreeStyles }

export type TreeOpenMode = 'blob' | 'history'
export function fileOpenRoute(openMode: TreeOpenMode) {
  return openMode === 'history'
    ? '/p/$namespace/$project/history/$'
    : '/p/$namespace/$project/blob/$'
}

export function FileTree({
  namespace,
  project,
  nodes,
  activePath,
  openMode = 'blob',
}: {
  namespace: string
  project: string
  nodes: FileNode[]
  activePath: string | null
  openMode?: TreeOpenMode
}) {
  const navigate = useNavigate()
  const {
    canDrag = false,
    canRename = false,
    onTreeMove,
    onTreeRename,
    renderContextMenu,
    reservedPaths: reservedSet,
    dndManager,
  } = useContentConfig()

  const data = useMemo(() => toTreeNodes(nodes), [nodes])
  const treeRef = useRef<TreeApi<TreeNode> | null>(null)

  const reserved = reservedSet ?? new Set<string>()

  const [menu, setMenu] = useState<{ x: number; y: number; node: TreeNode } | null>(
    null,
  )

  const initialOpenState = useMemo(() => {
    const open: Record<string, boolean> = {}
    if (activePath) for (const dir of ancestorDirs(activePath)) open[dir] = true
    return open
  }, [activePath])

  const wrapRef = useRef<HTMLDivElement>(null)
  const [size, setSize] = useState({ w: 240, h: 400 })
  useEffect(() => {
    const el = wrapRef.current
    if (!el) return
    const ro = new ResizeObserver(() => {
      setSize({ w: el.clientWidth, h: el.clientHeight })
    })
    ro.observe(el)
    setSize({ w: el.clientWidth, h: el.clientHeight })
    return () => ro.disconnect()
  }, [])

  useEffect(() => {
    if (!activePath) return
    const api = treeRef.current
    if (!api) return
    for (const dir of ancestorDirs(activePath)) api.open(dir)
    api.scrollTo(activePath)
  }, [activePath, data])

  const openFile = (path: string) => {
    navigate({
      to: fileOpenRoute(openMode),
      params: { namespace, project, _splat: path },
    })
  }

  const handleRename = canRename && onTreeRename
    ? ({ name, node }: { name: string; node: NodeApi<TreeNode> }) => {
        if (!node.data.isFile) return
        onTreeRename(namespace, project, node.data.path, name.trim())
      }
    : undefined

  const handleMove = canDrag && onTreeMove
    ? ({
        dragNodes,
        parentNode,
      }: {
        dragNodes: NodeApi<TreeNode>[]
        parentNode: NodeApi<TreeNode> | null
      }) => {
        const dragged = dragNodes[0]
        if (!dragged || !dragged.data.isFile) return
        const src = dragged.data.path
        const destDir = parentNode ? parentNode.data.path : ''
        const baseName = src.includes('/') ? src.slice(src.lastIndexOf('/') + 1) : src
        const target = destDir ? `${destDir}/${baseName}` : baseName
        onTreeMove(namespace, project, src, target)
      }
    : undefined

  const copyDeepLink = (path: string) => {
    const url = `${window.location.origin}/p/${encodeURIComponent(
      namespace,
    )}/${encodeURIComponent(project)}/blob/${path}`
    void navigator.clipboard?.writeText(url).catch(() => {})
  }

  return (
    <div ref={wrapRef} className={styles.wrap}>
      <Tree<TreeNode>
        ref={treeRef}
        dndManager={dndManager as never}
        data={data}
        idAccessor="id"
        childrenAccessor="children"
        initialOpenState={initialOpenState}
        openByDefault={false}
        width={size.w}
        height={size.h}
        indent={14}
        rowHeight={24}
        disableMultiSelection
        selection={activePath ?? undefined}
        onMove={handleMove}
        onRename={handleRename}
        disableEdit={(d: TreeNode) => !canRename || !d.isFile}
        disableDrag={(d: TreeNode) => !canDrag || !d.isFile}
        onActivate={(node) => {
          if (node.data.isFile) openFile(node.data.path)
          else node.toggle()
        }}
      >
        {(props) => (
          <Row
            {...props}
            activePath={activePath}
            isReserved={props.node.data.isFile && reserved.has(props.node.data.path)}
            onContext={
              renderContextMenu
                ? (e, node) => {
                    if (!node.isFile) return
                    e.preventDefault()
                    setMenu({ x: e.clientX, y: e.clientY, node })
                  }
                : undefined
            }
          />
        )}
      </Tree>

      {menu && renderContextMenu && (
        <ContextMenuOverlay onClose={() => setMenu(null)}>
          {renderContextMenu({
            node: menu.node,
            ns: namespace,
            proj: project,
            x: menu.x,
            y: menu.y,
            onClose: () => setMenu(null),
            onRename: () => {
              setMenu(null)
              void treeRef.current?.edit(menu.node.id)
            },
            onCopyLink: () => {
              setMenu(null)
              copyDeepLink(menu.node.path)
            },
          })}
        </ContextMenuOverlay>
      )}
    </div>
  )
}

function ContextMenuOverlay({
  onClose,
  children,
}: {
  onClose: () => void
  children: React.ReactNode
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return (
    <>
      <div
        className={styles.ctxOverlay}
        onClick={onClose}
        onContextMenu={(e) => {
          e.preventDefault()
          onClose()
        }}
      />
      {children}
    </>
  )
}

function Row({
  node,
  style,
  dragHandle,
  activePath,
  isReserved,
  onContext,
}: NodeRendererProps<TreeNode> & {
  activePath: string | null
  isReserved: boolean
  onContext?: (e: React.MouseEvent, node: TreeNode) => void
}) {
  const isActive = node.data.isFile && node.data.path === activePath
  return (
    <div
      ref={dragHandle}
      style={style}
      className={styles.row}
      data-active={isActive}
      data-reserved={isReserved}
      data-dir-path={!node.data.isFile ? node.data.path : undefined}
      onClick={(e) => {
        e.stopPropagation()
        if (node.data.isFile) node.activate()
        else node.toggle()
      }}
      onContextMenu={onContext ? (e) => onContext(e, node.data) : undefined}
    >
      <span className={styles.chevron} data-dir={!node.data.isFile}>
        {!node.data.isFile && (
          <svg
            width="12"
            height="12"
            viewBox="0 0 12 12"
            style={{
              transform: node.isOpen ? 'rotate(90deg)' : 'none',
              transition: 'transform 0.1s',
            }}
          >
            <path d="M4 2.5L8 6l-4 3.5z" fill="currentColor" />
          </svg>
        )}
      </span>
      <span className={styles.icon}>
        {node.data.isFile ? <FileIcon /> : <DirIcon open={node.isOpen} />}
      </span>
      {node.isEditing ? (
        <RenameInput node={node} />
      ) : (
        <span className={styles.name}>{node.data.name}</span>
      )}
    </div>
  )
}

function RenameInput({ node }: { node: NodeApi<TreeNode> }) {
  const ref = useRef<HTMLInputElement>(null)
  useEffect(() => {
    ref.current?.focus()
    ref.current?.select()
  }, [])
  return (
    <input
      ref={ref}
      className={styles.editInput}
      defaultValue={node.data.name}
      onClick={(e) => e.stopPropagation()}
      onBlur={() => node.reset()}
      onKeyDown={(e) => {
        e.stopPropagation()
        if (e.key === 'Escape') node.reset()
        if (e.key === 'Enter') node.submit(ref.current?.value ?? '')
      }}
    />
  )
}

function FileIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
      <path
        d="M4 2h5l3 3v9H4z"
        stroke="currentColor"
        strokeWidth="1.2"
        strokeLinejoin="round"
      />
      <path d="M9 2v3h3" stroke="currentColor" strokeWidth="1.2" strokeLinejoin="round" />
    </svg>
  )
}

function DirIcon({ open }: { open: boolean }) {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
      <path
        d="M2 4.5h4l1.2 1.4H14V13H2z"
        stroke="currentColor"
        strokeWidth="1.2"
        strokeLinejoin="round"
        fill={open ? 'currentColor' : 'none'}
        fillOpacity={open ? 0.12 : 0}
      />
    </svg>
  )
}
