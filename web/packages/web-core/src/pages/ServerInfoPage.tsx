import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { getServerNetworkInfo } from '../lib/serverInfoOps'
import type { NetworkElement } from '../lib/types'
import styles from './ServerInfoPage.module.css'

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      className={styles.copyBtn}
      onClick={() => {
        navigator.clipboard.writeText(text)
        setCopied(true)
        setTimeout(() => setCopied(false), 1500)
      }}
    >
      {copied ? 'Copied' : 'Copy'}
    </button>
  )
}

function ElementCard({ el }: { el: NetworkElement }) {
  return (
    <div>
      <div className={styles.elementHeader}>
        <span className={styles.statusDot} data-status={el.status} />
        <span className={styles.elementLabel}>{el.label}</span>
      </div>
      <div className={styles.row}>
        <span className={styles.label}>Listen</span>
        <span className={styles.value}>{el.listen_address}</span>
      </div>
      {el.external_url && (
        <div className={styles.row}>
          <span className={styles.label}>External URL</span>
          <span className={styles.value}>
            {el.external_url}
            <CopyButton text={el.external_url} />
          </span>
        </div>
      )}
      {el.description && (
        <div className={styles.row}>
          <span className={styles.label}>Description</span>
          <span className={styles.value}>{el.description}</span>
        </div>
      )}
    </div>
  )
}

export function ServerInfoPage() {
  const q = useQuery({ queryKey: ['server-network-info'], queryFn: getServerNetworkInfo })

  return (
    <div className={styles.page}>
      <h1 className={styles.title}>Server Info</h1>
      <p className={styles.intro}>
        Network endpoints this server is listening on. Use these addresses when configuring
        MCP clients, agents, or external integrations.
      </p>

      {q.isLoading ? (
        <p>Loading…</p>
      ) : q.data && q.data.length > 0 ? (
        <div className={styles.card} data-testid="server-info-elements">
          {q.data.map((el, i) => (
            <div key={el.label}>
              {i > 0 && <hr className={styles.separator} />}
              <ElementCard el={el} />
            </div>
          ))}
        </div>
      ) : (
        <p>No network endpoints configured.</p>
      )}
    </div>
  )
}
