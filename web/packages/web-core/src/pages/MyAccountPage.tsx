import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  getAccount,
  setAccountName,
  setAccountPassword,
  enrollTOTP,
  verifyTOTP,
  disableTOTP,
  type AccountInfo,
} from '../lib/accountOps'
import { TotpEnrollment } from '../components/TotpEnrollment'
import { useToast } from '../lib/toast'
import styles from './MyAccountPage.module.css'

// My Account (B-28) — the per-user self-service Settings item, visible to EVERY
// authenticated user (NOT super-user-only, unlike User management / OAuth). It lets the
// signed-in user view their own info, change their display name, and reset their
// password. Email is the account id and is shown read-only — there is no setter. All
// ops act on the caller's own account server-side (the session identity), so this page
// can never touch another user's account.

// MIN_PASSWORD_LEN mirrors the server policy (userstore.MinPasswordLen); the server is
// authoritative — this only gates the submit button for immediate feedback.
const MIN_PASSWORD_LEN = 8

export function MyAccountPage() {
  const qc = useQueryClient()
  const { add: toast } = useToast()
  const account = useQuery({ queryKey: ['account'], queryFn: getAccount })

  if (account.isLoading) {
    return (
      <div className={styles.page}>
        <h1 className={styles.title}>My Account</h1>
        <p className={styles.muted}>Loading…</p>
      </div>
    )
  }
  if (account.error || !account.data) {
    return (
      <div className={styles.page}>
        <h1 className={styles.title}>My Account</h1>
        <p className={styles.err}>{msg(account.error) || 'Could not load your account.'}</p>
      </div>
    )
  }

  return (
    <div className={styles.page}>
      <h1 className={styles.title}>My Account</h1>
      <AccountInfoView info={account.data} />
      <TOTPSection
        info={account.data}
        onChanged={() => void qc.invalidateQueries({ queryKey: ['account'] })}
        toast={toast}
      />
      <NameSection
        info={account.data}
        onSaved={() => {
          void qc.invalidateQueries({ queryKey: ['account'] })
          void qc.invalidateQueries({ queryKey: ['auth-status'] })
        }}
        toast={toast}
      />
      <PasswordSection toast={toast} />
    </div>
  )
}

function AccountInfoView({ info }: { info: AccountInfo }) {
  return (
    <section>
      <h2 className={styles.h2}>Account</h2>
      <dl className={styles.info}>
        <dt>Email</dt>
        <dd>
          <span className={styles.mono}>{info.email}</span>
          <span className={styles.muted}> — your account ID; it cannot be changed</span>
        </dd>
        <dt>Name</dt>
        <dd>{info.display_name || <span className={styles.muted}>(none)</span>}</dd>
        <dt>Role</dt>
        <dd>{info.is_admin ? 'Administrator' : 'Standard user'}</dd>
        <dt>Permissions</dt>
        <dd className={styles.mono}>{info.scope || '—'}</dd>
      </dl>
    </section>
  )
}

