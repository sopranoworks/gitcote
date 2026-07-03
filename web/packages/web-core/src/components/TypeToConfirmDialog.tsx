import { useEffect, useState } from 'react'
import styles from './ConfirmDialog.module.css'

// A GitHub-repository-style "type the exact name to confirm" modal for irreversible ops
// (B-28 part 2). Confirm is enabled ONLY when the typed text === the exact expected name —
// the high-friction gate the operator requires for every destructive namespace/project
// delete. Reused for both project and namespace delete, parameterised by the entity kind
// and exact name. In-app (no native confirm), Escape-cancellable, unit-testable.
export interface TypeToConfirmDialogProps {
  open: boolean
  title: string
  message: string
  // expected is the exact name the operator must type before Confirm enables.
  expected: string
  confirmLabel?: string
  cancelLabel?: string
  onConfirm: () => void
  onCancel: () => void
}

export function TypeToConfirmDialog({
  open,
  title,
  message,
  expected,
  confirmLabel = 'Delete',
  cancelLabel = 'Cancel',
  onConfirm,
  onCancel,
}: TypeToConfirmDialogProps) {
  const [value, setValue] = useState('')

  useEffect(() => {
    if (open) setValue('')
  }, [open])

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        onCancel()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onCancel])

  if (!open) return null

  const matches = value.trim() === expected

  return (
    <div className={styles.overlay} onClick={onCancel}>
      <div
        className={styles.dialog}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className={styles.title}>{title}</h2>
        <p className={styles.message}>{message}</p>
        <label className={styles.message}>
          Type <strong>{expected}</strong> to confirm:
          <input
            className={styles.input}
            value={value}
            autoFocus
            aria-label="confirm name"
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && matches) {
                e.preventDefault()
                onConfirm()
              }
            }}
          />
        </label>
        <div className={styles.actions}>
          <button className={styles.cancel} onClick={onCancel}>
            {cancelLabel}
          </button>
          <button
            className={styles.danger}
            onClick={onConfirm}
            disabled={!matches}
            title={matches ? '' : `Type the exact name (${expected}) to enable`}
          >
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}
