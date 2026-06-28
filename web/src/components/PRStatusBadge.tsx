import styles from './PRStatusBadge.module.css'

const LABELS: Record<string, string> = {
  open: 'Open',
  approved: 'Approved',
  rejected: 'Changes Requested',
  merged: 'Merged',
  closed: 'Closed',
  merge_conflict: 'Conflicts',
  interrupted: 'Interrupted',
}

export function PRStatusBadge({ state }: { state: string }) {
  return (
    <span className={styles.badge} data-state={state}>
      {LABELS[state] ?? state}
    </span>
  )
}
