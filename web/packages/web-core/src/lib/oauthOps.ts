import { wsClient } from './wsClient'
import type {
  OAuthConnection,
  OAuthListPayload,
  OAuthDenied,
  OAuthIssueSelfPayload,
} from './types'

// The single imperative /ws/ui ops for the OAuth connection management surface
// (the 2026-06-03 MCP OAuth (c) directive). No component touches wsClient
// directly — every list/revoke funnels through here, mirroring lib/fileOps. The
// ops are administrator-only; the AUTHORITATIVE gate is server-side (manager.go),
// so a non-admin or oauth-disabled caller comes back as an OAUTH_DENIED frame,
// which we surface as a typed OAuthDeniedError (distinct from a transport error).

// OAuthDeniedError marks a server-side authorization/capability refusal of an
// admin-only OAuth request, carrying the typed reason ("forbidden" |
// "oauth_disabled") so the UI can distinguish "you are not an admin" / "OAuth is
// off" from a generic connection failure.
export class OAuthDeniedError extends Error {
  readonly reason: string
  constructor(reason: string, message: string) {
    super(message)
    this.name = 'OAuthDeniedError'
    this.reason = reason
  }
}

// listConnections returns the live OAuth/MCP connections (no secrets). Throws
// OAuthDeniedError on an OAUTH_DENIED refusal; a transport/ERROR frame rejects
// via requestFrame as usual.
export async function listConnections(): Promise<OAuthConnection[]> {
  const frame = await wsClient().requestFrame('OAUTH_LIST', {})
  if (frame.type === 'OAUTH_DENIED') {
    const p = frame.payload as OAuthDenied
    throw new OAuthDeniedError(p.reason, p.message)
  }
  const p = frame.payload as OAuthListPayload
  return p.connections ?? []
}

// revokeConnection revokes exactly one connection by series id (the backend
// guarantees others are untouched). Throws OAuthDeniedError on refusal.
export async function revokeConnection(seriesId: string): Promise<void> {
  const frame = await wsClient().requestFrame('OAUTH_REVOKE', {
    series_id: seriesId,
  })
  if (frame.type === 'OAUTH_DENIED') {
    const p = frame.payload as OAuthDenied
    throw new OAuthDeniedError(p.reason, p.message)
  }
  // OAUTH_REVOKE ack: the revoke succeeded. Nothing else to read.
}

// issueSelfToken mints a fresh access token for the operator (the "token to self"
// path) and returns it ONCE. This is the only op that receives a secret — the
// caller must show it once for copy and never persist it. validitySeconds is the
// operator's per-issuance FINITE expiry (B-71 Stage 4); omit/0 ⇒ the finite global
// default (never infinite). Throws OAuthDeniedError on an admin/oauth-disabled refusal.
export async function issueSelfToken(
  validitySeconds = 0,
  name = '',
  scope = '',
): Promise<OAuthIssueSelfPayload> {
  const payload: Record<string, unknown> = { validity_seconds: validitySeconds }
  if (name) payload.name = name
  if (scope) payload.scope = scope
  const frame = await wsClient().requestFrame('OAUTH_ISSUE_SELF', payload)
  if (frame.type === 'OAUTH_DENIED') {
    const p = frame.payload as OAuthDenied
    throw new OAuthDeniedError(p.reason, p.message)
  }
  return frame.payload as OAuthIssueSelfPayload
}

// clientDomain extracts the display host from a connecting client's CIMD
// metadata URL (its identity — shown at runtime to tell connections apart,
// directive §0(b)). Falls back to the raw value if it does not parse as a URL.
// No concrete client-domain value is ever hardcoded — this derives it from
// whatever the store holds at runtime.
export function clientDomain(clientId: string): string {
  try {
    return new URL(clientId).host
  } catch {
    return clientId
  }
}
