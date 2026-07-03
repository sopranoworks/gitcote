import { useEffect, useState } from 'react'
import styles from './ConfirmDialog.module.css'

// RenameDialog is the ns/proj RENAME flow (B-28), deliberately DISTINCT from BOTH move and
// delete: it is a LOW-FRICTION edit-the-name input pre-filled with the current name — NOT the
// pick-a-target-namespace dropdown of MoveProjectDialog, and NOT the type-the-exact-name
// gate of TypeToConfirmDialog (renaming is not destructive; no danger styling). Confirm
// enables once the value is non-empty, valid, and DIFFERENT from the current name. The caller
// gates which control is shown (project rename = admin-on-ns; namespace rename = super-user).
const NAME_RE = /^[A-Za-z0-9_-]+$/

export interface RenameDialogProps {
  open: boolean
  // What is being renamed — drives the heading/label only.
  kind: 'project' | 'namespace'
  // The current name, pre-filled into the input and used for the "must differ" check.
  currentName: string
  onConfirm: (newName: string) => void
  onCancel: () => void
}

export function RenameDialog({ open, kind, currentName, onConfirm, onCancel }: RenameDialogProps) {
  const [value, setValue] = useState(currentName)

  // Seed the field with the current name each time the dialog opens.
  useEffect(() => {
    if (open) setValue(currentName)
  }, [open, currentName])

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

  const trimmed = value.trim()
  const valid = NAME_RE.test(trimmed)
  const changed = trimmed !== currentName
  const canConfirm = trimmed !== '' && valid && changed
  // Inline, non-fatal hints (collision is surfaced by the server response after submit).
  const hint =
    trimmed === ''
      ? 'Enter a new name.'
      : !valid
        ? 'Only letters, digits, hyphen, and underscore are allowed.'
        : !changed
          ? 'Enter a name different from the current one.'
          : ''

  const submit = () => {
    if (!canConfirm) return
    onConfirm(trimmed)
  }

  const label = kind === 'namespace' ? 'New namespace name' : 'New project name'
  return (
    <div className={styles.overlay} onClick={onCancel}>
      <div
        className={styles.dialog}
        role="dialog"
        aria-modal="true"
        aria-label={`Rename ${kind} ${currentName}`}
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className={styles.title}>
          Rename {kind} {currentName}
        </h2>
        <p className={styles.message}>
          Rename <strong>{currentName}</strong>. Its history is preserved; nothing is deleted or moved.
        </p>
        <label className={styles.message}>
          {label}
          <input
            className={styles.input}
            value={value}
            autoFocus
            aria-label={label}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                submit()
              }
            }}
          />
        </label>
        {hint && <div className={styles.message}>{hint}</div>}
        <div className={styles.actions}>
          <button className={styles.cancel} onClick={onCancel}>
            Cancel
          </button>
          <button
            className={styles.confirm}
            onClick={submit}
            disabled={!canConfirm}
            title={canConfirm ? '' : hint}
          >
            Rename
          </button>
        </div>
      </div>
    </div>
  )
}
