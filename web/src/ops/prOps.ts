import { wsClient } from '@shoka/web-core'

export interface PullRequest {
  number: number
  repo_namespace: string
  repo_project: string
  title: string
  description: string
  source_branch: string
  target_branch: string
  author: string
  state: string
  previous_state?: string
  mergeable: string
  source_commit: string
  target_commit: string
  merge_commit?: string
  created_at: string
  updated_at: string
  merged_at?: string
  closed_at?: string
  approved_by?: string
  approved_at?: string
  source_branch_deleted?: boolean
  rejection_reason?: string
  interrupt_info?: InterruptInfo
  order_files?: string[]
  result_files?: string[]
  review_files?: string[]
}

export interface InterruptInfo {
  reason: string
  detail: string
  agent_name: string
  agent_role: string
  at: string
}

export interface ConflictInfo {
  path: string
  type: string
}

export interface PRGetResponse {
  pull_request: PullRequest
  mergeable?: boolean
  conflicts?: ConflictInfo[]
  interrupted_previous_status?: string
  retry_eligible?: boolean
}

export function prList(
  namespace: string,
  projectName: string,
  state?: string,
): Promise<{ pull_requests: PullRequest[] }> {
  return wsClient().request('PR_LIST', { namespace, projectName, state })
}

export function prGet(
  namespace: string,
  projectName: string,
  number: number,
): Promise<PRGetResponse> {
  return wsClient().request('PR_GET', { namespace, projectName, number })
}

export function prMergeable(
  namespace: string,
  projectName: string,
  prNumber: number,
): Promise<{ mergeable: boolean; conflicts: ConflictInfo[] }> {
  return wsClient().request('PR_MERGEABLE', { namespace, projectName, prNumber })
}

export function prMerge(
  namespace: string,
  projectName: string,
  number: number,
): Promise<{ number: number; state: string; merge_commit?: string; error?: string; conflicts?: ConflictInfo[] }> {
  return wsClient().request('PR_MERGE', { namespace, projectName, number })
}

export function prRetryAgent(
  namespace: string,
  projectName: string,
  number: number,
  agentName?: string,
): Promise<{ number: number; state: string; message: string }> {
  return wsClient().request('PR_RETRY_AGENT', { namespace, projectName, number, agentName })
}

export function prDismissInterrupt(
  namespace: string,
  projectName: string,
  number: number,
): Promise<{ number: number; state: string; message: string }> {
  return wsClient().request('PR_DISMISS_INTERRUPT', { namespace, projectName, number })
}

export function prOperatorReject(
  namespace: string,
  projectName: string,
  prNumber: number,
  reason?: string,
): Promise<{ number: number; state: string }> {
  return wsClient().request('PR_OPERATOR_REJECT', { namespace, projectName, prNumber, reason })
}

export function prClose(
  namespace: string,
  projectName: string,
  number: number,
): Promise<{ number: number; state: string }> {
  return wsClient().request('PR_CLOSE', { namespace, projectName, number })
}

export function prReview(
  namespace: string,
  projectName: string,
  number: number,
): Promise<{ number: number; message: string }> {
  return wsClient().request('PR_REVIEW', { namespace, projectName, number })
}
