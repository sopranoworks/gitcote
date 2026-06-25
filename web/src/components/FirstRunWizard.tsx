import { useState } from 'react'
import { registerFirstAdmin } from '@shoka/web-core'
import styles from './LoginScreen.module.css'

export function FirstRunWizard({ onDone }: { onDone: () => void }) {
  const [email, setEmail] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function handleCreate() {
    setBusy(true)
    setError('')
    try {
      await registerFirstAdmin({ email, display_name: displayName, password })
      onDone()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Registration failed.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className={styles.container}>
      <div className={styles.card}>
        <h1 className={styles.title}>GitYard Setup</h1>
        <p style={{ color: 'var(--c-text-dim)', fontSize: '0.85rem', margin: '0 0 1rem', textAlign: 'center' }}>
          Create the administrator account.
        </p>
        <div className={styles.field}>
          <input
            className={styles.input}
            type="email"
            placeholder="Email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </div>
        <div className={styles.field}>
          <input
            className={styles.input}
            type="text"
            placeholder="Display name"
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
          />
        </div>
        <div className={styles.field}>
          <input
            className={styles.input}
            type="password"
            placeholder="Password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') void handleCreate() }}
          />
        </div>
        {error && <p className={styles.error}>{error}</p>}
        <button
          className={styles.btn}
          onClick={() => void handleCreate()}
          disabled={busy || !email || !password}
        >
          {busy ? 'Creating…' : 'Create Admin'}
        </button>
      </div>
    </div>
  )
}
