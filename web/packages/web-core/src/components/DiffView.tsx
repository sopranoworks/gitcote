import { useEffect, useMemo } from 'react'
import { lineDiff } from '../lib/lineDiff'
import styles from './DiffView.module.css'

export interface DiffViewProps {
  open: boolean
  serverContent: string
  bufferContent: string
  onClose: () => void
}

export function DiffView({
  open,
  serverContent,
  bufferContent,
  onClose,
}: DiffViewProps) {
  const rows = useMemo(
    () => (open ? lineDiff(serverContent, bufferContent) : []),
    [open, serverContent, bufferContent],
  )

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        onClose()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  if (!open) return null

  return (
    <div className={styles.overlay} onClick={onClose}>
      <div
        className={styles.dialog}
        role="dialog"
        aria-modal="true"
        aria-label="Diff: server vs your edits"
        onClick={(e) => e.stopPropagation()}
      >
        <div className={styles.head}>
          <span className={styles.legend}>
            <span className={styles.delTag}>− server</span>
            <span className={styles.addTag}>+ yours</span>
          </span>
          <button className={styles.close} aria-label="Close diff" onClick={onClose}>
            ×
          </button>
        </div>
        <div className={styles.body}>
          {rows.map((r, i) => (
            <div key={i} className={styles[r.type]}>
              <span className={styles.gutter}>
                {r.type === 'add' ? '+' : r.type === 'del' ? '−' : ' '}
              </span>
              <span className={styles.line}>{r.text || ' '}</span>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
