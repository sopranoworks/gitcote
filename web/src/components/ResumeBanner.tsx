import { useState } from 'react'
import { useIsSuperUser } from '@shoka/web-core'
import { useToast } from '@shoka/web-core'
import { useQueryClient } from '@tanstack/react-query'
import { seedResume } from '../ops/seedOps'
import styles from './ResumeBanner.module.css'

export function ResumeBanner() {
  const isSuperUser = useIsSuperUser()
  const { add: toast } = useToast()
  const qc = useQueryClient()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [resuming, setResuming] = useState(false)
  const [dismissed, setDismissed] = useState(false)

  if (!isSuperUser || dismissed) return null

  async function handleResume() {
    if (!email.trim() || !password) return
    setResuming(true)
    try {
      await seedResume(email.trim(), password)
      toast({ level: 'warn', text: 'Vault unlocked. Seed push resumed.' })
      setDismissed(true)
      void qc.invalidateQueries({ queryKey: ['seed-status'] })
    } catch (e) {
      toast({ level: 'warn', text: e instanceof Error ? e.message : 'Resume failed.' })
    } finally {
      setResuming(false)
    }
  }

  return (
    <div className={styles.banner}>
      <span className={styles.text}>Seed push is paused after server restart.</span>
      <input
        className={styles.input}
        type="email"
        placeholder="Email"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
      />
      <input
        className={styles.input}
        type="password"
        placeholder="Password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        onKeyDown={(e) => { if (e.key === 'Enter') void handleResume() }}
      />
      <button className={styles.btn} onClick={() => void handleResume()} disabled={resuming || !email.trim() || !password}>
        {resuming ? 'Resuming…' : 'Resume'}
      </button>
    </div>
  )
}
