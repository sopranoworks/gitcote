import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { prGet, prMerge, prRetryAgent, prDismissInterrupt, prOperatorReject, prClose, type ConflictInfo } from '../ops/prOps'
import { prEventSettingsGet } from '../ops/eventSettingsOps'
import { serverSshInfo } from '../ops/userSshKeyOps'
import { PRStatusBadge } from './PRStatusBadge'
import { AgentSelector } from './AgentSelector'
import styles from './PRDetailView.module.css'

function relativeTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  if (diff < 60_000) return 'just now'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`
  return `${Math.floor(diff / 86_400_000)}d ago`
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
      <div style={{ marginTop: '0.75rem', fontSize: '0.82rem', color: 'var(--c-text-dim)' }}>
        Resolve manually:
      </div>
      <div className={styles.codeBlock}>
{`git clone ${cloneUrl}
cd ${project}
git checkout ${sourceBranch}
git merge origin/${targetBranch}
# resolve conflicts
git add . && git commit
git push`}
      </div>
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
    } catch {
      // ignore
    } finally {
      setBusy(false)
    }
  }

  async function handleDismiss() {
    setBusy(true)
    try {
      await prDismissInterrupt(namespace, project, number)
      onRefresh()
    } catch {
      // ignore
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className={styles.interruptPanel}>
      <div className={styles.interruptHeader}>
        <PRStatusBadge state="interrupted" />
        <span style={{ fontSize: '0.82rem', color: 'var(--c-text-dim)' }}>
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
        <button
          className={styles.actionBtn}
          disabled={busy}
          onClick={() => setSwitchAgent(!switchAgent)}
        >
          Switch Agent
        </button>
        {switchAgent && (
          <>
            <AgentSelector
              role={info.agent_role}
              value={selectedAgent}
              onChange={setSelectedAgent}
            />
            <button
              className={styles.actionBtn}
              disabled={busy || !selectedAgent}
              onClick={() => void handleRetry(selectedAgent)}
            >
              Retry with selected
            </button>
          </>
        )}
      </div>
    </div>
  )
}

function FileRefs({ label, files }: { label: string; files?: string[] }) {
  if (!files || files.length === 0) return null
  return (
    <div style={{ marginTop: '0.5rem' }}>
      <div style={{ fontSize: '0.82rem', color: 'var(--c-text-dim)' }}>{label}</div>
      {files.map((f) => (
        <div key={f} style={{ fontSize: '0.82rem', fontFamily: 'monospace', marginLeft: '0.5rem' }}>{f}</div>
      ))}
    </div>
  )
}

export function PRDetailView({
  namespace,
  project,
  number,
  onBack,
}: {
  namespace: string
  project: string
  number: number
  onBack: () => void
}) {
  const queryClient = useQueryClient()
  const [merging, setMerging] = useState(false)
  const [rejecting, setRejecting] = useState(false)
  const [rejectReason, setRejectReason] = useState('')
  const [closing, setClosing] = useState(false)

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

  const globalConfirm = settingsData?.global?.on_confirmed
  const projectConfirm = settingsData?.project?.on_confirmed
  const autoConfirm =
    (projectConfirm?.auto_confirm !== undefined ? projectConfirm.auto_confirm : globalConfirm?.auto_confirm) ?? false

  function handleRefresh() {
    void refetch()
    void queryClient.invalidateQueries({ queryKey: ['pr-list', namespace, project] })
  }

  async function handleMerge() {
    setMerging(true)
    try {
      await prMerge(namespace, project, number)
      handleRefresh()
    } catch {
      // ignore
    } finally {
      setMerging(false)
    }
  }

  async function handleOperatorReject() {
    setRejecting(true)
    try {
      await prOperatorReject(namespace, project, number, rejectReason || undefined)
      setRejectReason('')
      handleRefresh()
    } catch {
      // ignore
    } finally {
      setRejecting(false)
    }
  }

  async function handleClose() {
    setClosing(true)
    try {
      await prClose(namespace, project, number)
      handleRefresh()
    } catch {
      // ignore
    } finally {
      setClosing(false)
    }
  }

  return (
    <div className={styles.container}>
      <button className={styles.backBtn} onClick={onBack}>
        ← Back to list
      </button>

      <div className={styles.titleRow}>
        <span className={styles.prTitle}>#{pr.number} {pr.title}</span>
        <PRStatusBadge state={pr.state} />
      </div>

      <div className={styles.meta}>
        {pr.source_branch} → {pr.target_branch} · by {pr.author} · {relativeTime(pr.created_at)}
        {pr.approved_by && ` · approved by ${pr.approved_by}`}
      </div>

      {pr.description && (
        <div className={styles.description}>{pr.description}</div>
      )}

      {/* Merged info */}
      {pr.state === 'merged' && (
        <div className={styles.mergedInfo}>
          Merged {pr.merged_at ? relativeTime(pr.merged_at) : ''}
          {pr.merge_commit && ` · ${pr.merge_commit.slice(0, 8)}`}
          {pr.source_branch_deleted && ' · source branch deleted'}
        </div>
      )}

      {/* Review report + CONFIRM/REJECT for approved PRs (manual confirm) */}
      {pr.state === 'approved' && !autoConfirm && (
        <div className={styles.section}>
          <FileRefs label="Review files:" files={pr.review_files} />
          <div className={styles.actions} style={{ marginTop: '0.75rem' }}>
            <button
              className={styles.mergeBtn}
              data-variant="primary"
              disabled={!mergeable || merging}
              onClick={() => void handleMerge()}
              title={!mergeable ? 'Resolve conflicts first' : undefined}
            >
              {merging ? 'Merging…' : 'Confirm (Merge)'}
            </button>
            <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', marginTop: '0.5rem' }}>
              <input
                type="text"
                placeholder="Rejection reason (optional)"
                value={rejectReason}
                onChange={(e) => setRejectReason(e.target.value)}
                className={styles.rejectInput}
                style={{ flex: 1, padding: '0.4rem 0.6rem', fontSize: '0.85rem', borderRadius: '4px', border: '1px solid var(--c-border)', background: 'var(--c-bg-input, var(--c-bg))' }}
              />
              <button
                className={styles.actionBtn}
                data-variant="danger"
                disabled={rejecting}
                onClick={() => void handleOperatorReject()}
              >
                {rejecting ? 'Rejecting…' : 'Reject'}
              </button>
            </div>
          </div>
        </div>
      )}

      {pr.state === 'approved' && autoConfirm && (
        <div className={styles.section}>
          <div className={styles.autoMerge}>
            Auto-merge enabled
          </div>
        </div>
      )}

      {/* Rejected by operator */}
      {pr.state === 'rejected' && (
        <div className={styles.section}>
          <div style={{ color: '#f85149', fontWeight: 600 }}>Rejected by operator</div>
          {pr.rejection_reason && (
            <div style={{ padding: '0.6rem 0.75rem', background: 'rgba(248, 81, 73, 0.08)', border: '1px solid rgba(248, 81, 73, 0.2)', borderRadius: '6px', fontSize: '0.85rem', lineHeight: '1.5', whiteSpace: 'pre-wrap', marginTop: '0.5rem' }}>{pr.rejection_reason}</div>
          )}
          <FileRefs label="Review files:" files={pr.review_files} />
          <FileRefs label="Order files:" files={pr.order_files} />
          <FileRefs label="Result files:" files={pr.result_files} />
          <div style={{ marginTop: '0.75rem' }}>
            <button
              className={styles.actionBtn}
              disabled={closing}
              onClick={() => void handleClose()}
            >
              {closing ? 'Closing…' : 'Close'}
            </button>
          </div>
        </div>
      )}

      {/* Conflict display */}
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

      {/* Interrupted panel */}
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
    </div>
  )
}
