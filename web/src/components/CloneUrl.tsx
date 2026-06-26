import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { serverSshInfo } from '../ops/userSshKeyOps'
import styles from './CloneUrl.module.css'

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

  async function handleCopy(url: string, idx: number) {
    try {
      await navigator.clipboard.writeText(url)
      setCopiedIdx(idx)
      setTimeout(() => setCopiedIdx(null), 2000)
    } catch {
      // fallback
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
