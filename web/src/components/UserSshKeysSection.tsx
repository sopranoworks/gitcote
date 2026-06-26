import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useToast } from '@shoka/web-core'
import { userSshKeyList, userSshKeyAdd, userSshKeyDelete } from '../ops/userSshKeyOps'
import styles from './SshKeysPage.module.css'

export function UserSshKeysSection() {
  const qc = useQueryClient()
  const { add: toast } = useToast()
  const [pubKey, setPubKey] = useState('')
  const [title, setTitle] = useState('')
  const [adding, setAdding] = useState(false)

  const { data } = useQuery({
    queryKey: ['user-ssh-keys'],
    queryFn: userSshKeyList,
    staleTime: 30_000,
  })

  const keys = data?.keys ?? []

  async function handleAdd() {
    if (!pubKey.trim()) return
    setAdding(true)
    try {
      await userSshKeyAdd(pubKey.trim(), title.trim())
      toast({ level: 'warn', text: 'SSH key added.' })
      setPubKey('')
      setTitle('')
      void qc.invalidateQueries({ queryKey: ['user-ssh-keys'] })
    } catch (e) {
      toast({ level: 'warn', text: e instanceof Error ? e.message : 'Failed to add key.' })
    } finally {
      setAdding(false)
    }
  }

  async function handleDelete(fingerprint: string) {
    if (!confirm('Delete this SSH key?')) return
    try {
      await userSshKeyDelete(fingerprint)
      toast({ level: 'warn', text: 'SSH key deleted.' })
      void qc.invalidateQueries({ queryKey: ['user-ssh-keys'] })
    } catch (e) {
      toast({ level: 'warn', text: e instanceof Error ? e.message : 'Failed to delete key.' })
    }
  }

  return (
    <div className={styles.page}>
      <h1 className={styles.title}>My SSH Keys</h1>
      <p className={styles.muted}>
        Register your SSH public keys for git access via SSH.
        These are personal keys — different from namespace deploy keys used for seed push.
      </p>

      {keys.length > 0 ? (
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Title</th>
              <th>Type</th>
              <th>Fingerprint</th>
              <th>Added</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {keys.map((k) => (
              <tr key={k.fingerprint}>
                <td>{k.title || '(untitled)'}</td>
                <td>{k.key_type}</td>
                <td className={styles.mono}>{k.fingerprint}</td>
                <td>{k.created_at ? new Date(k.created_at).toLocaleDateString() : ''}</td>
                <td>
                  <button className={styles.btnDanger} onClick={() => handleDelete(k.fingerprint)}>
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <p className={styles.muted}>No SSH keys registered.</p>
      )}

      <section className={styles.genSection}>
        <h2 className={styles.nsName}>Add SSH key</h2>
        <div className={styles.genRow}>
          <input
            className={styles.input}
            type="text"
            placeholder="Title (optional)"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
          />
        </div>
        <textarea
          className={styles.input}
          style={{ width: '100%', minHeight: '4rem', marginTop: '0.5rem', fontFamily: 'var(--font-mono, monospace)', fontSize: '0.8rem' }}
          placeholder="Paste your public key (ssh-ed25519 AAAA...)"
          value={pubKey}
          onChange={(e) => setPubKey(e.target.value)}
        />
        <div className={styles.genRow} style={{ marginTop: '0.5rem' }}>
          <button className={styles.btn} onClick={() => void handleAdd()} disabled={adding || !pubKey.trim()}>
            {adding ? 'Adding…' : 'Add Key'}
          </button>
        </div>
      </section>
    </div>
  )
}
