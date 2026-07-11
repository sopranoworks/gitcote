import { useQuery } from '@tanstack/react-query'
import { seedStatus } from '../ops/seedOps'
import styles from './SeedStatusBadge.module.css'

function relativeTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  if (diff < 60_000) return 'just now'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`
  return `${Math.floor(diff / 86_400_000)}d ago`
}

export function SeedStatusBadge({
  namespace,
  project,
}: {
  namespace: string
  project: string
}) {
  const { data } = useQuery({
    queryKey: ['seed-status', namespace, project],
    queryFn: () => seedStatus(namespace, project),
    staleTime: 30_000,
  })

  if (!data?.syncStatus && !data?.pushMode) return null
  if (data.pushMode === '' && !data.syncStatus) return null

  const state = data.syncStatus?.state ?? (data.pushMode === 'disabled' || data.pushMode === '' ? 'disabled' : 'pending')

  switch (state) {
    case 'active':
      return (
        <span className={styles.badge} data-state="active" title={data.syncStatus?.last_result ?? ''}>
          <span className={styles.dot} data-state="active" />
          Synced {data.syncStatus?.last_push_at ? relativeTime(data.syncStatus.last_push_at) : ''}
        </span>
      )
    case 'pending':
      return (
        <span className={styles.badge} data-state="pending">
          <span className={styles.dot} data-state="pending" />
          Awaiting resume
        </span>
      )
    case 'disabled':
      return (
        <span className={styles.badge} data-state="disabled">
          <span className={styles.dot} data-state="disabled" />
          Manual
        </span>
      )
    case 'error':
      return (
        <span className={styles.badge} data-state="error" title={data.syncStatus?.last_result ?? ''}>
          <span className={styles.dot} data-state="error" />
          Error
        </span>
      )
    case 'conflict': {
      const dir = data.syncStatus?.direction === 'push' ? 'push' : 'pull'
      return (
        <span className={styles.badge} data-state="conflict" data-direction={dir} title={data.syncStatus?.last_result ?? ''}>
          <span className={styles.dot} data-state="conflict" />
          {dir === 'push' ? 'Push conflict' : 'Pull conflict'}
        </span>
      )
    }
    case 'interrupted': {
      const dir = data.syncStatus?.direction === 'push' ? 'push' : 'pull'
      return (
        <span className={styles.badge} data-state="interrupted" data-direction={dir} title={data.syncStatus?.last_result ?? ''}>
          <span className={styles.dot} data-state="interrupted" />
          {dir === 'push' ? 'Push interrupted' : 'Pull interrupted'}
        </span>
      )
    }
    case 'retrying':
      return (
        <span className={styles.badge} data-state="retrying">
          <span className={styles.dot} data-state="retrying" />
          Retrying…
        </span>
      )
    default:
      return null
  }
}
