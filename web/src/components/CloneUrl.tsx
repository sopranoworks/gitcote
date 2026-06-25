import { useState } from 'react'
import styles from './CloneUrl.module.css'

export function CloneUrl({ namespace, project }: { namespace: string; project: string }) {
  const [copied, setCopied] = useState(false)
  const base = window.location.origin
  const url = `${base}/${namespace}/${project}.git`

  async function handleCopy() {
    try {
      await navigator.clipboard.writeText(url)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // fallback: select text
    }
  }

  return (
    <div className={styles.container}>
      <span className={styles.label}>Clone</span>
      <code className={styles.url}>{url}</code>
      <button className={styles.copyBtn} onClick={() => void handleCopy()}>
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  )
}
