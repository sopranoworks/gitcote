import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { serverSshInfo } from '../ops/userSshKeyOps'
import styles from './CloneUrl.module.css'

function selectFallback(text: string): boolean {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  const ok = document.execCommand('copy')
  document.body.removeChild(ta)
  return ok
}

export function CloneUrl({ namespace, project }: { namespace: string; project: string }) {
  const [copiedIdx, setCopiedIdx] = useState<number | null>(null)
  const base = window.location.origin
  const httpUrl = `${base}/${namespace}/${project}.git`

  const { data: sshInfo } = useQuery({
    queryKey: ['server-ssh-info'],
    queryFn: serverSshInfo,
    staleTime: 60_000,
  })

  const host = window.location.hostname
  let sshUrl: string | null = null
  if (sshInfo?.enabled && sshInfo.port) {
    if (sshInfo.port === 22) {
      sshUrl = `git@${host}:${namespace}/${project}.git`
    } else {
      sshUrl = `ssh://git@${host}:${sshInfo.port}/${namespace}/${project}.git`
    }
  }

  function handleCopy(url: string, idx: number) {
    const markCopied = () => {
      setCopiedIdx(idx)
      setTimeout(() => setCopiedIdx(null), 2000)
    }
    if (navigator.clipboard) {
      navigator.clipboard
        .writeText(url)
        .then(() => markCopied())
        .catch(() => {
          if (selectFallback(url)) markCopied()
        })
    } else {
      if (selectFallback(url)) markCopied()
    }
  }

  return (
    <div style={{ marginTop: '1rem', display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
      <div className={styles.container}>
        <span className={styles.label}>HTTP</span>
        <code className={styles.url}>{httpUrl}</code>
        <button className={styles.copyBtn} onClick={() => void handleCopy(httpUrl, 0)}>
          {copiedIdx === 0 ? 'Copied' : 'Copy'}
        </button>
      </div>
      {sshUrl && (
        <div className={styles.container}>
          <span className={styles.label}>SSH</span>
          <code className={styles.url}>{sshUrl}</code>
          <button className={styles.copyBtn} onClick={() => void handleCopy(sshUrl!, 1)}>
            {copiedIdx === 1 ? 'Copied' : 'Copy'}
          </button>
        </div>
      )}
    </div>
  )
}
