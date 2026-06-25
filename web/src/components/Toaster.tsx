import { useToast } from '@shoka/web-core'
import styles from './Toaster.module.css'

export function Toaster() {
  const { toasts, dismiss } = useToast()
  if (toasts.length === 0) return null
  return (
    <div className={styles.toaster} aria-live="polite">
      {toasts.map((t) => (
        <div key={t.id} className={styles.toast} data-level={t.level} role="alert">
          <span className={styles.text}>{t.text}</span>
          <button
            className={styles.close}
            onClick={() => dismiss(t.id)}
            aria-label="Dismiss"
          >
            ×
          </button>
        </div>
      ))}
    </div>
  )
}
