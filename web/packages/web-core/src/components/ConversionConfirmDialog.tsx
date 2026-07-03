import { useEffect } from 'react'
import type { ConversionCandidate } from '../lib/fileConvert'
import styles from './ConfirmDialog.module.css'

export interface ConversionConfirmDialogProps {
  open: boolean
  candidates: ConversionCandidate[]
  onConfirm: () => void
  onCancel: () => void
}

export function ConversionConfirmDialog({
  open,
  candidates,
  onConfirm,
  onCancel,
}: ConversionConfirmDialogProps) {
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

  if (!open || candidates.length === 0) return null

  const single = candidates.length === 1
  const c = candidates[0]

  return (
    <div className={styles.overlay} onClick={onCancel} data-testid="conversion-dialog">
      <div
        className={styles.dialog}
        role="dialog"
        aria-modal="true"
        aria-label="File Conversion Required"
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className={styles.title}>File Conversion Required</h2>
        <div className={styles.message}>
          {single ? (
            <>
              <p style={{ margin: '0 0 8px' }}>
                &ldquo;{c.originalName}&rdquo; will be converted to Markdown before uploading:
              </p>
              <ul style={{ margin: 0, paddingLeft: '1.5em' }}>
                {c.type === '.csv' && <li>CSV files are converted to Markdown tables</li>}
                <li>The file will be saved as &ldquo;{c.convertedName}&rdquo;</li>
              </ul>
            </>
          ) : (
            <>
              <p style={{ margin: '0 0 8px' }}>
                The following files will be converted to Markdown before uploading:
              </p>
              <ul style={{ margin: 0, paddingLeft: '1.5em' }}>
                {candidates.map((item) => (
                  <li key={item.originalName}>
                    &ldquo;{item.originalName}&rdquo; → &ldquo;{item.convertedName}&rdquo;
                    {item.type === '.csv' && ' (CSV → Markdown table)'}
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
        <div className={styles.actions}>
          <button className={styles.cancel} onClick={onCancel}>
            Cancel
          </button>
          <button className={styles.confirm} onClick={onConfirm} autoFocus>
            Convert and Upload
          </button>
        </div>
      </div>
    </div>
  )
}
