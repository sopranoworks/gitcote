import { useState } from 'react'
import {
  inviteInfo,
  redeemInvite,
  registerPasskey,
  isPasskeyCapable,
  newTOTP,
  type NewTOTP,
  type InviteInfo,
} from '../lib/authClient'
import { TotpEnrollment } from './TotpEnrollment'
import styles from './AuthScreen.module.css'

export function RedeemInvite({
  passkeyEnabled,
  onDone,
  onBack,
}: {
  passkeyEnabled: boolean
  onDone: () => void
  onBack: () => void
}) {
  const [code, setCode] = useState('')
  const [info, setInfo] = useState<InviteInfo | null>(null)
  const [displayName, setDisplayName] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [useTOTP, setUseTOTP] = useState(false)
  const [totp, setTOTP] = useState<NewTOTP | null>(null)
  const [totpCode, setTOTPCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const canPasskey = passkeyEnabled && isPasskeyCapable()

  async function lookup(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    setBusy(true)
    try {
      setInfo(await inviteInfo(code.trim()))
    } catch (err) {
      setError(errMsg(err))
    }
    setBusy(false)
  }

  async function toggleTOTP(on: boolean) {
    setUseTOTP(on)
    if (on && !totp && info) {
      try {
        setTOTP(await newTOTP(info.email))
      } catch (err) {
        setError(errMsg(err))
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
      await redeemInvite({
        code: code.trim(),
        display_name: displayName.trim(),
        password,
        totp_secret: useTOTP && totp ? totp.secret : undefined,
        totp_code: useTOTP ? totpCode.trim() : undefined,
      })
      if (canPasskey && window.confirm('Set up a passkey for faster sign-in?')) {
        try {
          await registerPasskey()
        } catch {
          /* non-fatal */
        }
      }
      onDone()
    } catch (err) {
      setError(errMsg(err))
      setBusy(false)
    }
  }

  return (
    <div className={styles.backdrop}>
      {!info ? (
        <form className={styles.card} onSubmit={lookup} aria-label="Redeem invite">
          <h1 className={styles.title}>Redeem an invite</h1>
          <p className={styles.subtitle}>Enter the invite code you were given.</p>
          {error && <div className={styles.error}>{error}</div>}
          <div className={styles.field}>
            <label htmlFor="rd-code">Invite code</label>
            <input id="rd-code" value={code} onChange={(e) => setCode(e.target.value)} required />
          </div>
          <button className={styles.button} type="submit" disabled={busy}>
            {busy ? 'Checking…' : 'Continue'}
          </button>
          <button type="button" className={`${styles.button} ${styles.secondary}`} onClick={onBack}>
            Back to sign in
          </button>
        </form>
      ) : (
        <form className={styles.card} onSubmit={submit} aria-label="Set up invited account">
          <h1 className={styles.title}>Set up your account</h1>
          <p className={styles.subtitle}>
            For <strong>{info.email}</strong>. Choose your credentials.
          </p>
          {error && <div className={styles.error}>{error}</div>}
          <div className={styles.field}>
            <label htmlFor="rd-name">Display name</label>
            <input id="rd-name" value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
          </div>
          <div className={styles.field}>
            <label htmlFor="rd-pw">Password</label>
            <input
              id="rd-pw"
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
            />
          </div>
          <div className={styles.field}>
            <label htmlFor="rd-pw2">Confirm password</label>
            <input
              id="rd-pw2"
              type="password"
              autoComplete="new-password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              required
            />
          </div>
          <div className={styles.toggleRow}>
            <input id="rd-totp" type="checkbox" checked={useTOTP} onChange={(e) => toggleTOTP(e.target.checked)} />
            <label htmlFor="rd-totp">Enable two-factor authentication (TOTP)</label>
          </div>
          {useTOTP && totp && (
            <TotpEnrollment
              secret={totp.secret}
              code={totpCode}
              onCodeChange={setTOTPCode}
              idPrefix="rd"
            />
          )}
          <button className={styles.button} type="submit" disabled={busy}>
            {busy ? 'Creating…' : 'Create account'}
          </button>
        </form>
      )}
    </div>
  )
}

function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : 'Something went wrong.'
}
