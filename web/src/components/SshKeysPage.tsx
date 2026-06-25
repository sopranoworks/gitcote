import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useIsSuperUser, useManagesAnyNamespace } from '@shoka/web-core'
import { useToast } from '@shoka/web-core'
import { seedKeyGenerate, seedKeyList, seedKeyDelete, type SSHKeyInfo } from '../ops/seedOps'
import styles from './SshKeysPage.module.css'

interface NamespaceKeys {
  namespace: string
  keys: SSHKeyInfo[]
}

export function SshKeysPage() {
  const isSuperUser = useIsSuperUser()
  const managesAny = useManagesAnyNamespace()
  const qc = useQueryClient()
  const { add: toast } = useToast()

  const { data: healthData } = useQuery({
    queryKey: ['namespace-health'],
    queryFn: async () => {
      const { wsClient } = await import('@shoka/web-core')
      return wsClient().request<{ namespaces: { name: string }[] }>('NAMESPACE_HEALTH', {})
    },
    staleTime: 30_000,
  })

  const namespaces = healthData?.namespaces?.map((n) => n.name) ?? []

  const { data: allKeys } = useQuery({
    queryKey: ['ssh-keys-all', namespaces.join(',')],
    queryFn: async () => {
      const results: NamespaceKeys[] = []
      for (const ns of namespaces) {
        const { keys } = await seedKeyList(ns)
        results.push({ namespace: ns, keys: keys ?? [] })
      }
      return results
    },
    enabled: namespaces.length > 0,
    staleTime: 30_000,
  })

  const [genNs, setGenNs] = useState('')
  const [genName, setGenName] = useState('')
  const [generating, setGenerating] = useState(false)
  const [showPubKey, setShowPubKey] = useState<string | null>(null)

  async function handleGenerate() {
    if (!genNs || !genName.trim()) return
    setGenerating(true)
    try {
      const result = await seedKeyGenerate(genNs, genName.trim())
      setShowPubKey(result.publicKey)
      setGenName('')
      void qc.invalidateQueries({ queryKey: ['ssh-keys-all'] })
      void qc.invalidateQueries({ queryKey: ['seed-keys'] })
    } catch (e) {
      toast({ level: 'warn', text: e instanceof Error ? e.message : 'Failed to generate key' })
    } finally {
      setGenerating(false)
    }
  }

  async function handleDelete(ns: string, name: string) {
    if (!confirm(`Delete SSH key "${name}" in namespace "${ns}"?`)) return
    try {
      await seedKeyDelete(ns, name)
      toast({ level: 'warn', text: `Deleted key "${name}".` })
      void qc.invalidateQueries({ queryKey: ['ssh-keys-all'] })
      void qc.invalidateQueries({ queryKey: ['seed-keys'] })
    } catch (e) {
      toast({ level: 'warn', text: e instanceof Error ? e.message : 'Failed to delete key' })
    }
  }

  if (!isSuperUser && !managesAny) {
    return (
      <div className={styles.page}>
        <h1 className={styles.title}>SSH Keys</h1>
        <p className={styles.muted}>You do not have permission to manage SSH keys.</p>
      </div>
    )
  }

  return (
    <div className={styles.page}>
      <h1 className={styles.title}>SSH Keys</h1>
      <p className={styles.muted}>
        SSH keys are managed at the namespace level. A key in a namespace is
        available to all projects within it for seed push.
      </p>

      {(allKeys ?? []).map((nk) => (
        <section key={nk.namespace} className={styles.nsBlock}>
          <h2 className={styles.nsName}>{nk.namespace}</h2>
          {nk.keys.length > 0 ? (
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Algorithm</th>
                  <th>Fingerprint</th>
                  <th>Created</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {nk.keys.map((k) => (
                  <tr key={k.name}>
                    <td className={styles.mono}>{k.name}</td>
                    <td>{k.algorithm}</td>
                    <td className={styles.mono}>{k.fingerprint}</td>
                    <td>{k.created_at ? new Date(k.created_at).toLocaleDateString() : ''}</td>
                    <td>
                      <button className={styles.btnDanger} onClick={() => handleDelete(nk.namespace, k.name)}>
                        Delete
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <p className={styles.muted}>No keys in this namespace.</p>
          )}
        </section>
      ))}

      {namespaces.length === 0 && (
        <p className={styles.muted}>No namespaces yet.</p>
      )}

      <section className={styles.genSection}>
        <h2 className={styles.nsName}>Generate new key</h2>
        <div className={styles.genRow}>
          <select
            className={styles.select}
            value={genNs}
            onChange={(e) => setGenNs(e.target.value)}
          >
            <option value="">— namespace —</option>
            {namespaces.map((ns) => (
              <option key={ns} value={ns}>{ns}</option>
            ))}
          </select>
          <input
            className={styles.input}
            type="text"
            placeholder="Key name"
            value={genName}
            onChange={(e) => setGenName(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') void handleGenerate() }}
          />
          <button className={styles.btn} onClick={() => void handleGenerate()} disabled={generating || !genNs || !genName.trim()}>
            {generating ? 'Generating…' : 'Generate'}
          </button>
        </div>

        {showPubKey && (
          <div className={styles.pubKeyBox}>
            <p className={styles.muted}>Public key (copy and add as a deploy key on your seed host):</p>
            <pre className={styles.pubKey}>{showPubKey}</pre>
            <button className={styles.btn} onClick={() => setShowPubKey(null)}>Dismiss</button>
          </div>
        )}
      </section>
    </div>
  )
}
