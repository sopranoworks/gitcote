import styles from './AuthScreen.module.css'

export function TotpEnrollment({
  secret,
  code,
  onCodeChange,
  idPrefix,
}: {
  secret: string
  code: string
  onCodeChange: (code: string) => void
  idPrefix: string
}) {
  return (
    <div className={styles.totpBox}>
      <div>Add this secret to your authenticator app, then enter a code:</div>
      <div className={styles.totpSecret} data-testid="totp-secret">{secret}</div>
      <div className={styles.field}>
        <label htmlFor={`${idPrefix}-code`}>6-digit code</label>
        <input
          id={`${idPrefix}-code`}
          inputMode="numeric"
          value={code}
          onChange={(e) => onCodeChange(e.target.value)}
        />
      </div>
    </div>
  )
}
