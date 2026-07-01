import { useState } from 'react'
import { useRouterState } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Markdown } from '@shoka/web-core'
import { prGet, prMerge, prRetryAgent, prDismissInterrupt, prOperatorReject, prClose, prReview, type ConflictInfo } from '../ops/prOps'
import { prEventSettingsGet } from '../ops/eventSettingsOps'
import { serverSshInfo } from '../ops/userSshKeyOps'
import { PRStatusBadge } from './PRStatusBadge'
import { AgentSelector } from './AgentSelector'
import styles from './PRViewPane.module.css'

function relativeTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  if (diff < 60_000) return 'just now'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`
  return `${Math.floor(diff / 86_400_000)}d ago`
}

function FileRefs({ label, files }: { label: string; files?: string[] }) {
  if (!files || files.length === 0) return null
  return (
    <div className={styles.fileRefs}>
      <div className={styles.fileRefsLabel}>{label}</div>
      {files.map((f) => (
        <div key={f} className={styles.fileRefItem}>{f}</div>
      ))}
    </div>
  )
}

function ConflictDisplay({
  conflicts,
  namespace,
  project,
  sourceBranch,
  targetBranch,
}: {
  conflicts: ConflictInfo[]
  namespace: string
  project: string
  sourceBranch: string
  targetBranch: string
}) {
  const { data: sshInfo } = useQuery({
    queryKey: ['server-ssh-info'],
    queryFn: serverSshInfo,
    staleTime: 60_000,
  })

  const host = window.location.hostname
  const base = window.location.origin
  const httpUrl = `${base}/${namespace}/${project}.git`
  let sshUrl = ''
  if (sshInfo?.enabled && sshInfo.port) {
    sshUrl = sshInfo.port === 22
      ? `git@${host}:${namespace}/${project}.git`
      : `ssh://git@${host}:${sshInfo.port}/${namespace}/${project}.git`
  }
  const cloneUrl = sshUrl || httpUrl

  return (
    <div className={styles.conflictSection}>
      <div className={styles.sectionTitle} style={{ color: '#f85149' }}>Conflicts</div>
      {conflicts.map((c) => (
        <div key={c.path} className={styles.conflictFile}>
          <code>{c.path}</code>
          <span className={styles.conflictType}>{c.type}</span>
        </div>
      ))}
      <div className={styles.resolveHint}>Resolve manually:</div>
      <pre className={styles.codeBlock}>
{`git clone ${cloneUrl}
cd ${project}
git checkout ${sourceBranch}
git merge origin/${targetBranch}
# resolve conflicts
git add . && git commit
git push`}
      </pre>
    </div>
  )
}

