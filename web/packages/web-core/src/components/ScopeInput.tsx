import { useState, useRef, useEffect } from 'react'
import { useProjectsQuery } from '../lib/queries'
import type { ProjectInfo } from '../lib/types'
import styles from './ScopeInput.module.css'
import dialogStyles from './ConfirmDialog.module.css'

interface ScopeInputProps {
  value: string
  onChange: (value: string) => void
  disabled?: boolean
  placeholder?: string
  className?: string
  'data-testid'?: string
}

interface Suggestion {
  label: string
  replacement: string
  detail?: string
}

function getCurrentSegment(value: string, cursor: number) {
  let start = 0
  for (let i = cursor - 1; i >= 0; i--) {
    if (value[i] === ',') { start = i + 1; break }
  }
  let end = value.length
  for (let i = cursor; i < value.length; i++) {
    if (value[i] === ',') { end = i; break }
  }
  return { start, end, text: value.slice(start, end) }
}

function buildSuggestions(text: string, projects: ProjectInfo[]): Suggestion[] {
  const trimmed = text.trim()
  if (!trimmed || trimmed === '*') return []

  let zone = ''
  let body = trimmed
  if (body.startsWith('git/')) { zone = 'git/'; body = body.slice(4) }

  const parts = body.split(':')
  const namespaces = [...new Set(projects.map(p => p.namespace))].sort()

  if (parts.length === 1) {
    const typed = parts[0].toLowerCase()
    const out: Suggestion[] = []
    if (!zone && 'git/'.startsWith(typed))
      out.push({ label: 'git/', replacement: 'git/', detail: 'zone prefix' })
    for (const ns of namespaces) {
      if (ns.toLowerCase().startsWith(typed))
        out.push({ label: zone + ns, replacement: zone + ns + ':', detail: 'namespace' })
    }
    return out
  }

  if (parts.length === 2) {
    const ns = parts[0]
    const typed = parts[1].toLowerCase()
    return [...new Set(projects.filter(p => p.namespace === ns).map(p => p.name))]
      .sort()
      .filter(p => typed === '' || p.toLowerCase().startsWith(typed))
      .map(p => ({ label: p, replacement: zone + ns + ':' + p + ':', detail: 'project' }))
  }

  if (parts.length === 3) {
    const [ns, proj, typed] = [parts[0], parts[1], parts[2].toLowerCase()]
    return (['r', 'rw', 'admin'] as const)
      .filter(l => l.startsWith(typed) && l !== typed)
      .map(l => ({
        label: l,
        replacement: zone + ns + ':' + proj + ':' + l,
        detail: l === 'r' ? 'read-only' : l === 'rw' ? 'read-write' : 'admin',
      }))
  }

  return []
}

