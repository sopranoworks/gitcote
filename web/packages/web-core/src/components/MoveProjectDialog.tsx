import { useEffect, useState } from 'react'
import styles from './ConfirmDialog.module.css'

// MoveProjectDialog is the project-move flow (B-28), deliberately DISTINCT from delete: it
// PICKS a target namespace from a dropdown of the managed set and confirms — it is NOT a
// type-the-name-to-destroy dialog (moving is not destroying; no danger styling). Confirm
// enables once a target namespace is chosen. Reachable only for a super-user (the caller
// gates the control).
export interface MoveProjectDialogProps {
  open: boolean
  // The project being moved, as "<sourceNamespace>/<project>", for the heading.
  sourceNamespace: string
  project: string
  // Candidate target namespaces (the managed set, with the source already excluded).
  targets: string[]
  onConfirm: (target: string) => void
  onCancel: () => void
}

export function MoveProjectDialog({
  open,
  sourceNamespace,
  project,
  targets,
  onConfirm,
  onCancel,
}: MoveProjectDialogProps) {
  const [target, setTarget] = useState('')

  useEffect(() => {
    if (open) setTarget('')
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

  return (
    <div className={styles.overlay} onClick={onCancel}>
      <div
        className={styles.dialog}
        role="dialog"
        aria-modal="true"
        aria-label={`Move project ${project}`}
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className={styles.title}>Move project {project}</h2>
        <p className={styles.message}>
          Move <strong>{sourceNamespace}/{project}</strong> to another namespace. Its history is preserved;
          nothing is deleted.
        </p>
        <label className={styles.message}>
          Target namespace:
          {targets.length === 0 ? (
            <span> no other namespace available — create one first.</span>
          ) : (
            <select
              className={styles.input}
              value={target}
              aria-label="target namespace"
              onChange={(e) => setTarget(e.target.value)}
            >
              <option value="">Choose a namespace…</option>
              {targets.map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </select>
          )}
        </label>
        <div className={styles.actions}>
          <button className={styles.cancel} onClick={onCancel}>
            Cancel
          </button>
          <button
            className={styles.confirm}
            onClick={() => onConfirm(target)}
            disabled={target === ''}
            title={target === '' ? 'Choose a target namespace' : ''}
          >
            Move
          </button>
        </div>
      </div>
    </div>
  )
}
