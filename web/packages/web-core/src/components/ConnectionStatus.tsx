import { useEffect, useReducer } from 'react'
import { useConnectionStatus } from '../lib/useConnectionStatus'
import { wsClient } from '../lib/wsClient'
import styles from './ConnectionStatus.module.css'

function clock(ms: number | null): string {
  if (ms == null) return '—'
  return new Date(ms).toLocaleTimeString()
}
function hhmm(ms: number | null): string {
  if (ms == null) return '—'
  return new Date(ms).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
  })
}

export function ConnectionStatus() {
  const state = useConnectionStatus()
  const [, tick] = useReducer((n: number) => n + 1, 0)

  useEffect(() => {
    if (state.status !== 'reconnecting' && state.status !== 'disconnected')
      return
    const id = setInterval(tick, 1000)
    return () => clearInterval(id)
  }, [state.status])

  let label: string
  let title: string
  if (state.status === 'connected') {
    label = 'Live'
    title = `Connected since ${clock(state.connectedSince)}`
  } else if (state.status === 'connecting') {
    label = 'Connecting…'
    title = 'Connecting to server'
  } else if (state.status === 'reconnecting') {
    const secs = state.retryAt
      ? Math.max(0, Math.ceil((state.retryAt - Date.now()) / 1000))
      : 0
    label = secs > 0 ? `Reconnecting in ${secs}s…` : 'Reconnecting…'
    title = `Last update ${clock(state.lastConnectedAt)} · retry ${state.attempt}`
  } else {
    label = `Disconnected — last update ${hhmm(state.lastConnectedAt)}`
    title = `Last update ${clock(state.lastConnectedAt)} · retry ${state.attempt}`
  }

  return (
    <span className={styles.status} data-status={state.status} title={title}>
      <span className={styles.dot} data-status={state.status} />
      <span className={styles.label}>{label}</span>
      {state.status === 'disconnected' && (
        <button
          className={styles.reconnect}
          onClick={() => wsClient().reconnectNow()}
        >
          Reconnect now
        </button>
      )}
    </span>
  )
}