function InterruptedPanel({
  namespace,
  project,
  number,
  info,
  onRefresh,
}: {
  namespace: string
  project: string
  number: number
  info: { reason: string; detail: string; agent_name: string; agent_role: string; at: string }
  onRefresh: () => void
}) {
  const [switchAgent, setSwitchAgent] = useState(false)
  const [selectedAgent, setSelectedAgent] = useState('')
  const [busy, setBusy] = useState(false)

  async function handleRetry(agentName?: string) {
    setBusy(true)
    try {
      await prRetryAgent(namespace, project, number, agentName)
      onRefresh()
    } finally {
      setBusy(false)
    }
  }

  async function handleDismiss() {
    setBusy(true)
    try {
      await prDismissInterrupt(namespace, project, number)
      onRefresh()
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className={styles.interruptPanel}>
      <div className={styles.interruptHeader}>
        <PRStatusBadge state="interrupted" />
        <span className={styles.interruptAgent}>
          {info.agent_name} ({info.agent_role})
        </span>
      </div>
      <div className={styles.interruptDetail}>{info.detail}</div>
      <div className={styles.interruptMeta}>
        Reason: {info.reason} · {relativeTime(info.at)}
      </div>
      <div className={styles.actions}>
        <button className={styles.actionBtn} disabled={busy} onClick={() => void handleRetry()}>
          Retry
        </button>
        <button className={styles.actionBtn} disabled={busy} onClick={handleDismiss}>
          Dismiss
        </button>
        <button className={styles.actionBtn} disabled={busy} onClick={() => setSwitchAgent(!switchAgent)}>
          Switch Agent
        </button>
        {switchAgent && (
          <>
            <AgentSelector role={info.agent_role} value={selectedAgent} onChange={setSelectedAgent} />
            <button className={styles.actionBtn} disabled={busy || !selectedAgent} onClick={() => void handleRetry(selectedAgent)}>
              Retry with selected
            </button>
          </>
        )}
      </div>
    </div>
  )
}

function PRDetail({ namespace, project, number }: { namespace: string; project: string; number: number }) {
  const queryClient = useQueryClient()
  const [merging, setMerging] = useState(false)
  const [rejecting, setRejecting] = useState(false)
  const [rejectReason, setRejectReason] = useState('')
  const [rejectModalOpen, setRejectModalOpen] = useState(false)
  const [closing, setClosing] = useState(false)
  const [reviewing, setReviewing] = useState(false)

  const { data, refetch } = useQuery({
    queryKey: ['pr-detail', namespace, project, number],
    queryFn: () => prGet(namespace, project, number),
    staleTime: 5_000,
  })

  const { data: settingsData } = useQuery({
    queryKey: ['pr-event-settings', namespace, project],
    queryFn: () => prEventSettingsGet(namespace, project),
    staleTime: 30_000,
  })

  if (!data) return null

  const pr = data.pull_request
  const mergeable = data.mergeable ?? false
  const conflicts = data.conflicts ?? []
  const hasConflicts = conflicts.length > 0

  const globalSettings = settingsData?.global
  const projectSettings = settingsData?.project
  const autoConfirm =
    (projectSettings?.on_confirmed?.auto_confirm !== undefined
      ? projectSettings.on_confirmed.auto_confirm
      : globalSettings?.on_confirmed?.auto_confirm) ?? false

  const hasReviewer = (() => {
    const projCreated = projectSettings?.on_created
    const globalCreated = globalSettings?.on_created
    const agentEnabled = projCreated?.agent_enabled !== undefined
      ? projCreated.agent_enabled
      : globalCreated?.agent_enabled
    return agentEnabled ?? false
  })()

  function handleRefresh() {
    void refetch()
    void queryClient.invalidateQueries({ queryKey: ['pr-list', namespace, project] })
  }

  async function handleReview() {
    setReviewing(true)
    try {
      await prReview(namespace, project, number)
      handleRefresh()
    } finally {
      setReviewing(false)
    }
  }

  async function handleMerge() {
    setMerging(true)
    try {
      await prMerge(namespace, project, number)
      handleRefresh()
    } finally {
      setMerging(false)
    }
  }

  async function handleOperatorReject() {
    setRejecting(true)
    try {
      await prOperatorReject(namespace, project, number, rejectReason || undefined)
      setRejectReason('')
      setRejectModalOpen(false)
      handleRefresh()
    } finally {
      setRejecting(false)
    }
  }

  async function handleClose() {
    setClosing(true)
    try {
      await prClose(namespace, project, number)
      handleRefresh()
    } finally {
      setClosing(false)
    }
  }

  return (
    <div className={styles.detail}>
      <div className={styles.titleRow}>
        <span className={styles.prTitle}>#{pr.number} {pr.title}</span>
        <PRStatusBadge state={pr.state} />
      </div>

      <div className={styles.meta}>
        {pr.source_branch} → {pr.target_branch} · by {pr.author} · {relativeTime(pr.created_at)}
        {pr.approved_by && ` · approved by ${pr.approved_by}`}
      </div>

      {pr.description && (
        <div className={styles.descriptionSection}>
          <div className={styles.sectionTitle}>Description</div>
          <div className={styles.descriptionBody}>
            <Markdown content={pr.description} />
          </div>
        </div>
      )}

      {pr.review_files && pr.review_files.length > 0 && (
        <div className={styles.section}>
          <FileRefs label="Review files:" files={pr.review_files} />
        </div>
      )}

      <FileRefs label="Order files:" files={pr.order_files} />
      <FileRefs label="Result files:" files={pr.result_files} />

      {pr.state === 'merged' && (
        <div className={styles.mergedInfo}>
          Merged {pr.merged_at ? relativeTime(pr.merged_at) : ''}
          {pr.merge_commit && ` · ${pr.merge_commit.slice(0, 8)}`}
          {pr.source_branch_deleted && ' · source branch deleted'}
        </div>
      )}

      {pr.state === 'open' && (
        <div className={styles.section}>
          <div className={styles.actions}>
            {hasReviewer && (
              <button className={styles.reviewBtn} disabled={reviewing} onClick={() => void handleReview()}>
                {reviewing ? 'Reviewing…' : 'Review'}
              </button>
            )}
            <button
              className={styles.rejectBtn}
              onClick={() => setRejectModalOpen(true)}
            >
              Reject
            </button>
          </div>
        </div>
      )}

      {pr.state === 'approved' && !autoConfirm && (
        <div className={styles.section}>
          <div className={styles.actions}>
            <button
              className={styles.confirmBtn}
              disabled={!mergeable || merging}
              onClick={() => void handleMerge()}
              title={!mergeable ? 'Resolve conflicts first' : undefined}
            >
              {merging ? 'Merging…' : 'Confirm'}
            </button>
            <button
              className={styles.rejectBtn}
              onClick={() => setRejectModalOpen(true)}
            >
              Reject
            </button>
          </div>
        </div>
      )}

      {pr.state === 'approved' && autoConfirm && (
        <div className={styles.section}>
          <div className={styles.autoMerge}>Auto-merge enabled</div>
        </div>
      )}

      {pr.state === 'rejected' && (
        <div className={styles.section}>
          <div style={{ marginTop: '0.75rem' }}>
            <button className={styles.actionBtn} disabled={closing} onClick={() => void handleClose()}>
              {closing ? 'Closing…' : 'Close'}
            </button>
          </div>
        </div>
      )}

      {hasConflicts && (pr.state === 'open' || pr.state === 'approved' || pr.state === 'merge_conflict') && (
        <div className={styles.section}>
          <ConflictDisplay
            conflicts={conflicts}
            namespace={namespace}
            project={project}
            sourceBranch={pr.source_branch}
            targetBranch={pr.target_branch}
          />
        </div>
      )}

      {pr.state === 'interrupted' && pr.interrupt_info && (
        <div className={styles.section}>
          <InterruptedPanel
            namespace={namespace}
            project={project}
            number={number}
            info={pr.interrupt_info}
            onRefresh={handleRefresh}
          />
        </div>
      )}

      {rejectModalOpen && (
        <div className={styles.rejectOverlay} onClick={() => { setRejectModalOpen(false); setRejectReason('') }}>
          <div
            className={styles.rejectDialog}
            role="dialog"
            aria-modal="true"
            aria-label={`Reject PR #${pr.number}`}
            onClick={(e) => e.stopPropagation()}
          >
            <h2 className={styles.rejectDialogTitle}>Reject PR #{pr.number}</h2>
            <label className={styles.rejectDialogLabel}>
              Reason (optional)
              <textarea
                className={styles.rejectDialogTextarea}
                rows={3}
                value={rejectReason}
                onChange={(e) => setRejectReason(e.target.value)}
                placeholder="Why is this PR being rejected?"
                autoFocus
              />
            </label>
            <div className={styles.rejectDialogActions}>
              <button
                className={styles.rejectDialogCancel}
                onClick={() => { setRejectModalOpen(false); setRejectReason('') }}
              >
                Cancel
              </button>
              <button
                className={styles.rejectDialogConfirm}
                disabled={rejecting}
                onClick={() => void handleOperatorReject()}
              >
                {rejecting ? 'Rejecting…' : 'Reject'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

export function PRViewPane() {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useRouterState({ select: (s) => s.location.search as { pr?: string } })

  const m = pathname.match(/^\/p\/([^/]+)\/([^/]+)/)
  if (!m) return null

  const namespace = decodeURIComponent(m[1])
  const project = decodeURIComponent(m[2])
  const prNumber = search.pr ? Number(search.pr) : NaN

  if (!Number.isFinite(prNumber)) {
    return (
      <div className={styles.empty}>
        <div className={styles.emptyIcon}>
          <svg width="48" height="48" viewBox="0 0 24 24" fill="none">
            <circle cx="6" cy="6" r="2.5" stroke="currentColor" strokeWidth="1.4" />
            <circle cx="6" cy="18" r="2.5" stroke="currentColor" strokeWidth="1.4" />
            <circle cx="18" cy="18" r="2.5" stroke="currentColor" strokeWidth="1.4" />
            <path d="M6 8.5v7M18 15.5V10a2 2 0 0 0-2-2H9" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
          </svg>
        </div>
        <div className={styles.emptyText}>Select a pull request</div>
      </div>
    )
  }

  return (
    <div className={styles.container}>
      <PRDetail namespace={namespace} project={project} number={prNumber} />
    </div>
  )
}
