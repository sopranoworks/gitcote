import { wsClient } from './wsClient'
import { OAuthDeniedError } from './oauthOps'
import type {
  ConfidentialClientInfo,
  ConfidentialListPayload,
  ConfidentialIssuePayload,
  OAuthDenied,
} from './types'

// B-71 Stage 3 — the admin-only CLIENT_* /ws/ui ops for confidential-mode management: issue / list
// / revoke pre-issued client credentials (Client ID + Secret). The client SECRET is shown ONCE in
// the CLIENT_ISSUE response and never returned again (stored hashed server-side); CLIENT_LIST never
// carries it. Refusals come back as an OAUTH_DENIED frame, surfaced as OAuthDeniedError (mirrors
// domainOps).

function throwIfDenied(type: string, payload: unknown): void {
  if (type === 'OAUTH_DENIED') {
    const p = payload as OAuthDenied
    throw new OAuthDeniedError(p.reason, p.message)
  }
}

export async function listConfidentialClients(): Promise<ConfidentialClientInfo[]> {
  const frame = await wsClient().requestFrame('CLIENT_LIST', {})
  throwIfDenied(frame.type, frame.payload)
  return (frame.payload as ConfidentialListPayload).clients ?? []
}

export interface ConfidentialIssueInput {
  scope: string
  name?: string
  validitySeconds: number
}

export async function issueConfidentialClient(
  input: ConfidentialIssueInput,
): Promise<ConfidentialIssuePayload> {
  const payload: Record<string, unknown> = {
    scope: input.scope,
    validity_seconds: input.validitySeconds,
  }
  if (input.name) payload.name = input.name
  const frame = await wsClient().requestFrame('CLIENT_ISSUE', payload)
  throwIfDenied(frame.type, frame.payload)
  return frame.payload as ConfidentialIssuePayload
}

export async function revokeConfidentialClient(id: string): Promise<void> {
  const frame = await wsClient().requestFrame('CLIENT_REVOKE', { id })
  throwIfDenied(frame.type, frame.payload)
}
