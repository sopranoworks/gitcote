import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useIsSuperUser } from '@shoka/web-core'
import { useToast } from '@shoka/web-core'
import { seedKeyGenerate, seedKeyList, seedKeyDelete, type SSHKeyInfo } from '../ops/seedOps'
import styles from './SshKeySection.module.css'

export function SshKeySection({ namespace }: { namespace: string }) {
  const isSuperUser = useIsSuperUser()
  if (!isSuperUser) return null
  return <SshKeyPanel namespace={namespace} />
}

function SshKeyPanel({ namespace }: { namespace: string }) {
  const qc = useQueryClient()
  const { add: toast } = useToast()
  const [genName, setGenName] = useState('')
  const [generating, setGenerating] = useState(false)
  const [showPubKey, setShowPubKey] = useState<string | null>(null)

  const { data } = useQuery({
    queryKey: ['seed-keys', namespace],
    queryFn: () => seedKeyList(namespace),
    staleTime: 30_000,
  })

  const keys = data?.keys ?? []

  async function handleGenerate() {
    const name = genName.trim()
    if (!name) return
    setGenerating(true)
    try {
      const result = await seedKeyGenerate(namespace, name)
      setShowPubKey(result.publicKey)
      setGenName('')
      void qc.invalidateQueries({ queryKey: ['seed-keys', namespace] })
    } catch (e) {
      toast({ level: 'warn', text: e instanceof Error ? e.message : 'Failed to generate key' })
    } finally {
      setGenerating(false)
    }
  }

  async function handleDelete(name: string) {
    if (!confirm(`Delete SSH key "${name}"? This cannot be undone.`)) return
    try {
      await seedKeyDelete(namespace, name)
      toast({ level: 'warn', text: `Deleted key "${name}".` })
      void qc.invalidateQueries({ queryKey: ['seed-keys', namespace] })
    } catch (e) {
      toast({ level: 'warn', text: e instanceof Error ? e.message : 'Failed to delete key' })
    }
  }

  return (
    <div className={styles.section}>
      <h3 className={styles.heading}>SSH Keys</h3>

      {keys.length > 0 && (
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
            {keys.map((k: SSHKeyInfo) => (
              <tr key={k.name}>
                <td className={styles.mono}>{k.name}</td>
                <td>{k.algorithm}</td>
                <td className={styles.mono}>{k.fingerprint}</td>
                <td>{k.created_at ? new Date(k.created_at).toLocaleDateString() : ''}</td>
                <td>
                  <button className={styles.btnDanger} onClick={() => handleDelete(k.name)}>
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {keys.length === 0 && <p className={styles.muted}>No SSH keys in this namespace.</p>}

      <div className={styles.genRow}>
        <input
          className={styles.input}
          type="text"
          placeholder="Key name"
          value={genName}
          onChange={(e) => setGenName(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') void handleGenerate() }}
        />
        <button className={styles.btn} onClick={() => void handleGenerate()} disabled={generating || !genName.trim()}>
          {generating ? 'Generating…' : 'Generate Key'}
        </button>
      </div>

      {showPubKey && (
        <div className={styles.pubKeyBox}>
          <p className={styles.muted}>Public key (copy and add as a deploy key on your seed host):</p>
          <pre className={styles.pubKey}>{showPubKey}</pre>
          <button className={styles.btn} onClick={() => setShowPubKey(null)}>
            Dismiss
          </button>
        </div>
      )}
    </div>
  )
}
