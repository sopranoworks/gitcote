import { useEffect, useState } from 'react'
import styles from './ConfirmDialog.module.css'

// A single-line text-input modal (used for "Save as…" path entry). In-app, not
// a native prompt(): focusable, Escape-cancellable, unit-testable.
export interface PromptDialogProps {
  open: boolean
  title?: string
  label: string
  defaultValue?: string
  confirmLabel?: string
  cancelLabel?: string
  // Optional synchronous validator: return an error message to block submit and
  // show it inline, or null when the value is acceptable.
  validate?: (value: string) => string | null
  onConfirm: (value: string) => void
  onCancel: () => void
}

export function PromptDialog({
  open,
  title,
  label,
  defaultValue = '',
  confirmLabel = 'Save',
  cancelLabel = 'Cancel',
  validate,
  onConfirm,
  onCancel,
}: PromptDialogProps) {
  const [value, setValue] = useState(defaultValue)
  const [error, setError] = useState<string | null>(null)

  // Seed the field each time the dialog opens.
  useEffect(() => {
    if (open) {
      setValue(defaultValue)
      setError(null)
    }
  }, [open, defaultValue])

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

  const submit = () => {
    const v = value.trim()
    if (!v) return
    if (validate) {
      const msg = validate(v)
      if (msg) {
        setError(msg)
        return
      }
    }
    onConfirm(v)
  }

  return (
    <div className={styles.overlay} onClick={onCancel}>
      <div
        className={styles.dialog}
        role="dialog"
        aria-modal="true"
        aria-label={title ?? label}
        onClick={(e) => e.stopPropagation()}
      >
        {title && <h2 className={styles.title}>{title}</h2>}
        <label className={styles.message}>
          {label}
          <input
            className={styles.input}
            value={value}
            autoFocus
            onChange={(e) => {
              setValue(e.target.value)
              if (error) setError(null)
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                submit()
              }
            }}
          />
        </label>
        {error && <div className={styles.error}>{error}</div>}
        <div className={styles.actions}>
          <button className={styles.cancel} onClick={onCancel}>
            {cancelLabel}
          </button>
          <button className={styles.confirm} onClick={submit}>
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}
