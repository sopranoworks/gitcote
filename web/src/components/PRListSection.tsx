import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { prList } from '../ops/prOps'
import { PRStatusBadge } from './PRStatusBadge'
import { PRDetailView } from './PRDetailView'
import styles from './PRListSection.module.css'

export function PRListSection({ namespace, project }: { namespace: string; project: string }) {
  const [selectedPR, setSelectedPR] = useState<number | null>(null)

  const { data } = useQuery({
    queryKey: ['pr-list', namespace, project],
    queryFn: () => prList(namespace, project),
    staleTime: 10_000,
  })

  const prs = data?.pull_requests ?? []
  const active = prs.filter((p) => p.state !== 'merged' && p.state !== 'closed')

  if (selectedPR !== null) {
    return (
      <PRDetailView
        namespace={namespace}
        project={project}
        number={selectedPR}
        onBack={() => setSelectedPR(null)}
      />
    )
  }

  return (
    <div className={styles.section}>
      <div className={styles.header}>
        <span className={styles.title}>Pull Requests</span>
        <span className={styles.count}>{active.length} active</span>
      </div>
      {prs.length === 0 ? (
        <div className={styles.empty}>No pull requests</div>
      ) : (
        <div className={styles.list}>
          {prs.map((p) => (
            <div
              key={p.number}
              className={styles.item}
              onClick={() => setSelectedPR(p.number)}
            >
              <span className={styles.prNumber}>#{p.number}</span>
              <span className={styles.prTitle}>{p.title}</span>
              <span className={styles.branches}>{p.source_branch} → {p.target_branch}</span>
              <PRStatusBadge state={p.state} />
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