export function ScopeInput(props: ScopeInputProps) {
  const { value, onChange, disabled, placeholder, className, 'data-testid': testId } = props
  const { data: projects = [] } = useProjectsQuery()
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const [cursor, setCursor] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLUListElement>(null)

  const seg = getCurrentSegment(value, cursor)
  const suggestions = open && !disabled ? buildSuggestions(seg.text, projects) : []
  const safeActive = Math.min(active, Math.max(0, suggestions.length - 1))

  const accept = (s: Suggestion) => {
    const before = value.slice(0, seg.start)
    const after = value.slice(seg.end)
    const sp = seg.start > 0 && !before.endsWith(' ') ? ' ' : ''
    const next = before + sp + s.replacement + after
    onChange(next)
    setOpen(true)
    setActive(0)
    requestAnimationFrame(() => {
      const pos = (before + sp + s.replacement).length
      inputRef.current?.setSelectionRange(pos, pos)
      setCursor(pos)
    })
  }

  useEffect(() => {
    if (listRef.current && suggestions.length > 0) {
      const el = listRef.current.children[safeActive] as HTMLElement
      el?.scrollIntoView({ block: 'nearest' })
    }
  }, [safeActive, suggestions.length])

  return (
    <div className={styles.wrapper}>
      <input
        ref={inputRef}
        className={className}
        value={value}
        placeholder={placeholder}
        disabled={disabled}
        data-testid={testId}
        autoComplete="off"
        onChange={e => {
          onChange(e.target.value)
          setCursor(e.target.selectionStart ?? 0)
          setOpen(true)
          setActive(0)
        }}
        onFocus={() => { setCursor(inputRef.current?.selectionStart ?? 0); setOpen(true) }}
        onBlur={() => setTimeout(() => setOpen(false), 150)}
        onClick={() => { setCursor(inputRef.current?.selectionStart ?? 0); setOpen(true) }}
        onKeyDown={e => {
          if (!suggestions.length) return
          switch (e.key) {
            case 'ArrowDown':
              e.preventDefault()
              setActive(i => (i + 1) % suggestions.length)
              break
            case 'ArrowUp':
              e.preventDefault()
              setActive(i => (i - 1 + suggestions.length) % suggestions.length)
              break
            case 'Enter':
            case 'Tab':
              e.preventDefault()
              accept(suggestions[safeActive])
              break
            case 'Escape':
              setOpen(false)
              break
          }
        }}
      />
      {suggestions.length > 0 && (
        <ul ref={listRef} className={styles.dropdown} role="listbox"
          data-testid={testId ? `${testId}-suggestions` : undefined}
        >
          {suggestions.map((s, i) => (
            <li
              key={s.replacement}
              className={`${styles.item}${i === safeActive ? ' ' + styles.active : ''}`}
              role="option"
              aria-selected={i === safeActive}
              onMouseDown={e => { e.preventDefault(); accept(s) }}
              onMouseEnter={() => setActive(i)}
            >
              <span className={styles.label}>{s.label}</span>
              {s.detail && <span className={styles.detail}>{s.detail}</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

export interface ScopeIssue {
  raw: string
  message: string
}

function parseScopeTarget(seg: string): { ns: string; proj: string } | null {
  let body = seg.trim()
  if (!body || body === '*' || body === 'none') return null
  if (body.startsWith('git/')) body = body.slice(4)
  if (body.startsWith('namespace:')) {
    body = body.slice('namespace:'.length)
    for (const s of [':admin', ':rw', ':r']) {
      if (body.endsWith(s)) { body = body.slice(0, -s.length); break }
    }
    const slash = body.indexOf('/')
    if (slash >= 0) return { ns: body.slice(0, slash), proj: body.slice(slash + 1) }
    return body ? { ns: body, proj: '' } : null
  }
  for (const s of [':admin', ':rw', ':r']) {
    if (body.endsWith(s)) { body = body.slice(0, -s.length); break }
  }
  const colon = body.indexOf(':')
  if (colon >= 0) return { ns: body.slice(0, colon), proj: body.slice(colon + 1) }
  return body ? { ns: body, proj: '' } : null
}

export function validateScopeExistence(scope: string, projects: ProjectInfo[]): ScopeIssue[] {
  const trimmed = scope.trim()
  if (!trimmed || trimmed === '*') return []
  const nsSet = new Set(projects.map(p => p.namespace))
  const projSet = new Set(projects.map(p => `${p.namespace}/${p.name}`))
  const issues: ScopeIssue[] = []
  for (const raw of trimmed.split(',')) {
    const seg = raw.trim()
    if (!seg) continue
    const t = parseScopeTarget(seg)
    if (!t) continue
    if (!nsSet.has(t.ns))
      issues.push({ raw: seg, message: `namespace '${t.ns}' not found` })
    else if (t.proj && !projSet.has(`${t.ns}/${t.proj}`))
      issues.push({ raw: seg, message: `project '${t.proj}' not found in '${t.ns}'` })
  }
  return issues
}

export function ScopeConfirmDialog({
  open,
  issues,
  onConfirm,
  onCancel,
}: {
  open: boolean
  issues: ScopeIssue[]
  onConfirm: () => void
  onCancel: () => void
}) {
  useEffect(() => {
    if (!open) return
    const h = (e: KeyboardEvent) => { if (e.key === 'Escape') { e.preventDefault(); onCancel() } }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [open, onCancel])

  if (!open) return null

  return (
    <div className={dialogStyles.overlay} onClick={onCancel}>
      <div className={dialogStyles.dialog} role="dialog" aria-modal="true"
        aria-label="Non-existent scope references"
        onClick={e => e.stopPropagation()}
      >
        <h2 className={dialogStyles.title}>Non-existent scope references</h2>
        <div className={dialogStyles.message}>
          The following scopes reference items that don&apos;t exist:
          <ul>
            {issues.map(i => (
              <li key={i.raw}><code>{i.raw}</code> &mdash; {i.message}</li>
            ))}
          </ul>
          Create this token anyway?
        </div>
        <div className={dialogStyles.actions}>
          <button className={dialogStyles.cancel} onClick={onCancel}>Cancel</button>
          <button className={dialogStyles.confirm} onClick={onConfirm}
            data-testid="scope-confirm-create"
          >
            Create anyway
          </button>
        </div>
      </div>
    </div>
  )
}
