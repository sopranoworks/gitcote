import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { librarianStatus, refreshLibrarianStatus, reloadLibrarianConfig } from '../lib/librarianStatus'
import styles from './LibrarianStatusPage.module.css'

// Human labels for the server's health kinds (B-73). Never shows the API key.
const KIND_LABEL: Record<string, string> = {
  ready: 'Ready',
  model_not_found: 'Model not found',
  auth_failed: 'Authentication failed (check the environment API key)',
  unreachable: 'Endpoint unreachable',
  misconfigured: 'Misconfigured',
  unconfigured: 'Not configured',
  unknown: 'Not checked yet',
}

// LibrarianStatusPage shows the ask_the_librarian LLM health (provider, model,
// connectivity) and a manual Refresh that re-runs the one-call check on demand.
// The page reads the CACHED snapshot on load — only Refresh makes a real call.
export function LibrarianStatusPage() {
  const qc = useQueryClient()
  const [refreshing, setRefreshing] = useState(false)
  const [reloading, setReloading] = useState(false)
  const q = useQuery({ queryKey: ['librarian-status'], queryFn: librarianStatus })

  async function onRefresh() {
    setRefreshing(true)
    try {
      const fresh = await refreshLibrarianStatus()
      qc.setQueryData(['librarian-status'], fresh)
    } finally {
      setRefreshing(false)
    }
  }

  async function onReload() {
    setReloading(true)
    try {
      const fresh = await reloadLibrarianConfig()
      qc.setQueryData(['librarian-status'], fresh)
    } finally {
      setReloading(false)
    }
  }

  const s = q.data
  const kind = s?.kind ?? 'unknown'
  const ok = kind === 'ready'

  return (
    <div className={styles.page}>
      <h1 className={styles.title}>Librarian</h1>
      <p className={styles.intro}>
        Health of the ask_the_librarian LLM — its provider, model, and connectivity. The API key is read
        from the server environment and is never shown here.
      </p>

      {q.isLoading ? (
        <p>Loading…</p>
      ) : (
        <div className={styles.card}>
          <div className={styles.row}>
            <span className={styles.label}>Status</span>
            <span className={ok ? styles.ok : styles.bad} data-testid="librarian-kind">
              {KIND_LABEL[kind] ?? kind}
            </span>
          </div>
          {s?.provider && (
            <div className={styles.row}>
              <span className={styles.label}>Provider</span>
              <span>{s.provider}</span>
            </div>
          )}
          {s?.model && (
            <div className={styles.row}>
              <span className={styles.label}>Model</span>
              <span>{s.model}</span>
            </div>
          )}
          {s?.detail && (
            <div className={styles.row}>
              <span className={styles.label}>Detail</span>
              <span data-testid="librarian-detail">{s.detail}</span>
            </div>
          )}
          {s?.checkedAt && (
            <div className={styles.row}>
              <span className={styles.label}>Last checked</span>
              <span>{s.checkedAt}</span>
            </div>
          )}
        </div>
      )}

      <div className={styles.actions}>
        <button
          className={styles.refresh}
          onClick={onRefresh}
          disabled={refreshing || reloading}
          data-testid="librarian-refresh"
        >
          {refreshing ? 'Checking…' : 'Refresh status'}
        </button>
        <button
          className={styles.refresh}
          onClick={onReload}
          disabled={refreshing || reloading}
          data-testid="librarian-reload"
        >
          {reloading ? 'Reloading…' : 'Reload from config file'}
        </button>
      </div>
      <p className={styles.intro}>
        To change the librarian's model or provider without restarting Shoka, edit the server's config
        file (the <code>llm</code> block), then Reload. Shoka re-reads the file and runs a connection test;
        on success the change takes effect immediately and persists because it lives in the file you edited
        (Shoka never writes the config). On failure the previous setting stays and the reason is shown above.
      </p>
    </div>
  )
}
