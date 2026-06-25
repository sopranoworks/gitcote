import { useState } from 'react'
import { login } from '@shoka/web-core'
import styles from './LoginScreen.module.css'

export function LoginScreen({ onDone }: { onDone: () => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [totp, setTotp] = useState('')
  const [error, setError] = useState('')
  const [needTotp, setNeedTotp] = useState(false)
  const [busy, setBusy] = useState(false)

  async function handleLogin() {
    setBusy(true)
    setError('')
    try {
      await login({ email, password, totp_code: totp || undefined })
      onDone()
    } catch (e) {
      const err = e as Error & { totpRequired?: boolean }
      if (err.totpRequired) {
        setNeedTotp(true)
      } else {
        setError(err.message || 'Login failed.')
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className={styles.container}>
      <div className={styles.card}>
        <h1 className={styles.title}>GitYard</h1>
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
            type="password"
            placeholder="Password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter' && !needTotp) void handleLogin() }}
          />
        </div>
        {needTotp && (
          <div className={styles.field}>
            <input
              className={styles.input}
              type="text"
              placeholder="TOTP code"
              value={totp}
              onChange={(e) => setTotp(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') void handleLogin() }}
              autoFocus
            />
          </div>
        )}
        {error && <p className={styles.error}>{error}</p>}
        <button className={styles.btn} onClick={() => void handleLogin()} disabled={busy}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </div>
    </div>
  )
}
