import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useParams, useSearch, useElementScrollRestoration } from '@tanstack/react-router'
import { useFileQuery, useTreeQuery } from '../lib/queries'
import { classifyFile, isHighlightableCode } from '../lib/fileKind'
import { Markdown } from '../components/Markdown'
import { FileSearchBar } from '../components/FileSearchBar'
import { useContentConfig } from '../lib/contentConfig'
import type { FileNode } from '../lib/types'
import styles from './FilePage.module.css'

const CodeView = lazy(() => import('../components/CodeView'))

function findModifiedAt(nodes: FileNode[], target: string): string | undefined {
  for (const n of nodes) {
    if (!n.isDir && n.path === target) return n.modifiedAt
    if (n.isDir && n.children) {
      const found = findModifiedAt(n.children, target)
      if (found) return found
    }
  }
}

function formatDate(iso: string): string {
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function CopyPathButton({ path }: { path: string }) {
  const [copied, setCopied] = useState(false)
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  const handleCopy = () => {
    navigator.clipboard?.writeText(path).catch(() => {})
    setCopied(true)
    clearTimeout(timerRef.current)
    timerRef.current = setTimeout(() => setCopied(false), 1500)
  }

  useEffect(() => () => clearTimeout(timerRef.current), [])

  return (
    <button
      className={styles.copyBtn}
      onClick={handleCopy}
      title={copied ? 'Copied!' : 'Copy file path'}
      aria-label="Copy file path"
      data-testid="copy-path-button"
    >
      {copied ? (
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
          <path
            d="M3 8.5l3 3 7-7"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      ) : (
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
          <rect
            x="5" y="5" width="8" height="8" rx="1"
            stroke="currentColor" strokeWidth="1.3"
          />
          <path
            d="M11 5V3.5A1.5 1.5 0 009.5 2h-6A1.5 1.5 0 002 3.5v6A1.5 1.5 0 003.5 11H5"
            stroke="currentColor" strokeWidth="1.3"
          />
        </svg>
      )}
    </button>
  )
}

export function BlobPage() {
  const { namespace, project, _splat } = useParams({ strict: false }) as {
    namespace: string
    project: string
    _splat?: string
  }
  const path = _splat ?? ''
  const search = useSearch({ strict: false }) as Record<string, unknown>
  const highlight =
    typeof search.highlight === 'string' ? search.highlight : undefined

  const { data, isError } = useFileQuery(namespace, project, path)
  const { data: tree } = useTreeQuery(namespace, project)
  const { renderEditButton } = useContentConfig()
  const modifiedAt = useMemo(
    () => (tree && path ? findModifiedAt(tree, path) : undefined),
    [tree, path],
  )

  const scrollEntry = useElementScrollRestoration({ id: 'file-body' })
  const bodyRef = useRef<HTMLDivElement>(null)
  const restoredRef = useRef(false)
  useEffect(() => {
    if (restoredRef.current) return
    if (data && scrollEntry?.scrollY && bodyRef.current) {
      bodyRef.current.scrollTop = scrollEntry.scrollY
      restoredRef.current = true
    }
  }, [data, scrollEntry])

  const [searchOpen, setSearchOpen] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')

  useEffect(() => {
    if (highlight) {
      setSearchOpen(true)
      setSearchQuery(highlight)
    }
  }, [highlight])

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 'f') {
        e.preventDefault()
        setSearchOpen(true)
      }
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [])

  const closeSearch = useCallback(() => {
    setSearchOpen(false)
    if (highlight) {
      const url = new URL(window.location.href)
      url.searchParams.delete('highlight')
      window.history.replaceState({}, '', url.toString())
    }
  }, [highlight])

  const kind = data ? classifyFile(path, data.content) : null

  return (
    <div className={styles.page}>
      <div className={styles.toolbar}>
        <div className={styles.toolbarLeft}>
          <span className={styles.filePath} title={path}>
            {path}
          </span>
          <CopyPathButton path={path} />
        </div>
        <div className={styles.toolbarRight}>
          {modifiedAt && (
            <span className={styles.fileDate} title={modifiedAt}>
              {formatDate(modifiedAt)}
            </span>
          )}
          <button
            className={styles.searchToggle}
            onClick={() => setSearchOpen((v) => !v)}
            title="Find in file (Ctrl+F)"
            aria-label="Toggle file search"
            data-active={searchOpen}
          >
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
              <circle cx="7" cy="7" r="4.5" stroke="currentColor" strokeWidth="1.3" />
              <path d="M10.5 10.5l3 3" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
            </svg>
          </button>
          {!isError && renderEditButton && renderEditButton(namespace, project, path, styles)}
        </div>
      </div>

      {searchOpen && data && (
        <FileSearchBar
          containerRef={bodyRef}
          initialQuery={searchQuery}
          contentKey={data.content}
          onClose={closeSearch}
        />
      )}

      <div
        ref={bodyRef}
        className={styles.body}
        data-scroll-restoration-id="file-body"
      >
        {isError ? (
          <div className={styles.error}>
            File not found: <code>{path}</code>
          </div>
        ) : !data ? (
          <div className={styles.loading}>Loading…</div>
        ) : kind === 'markdown' ? (
          <Markdown content={data.content} />
        ) : kind === 'binary' ? (
          <div className={styles.placeholder}>
            Binary file — cannot preview.
          </div>
        ) : isHighlightableCode(path) ? (
          <Suspense fallback={<pre className={styles.plain}>{data.content}</pre>}>
            <CodeView path={path} content={data.content} />
          </Suspense>
        ) : (
          <pre className={styles.plain}>{data.content}</pre>
        )}
      </div>
    </div>
  )
}
