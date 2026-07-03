import { useState } from 'react'
import { login, loginPasskey, isPasskeyCapable } from '../lib/authClient'
import { RedeemInvite } from './RedeemInvite'
import styles from './AuthScreen.module.css'

export function LoginScreen({
  appName,
  passkeyEnabled,
  onDone,
}: {
  appName: string
  passkeyEnabled: boolean
  onDone: () => void
}) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [totpCode, setTOTPCode] = useState('')
  const [totpRequired, setTOTPRequired] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [redeeming, setRedeeming] = useState(false)

  const canPasskey = passkeyEnabled && isPasskeyCapable()

  if (redeeming) {
    return <RedeemInvite passkeyEnabled={passkeyEnabled} onDone={onDone} onBack={() => setRedeeming(false)} />
  }

  async function submitPassword(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    setBusy(true)
    try {
      await login({ email: email.trim(), password, totp_code: totpRequired ? totpCode.trim() : undefined })
      onDone()
    } catch (e) {
      const err = e as Error & { totpRequired?: boolean }
      if (err.totpRequired) {
        setTOTPRequired(true)
        setError('Enter your two-factor code to continue.')
      } else {
        setError(errMsg(e))
      }
      setBusy(false)
    }
  }

  async function submitPasskey() {
    setError(null)
    if (!email.trim()) return setError('Enter your email to sign in with a passkey.')
    setBusy(true)
    try {
      await loginPasskey(email.trim())
      onDone()
    } catch (e) {
      setError(errMsg(e))
      setBusy(false)
    }
  }

  return (
    <div className={styles.backdrop}>
      <form className={styles.card} onSubmit={submitPassword} aria-label="Sign in">
        <h1 className={styles.title}>Sign in to {appName}</h1>
        <p className={styles.subtitle}>Authenticate to continue to your projects.</p>
        {error && <div className={styles.error}>{error}</div>}
        <div className={styles.field}>
          <label htmlFor="lg-email">Email</label>
          <input
            id="lg-email"
            type="email"
            autoComplete="username"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
          />
        </div>
        {canPasskey && (
          <>
            <button
              type="button"
              className={`${styles.button} ${styles.secondary}`}
              onClick={submitPasskey}
              disabled={busy}
            >
              Sign in with a passkey
            </button>
            <div className={styles.divider}>or use your password</div>
          </>
        )}
        <div className={styles.field}>
          <label htmlFor="lg-pw">Password</label>
          <input
            id="lg-pw"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </div>
        {totpRequired && (
          <div className={styles.field}>
            <label htmlFor="lg-code">Two-factor code</label>
            <input
              id="lg-code"
              inputMode="numeric"
              value={totpCode}
              onChange={(e) => setTOTPCode(e.target.value)}
              autoFocus
            />
          </div>
        )}
        <button className={styles.button} type="submit" disabled={busy}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
        <button
          type="button"
          className={`${styles.button} ${styles.secondary}`}
          onClick={() => setRedeeming(true)}
        >
          Have an invite code?
        </button>
      </form>
    </div>
  )
}

function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : 'Something went wrong.'
}
