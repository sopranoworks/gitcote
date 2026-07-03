import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { wsClient } from '../lib/wsClient'
import { useToast } from '../lib/toast'
import styles from './RecoverButton.module.css'

async function recoverProject(ns: string, proj: string) {
  return wsClient().request<{ message: string }>('RECOVER_PROJECT', {
    namespace: ns,
    projectName: proj,
  })
}

export function RecoverButton({
  namespace,
  project,
}: {
  namespace: string
  project: string
}) {
  const queryClient = useQueryClient()
  const { add: addToast } = useToast()
  const [busy, setBusy] = useState(false)

  async function onRecover(e: React.MouseEvent) {
    e.preventDefault()
    e.stopPropagation()
    if (busy) return
    setBusy(true)
    try {
      const ack = await recoverProject(namespace, project)
      addToast({ level: 'warn', text: ack.message })
    } catch (err) {
      addToast({
        level: 'warn',
        text: `Recover failed: ${
          err instanceof Error ? err.message : 'unknown error'
        }`,
      })
    } finally {
      queryClient.invalidateQueries({ queryKey: ['projects'] })
      setBusy(false)
    }
  }

  return (
    <button
      type="button"
      className={styles.recoverBtn}
      onClick={onRecover}
      disabled={busy}
      aria-label={`Recover ${namespace}/${project}`}
      title="Re-sync this project to its on-disk git HEAD and clear a false corrupted state"
    >
      {busy ? 'Recovering…' : 'Recover'}
    </button>
  )
}
