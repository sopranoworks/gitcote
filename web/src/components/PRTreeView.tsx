import { useMemo, useState } from 'react'
import { useNavigate, useRouterState } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { prList, type PullRequest } from '../ops/prOps'
import { PRStatusBadge } from './PRStatusBadge'
import { sidebarStyles } from '@shoka/web-core'
import styles from './PRTreeView.module.css'

type SortDir = 'desc' | 'asc'

const FILTER_OPTIONS = [
  { value: '', label: 'All' },
  { value: 'open', label: 'Open' },
  { value: 'approved', label: 'Approved' },
  { value: 'rejected', label: 'Rejected' },
  { value: 'merged', label: 'Merged' },
  { value: 'closed', label: 'Closed' },
  { value: 'interrupted', label: 'Interrupted' },
] as const

function useActiveProjectRef(): { ns: string; proj: string } | null {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const m = pathname.match(/^\/p\/([^/]+)\/([^/]+)/)
  if (!m) return null
  return { ns: decodeURIComponent(m[1]), proj: decodeURIComponent(m[2]) }
}

function useSelectedPR(): number | null {
  const search = useRouterState({ select: (s) => s.location.search as { pr?: string } })
  const n = search.pr ? Number(search.pr) : NaN
  return Number.isFinite(n) ? n : null
}

export function PRTreeView() {
  const ref = useActiveProjectRef()
  const selectedPR = useSelectedPR()
  const navigate = useNavigate()
  const [filter, setFilter] = useState('')
  const [sortDir, setSortDir] = useState<SortDir>('desc')

  const { data } = useQuery({
    queryKey: ['pr-list', ref?.ns, ref?.proj],
    queryFn: () => prList(ref!.ns, ref!.proj),
    enabled: !!ref,
    staleTime: 10_000,
  })

  const prs = data?.pull_requests ?? []

  const filtered = useMemo(() => {
    let list = prs
    if (filter) list = list.filter((p) => p.state === filter)
    return list.slice().sort((a, b) => {
      const ta = new Date(a.created_at).getTime()
      const tb = new Date(b.created_at).getTime()
      return sortDir === 'desc' ? tb - ta : ta - tb
    })
  }, [prs, filter, sortDir])

  if (!ref) {
    return (
      <div className={sidebarStyles.pane}>
        <div className={sidebarStyles.sectionHeader}>Pull Requests</div>
        <div className={sidebarStyles.empty}>Open a project to view pull requests.</div>
      </div>
    )
  }

  function handleSelect(pr: PullRequest) {
    void navigate({
      to: '/p/$namespace/$project/prs',
      params: { namespace: ref!.ns, project: ref!.proj },
      search: { pr: String(pr.number) },
    })
  }

  return (
    <div className={sidebarStyles.pane}>
      <div className={sidebarStyles.sectionHeader}>
        <span className={sidebarStyles.projTitle}>
          <span className={sidebarStyles.projNs}>{ref.ns}/</span>
          {ref.proj}
        </span>
      </div>
      <div className={styles.filterBar}>
        <select
          className={styles.filterSelect}
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          aria-label="Filter by status"
        >
          {FILTER_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>{o.label}</option>
          ))}
        </select>
        <button
          className={styles.sortBtn}
          onClick={() => setSortDir((d) => (d === 'desc' ? 'asc' : 'desc'))}
          title={sortDir === 'desc' ? 'Newest first' : 'Oldest first'}
          aria-label="Toggle sort order"
        >
          {sortDir === 'desc' ? '↓' : '↑'}
        </button>
      </div>
      <div className={styles.listWrap}>
        {filtered.length === 0 ? (
          <div className={sidebarStyles.empty}>
            {filter ? 'No matching pull requests.' : 'No pull requests.'}
          </div>
        ) : (
          <div className={styles.list}>
            {filtered.map((p) => (
              <div
                key={p.number}
                className={styles.item}
                data-selected={p.number === selectedPR || undefined}
                onClick={() => handleSelect(p)}
              >
                <span className={styles.prNumber}>#{p.number}</span>
                <span className={styles.prTitle}>{p.title}</span>
                <PRStatusBadge state={p.state} />
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
