import { useState } from 'react'
import {
  registerFirstAdmin,
  registerPasskey,
  isPasskeyCapable,
  newTOTP,
  type NewTOTP,
} from '../lib/authClient'
import { TotpEnrollment } from './TotpEnrollment'
import styles from './AuthScreen.module.css'

export function FirstRunWizard({
  appName,
  passkeyEnabled,
  onDone,
}: {
  appName: string
  passkeyEnabled: boolean
  onDone: () => void
}) {
  const [email, setEmail] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [useTOTP, setUseTOTP] = useState(false)
  const [totp, setTOTP] = useState<NewTOTP | null>(null)
  const [totpCode, setTOTPCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const canPasskey = passkeyEnabled && isPasskeyCapable()

  async function toggleTOTP(on: boolean) {
    setUseTOTP(on)
    setError(null)
    if (on && !totp) {
      try {
        setTOTP(await newTOTP(email || 'admin'))
      } catch (e) {
        setError(errMsg(e))
        setUseTOTP(false)
      }
    }
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    if (password.length < 8) return setError('Password must be at least 8 characters.')
    if (password !== confirm) return setError('Passwords do not match.')
    if (useTOTP && (!totp || totpCode.trim() === '')) {
      return setError('Enter the 6-digit code from your authenticator to confirm TOTP.')
    }
    setBusy(true)
    try {
      await registerFirstAdmin({
        email: email.trim(),
        display_name: displayName.trim() || email.trim(),
        password,
        totp_secret: useTOTP && totp ? totp.secret : undefined,
        totp_code: useTOTP ? totpCode.trim() : undefined,
      })
      if (canPasskey && window.confirm('Set up a passkey for faster, passwordless sign-in?')) {
        try {
          await registerPasskey()
        } catch {
          /* non-fatal: password+TOTP remains usable */
        }
      }
      onDone()
    } catch (e) {
      setError(errMsg(e))
      setBusy(false)
    }
  }

  return (
    <div className={styles.backdrop}>
      <form className={styles.card} onSubmit={submit} aria-label="First-run setup">
        <h1 className={styles.title}>Welcome to {appName}</h1>
        <p className={styles.subtitle}>
          No users exist yet. Create the first administrator account — this account
          becomes the owner of this {appName} instance.
        </p>
        {error && <div className={styles.error}>{error}</div>}
        <div className={styles.field}>
          <label htmlFor="fr-email">Email (your commit author identity)</label>
          <input
            id="fr-email"
            type="email"
            autoComplete="username"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
          />
        </div>
        <div className={styles.field}>
          <label htmlFor="fr-name">Display name</label>
          <input
            id="fr-name"
            type="text"
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
          />
        </div>
        <div className={styles.field}>
          <label htmlFor="fr-pw">Password</label>
          <input
            id="fr-pw"
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </div>
        <div className={styles.field}>
          <label htmlFor="fr-pw2">Confirm password</label>
          <input
            id="fr-pw2"
            type="password"
            autoComplete="new-password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            required
          />
        </div>
        <div className={styles.toggleRow}>
          <input
            id="fr-totp"
            type="checkbox"
            checked={useTOTP}
            onChange={(e) => toggleTOTP(e.target.checked)}
          />
          <label htmlFor="fr-totp">Enable two-factor authentication (TOTP)</label>
        </div>
        {useTOTP && totp && (
          <TotpEnrollment
            secret={totp.secret}
            code={totpCode}
            onCodeChange={setTOTPCode}
            idPrefix="fr"
          />
        )}
        <button className={styles.button} type="submit" disabled={busy}>
          {busy ? 'Creating…' : 'Create administrator'}
        </button>
      </form>
    </div>
  )
}

function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : 'Something went wrong.'
}