function TOTPSection({
  info,
  onChanged,
  toast,
}: {
  info: AccountInfo
  onChanged: () => void
  toast: (t: { level: 'warn'; text: string }) => void
}) {
  const [enrolling, setEnrolling] = useState(false)
  const [secret, setSecret] = useState('')
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [confirmDisable, setConfirmDisable] = useState(false)

  async function startEnroll() {
    setError(null)
    setBusy(true)
    try {
      const resp = await enrollTOTP()
      setSecret(resp.secret)
      setEnrolling(true)
    } catch (e) {
      setError(msg(e))
    } finally {
      setBusy(false)
    }
  }

  async function submitVerify(e: React.FormEvent) {
    e.preventDefault()
    if (!secret || !code.trim()) return
    setError(null)
    setBusy(true)
    try {
      await verifyTOTP(secret, code.trim())
      toast({ level: 'warn', text: 'Two-factor authentication has been enabled.' })
      setEnrolling(false)
      setSecret('')
      setCode('')
      onChanged()
    } catch (e) {
      setError(msg(e))
    } finally {
      setBusy(false)
    }
  }

  async function doDisable() {
    setBusy(true)
    try {
      await disableTOTP()
      toast({ level: 'warn', text: 'Two-factor authentication has been disabled.' })
      setConfirmDisable(false)
      onChanged()
    } catch (e) {
      setError(msg(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section>
      <h2 className={styles.h2}>Two-factor authentication (TOTP)</h2>
      <div className={styles.totpRow}>
        <span className={styles.totpStatus}>{info.has_totp ? 'Enrolled' : 'Not enrolled'}</span>
        {info.has_totp ? (
          <button
            className={`${styles.btn} ${styles.danger}`}
            onClick={() => setConfirmDisable(true)}
            disabled={busy}
          >
            Disable
          </button>
        ) : (
          <button
            className={`${styles.btn} ${styles.primary}`}
            onClick={startEnroll}
            disabled={busy || enrolling}
          >
            Enable
          </button>
        )}
      </div>
      {error && <p className={styles.err}>{error}</p>}
      {enrolling && secret && (
        <form className={styles.totpEnroll} onSubmit={submitVerify}>
          <TotpEnrollment
            secret={secret}
            code={code}
            onCodeChange={setCode}
            idPrefix="acct-totp"
          />
          <button
            type="submit"
            className={`${styles.btn} ${styles.primary}`}
            disabled={busy || code.trim() === ''}
            style={{ marginTop: '0.5rem' }}
          >
            Verify
          </button>
          <button
            type="button"
            className={styles.btn}
            onClick={() => { setEnrolling(false); setSecret(''); setCode(''); setError(null) }}
            style={{ marginTop: '0.5rem', marginLeft: '0.5rem' }}
          >
            Cancel
          </button>
        </form>
      )}
      {confirmDisable && (
        <div className={styles.confirmOverlay}>
          <div className={styles.confirmDialog} role="dialog" aria-label="Disable Two-Factor Authentication">
            <h3>Disable Two-Factor Authentication</h3>
            <p>
              This will remove TOTP from your account. You will no longer
              need a verification code to sign in.
            </p>
            <div className={styles.confirmActions}>
              <button
                className={styles.btn}
                onClick={() => setConfirmDisable(false)}
                disabled={busy}
              >
                Cancel
              </button>
              <button
                className={`${styles.btn} ${styles.danger}`}
                onClick={doDisable}
                disabled={busy}
              >
                Disable 2FA
              </button>
            </div>
          </div>
        </div>
      )}
    </section>
  )
}

function NameSection({
  info,
  onSaved,
  toast,
}: {
  info: AccountInfo
  onSaved: () => void
  toast: (t: { level: 'warn'; text: string }) => void
}) {
  const [name, setName] = useState(info.display_name)
  const [saving, setSaving] = useState(false)
  // Keep the field in sync if the underlying account refetches to a new name.
  useEffect(() => setName(info.display_name), [info.display_name])

  const trimmed = name.trim()
  const canSave = trimmed !== '' && trimmed !== info.display_name && !saving

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!canSave) return
    setSaving(true)
    try {
      await setAccountName(trimmed)
      toast({ level: 'warn', text: 'Your name has been updated.' })
      onSaved()
    } catch (err) {
      toast({ level: 'warn', text: msg(err) })
    } finally {
      setSaving(false)
    }
  }

  return (
    <section>
      <h2 className={styles.h2}>Change name</h2>
      <form className={styles.form} onSubmit={onSubmit}>
        <label className={styles.label} htmlFor="acct-name">
          Display name
        </label>
        <input
          id="acct-name"
          className={`${styles.input} ${trimmed === '' ? styles.invalid : ''}`}
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          aria-invalid={trimmed === '' || undefined}
          autoComplete="name"
        />
        <button
          type="submit"
          className={`${styles.btn} ${styles.primary}`}
          disabled={!canSave}
          title={trimmed === '' ? 'name must not be empty' : undefined}
        >
          Save name
        </button>
      </form>
    </section>
  )
}

function PasswordSection({
  toast,
}: {
  toast: (t: { level: 'warn'; text: string }) => void
}) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [saving, setSaving] = useState(false)

  const tooShort = next.length > 0 && next.length < MIN_PASSWORD_LEN
  const mismatch = confirm.length > 0 && next !== confirm
  const canSave =
    current !== '' &&
    next.length >= MIN_PASSWORD_LEN &&
    next === confirm &&
    !saving

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!canSave) return
    setSaving(true)
    try {
      await setAccountPassword(current, next)
      toast({ level: 'warn', text: 'Your password has been changed.' })
      setCurrent('')
      setNext('')
      setConfirm('')
    } catch (err) {
      toast({ level: 'warn', text: msg(err) })
    } finally {
      setSaving(false)
    }
  }

  return (
    <section>
      <h2 className={styles.h2}>Reset password</h2>
      <form className={styles.form} onSubmit={onSubmit}>
        <label className={styles.label} htmlFor="acct-current-pw">
          Current password
        </label>
        <input
          id="acct-current-pw"
          className={styles.input}
          type="password"
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
          autoComplete="current-password"
        />
        <label className={styles.label} htmlFor="acct-new-pw">
          New password
        </label>
        <input
          id="acct-new-pw"
          className={`${styles.input} ${tooShort ? styles.invalid : ''}`}
          type="password"
          value={next}
          onChange={(e) => setNext(e.target.value)}
          aria-invalid={tooShort || undefined}
          autoComplete="new-password"
        />
        {tooShort && (
          <p className={styles.err}>Password must be at least {MIN_PASSWORD_LEN} characters.</p>
        )}
        <label className={styles.label} htmlFor="acct-confirm-pw">
          Confirm new password
        </label>
        <input
          id="acct-confirm-pw"
          className={`${styles.input} ${mismatch ? styles.invalid : ''}`}
          type="password"
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
          aria-invalid={mismatch || undefined}
          autoComplete="new-password"
        />
        {mismatch && <p className={styles.err}>The new passwords do not match.</p>}
        <button
          type="submit"
          className={`${styles.btn} ${styles.primary}`}
          disabled={!canSave}
          title={!canSave ? 'enter your current password and a matching new password' : undefined}
        >
          Change password
        </button>
      </form>
    </section>
  )
}

function msg(e: unknown): string {
  return e instanceof Error ? e.message : 'Something went wrong.'
}
