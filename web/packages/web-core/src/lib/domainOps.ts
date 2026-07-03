import { wsClient } from './wsClient'
import { OAuthDeniedError } from './oauthOps'
import type {
  DomainInfo,
  DomainListPayload,
  DomainGenerateConsentPayload,
  OAuthDenied,
} from './types'

// The admin-only DOMAIN_* /ws/ui ops for domain-mode management: CRUD over the dynamic "domain"
// registration store (trusted domain + per-domain TTL + per-domain consent). Per the 2026-06-20
// threat model, per-domain CONSENT is now PLAINTEXT and operator-readable: it is server-GENERATED
// (DOMAIN_GENERATE_CONSENT, re-rollable), returned + listed (so the card can always show it), and
// never typed. Refusals come back as an OAUTH_DENIED frame, surfaced as OAuthDeniedError.

function throwIfDenied(type: string, payload: unknown): void {
  if (type === 'OAUTH_DENIED') {
    const p = payload as OAuthDenied
    throw new OAuthDeniedError(p.reason, p.message)
  }
}

export async function listDomains(): Promise<DomainInfo[]> {
  const frame = await wsClient().requestFrame('DOMAIN_LIST', {})
  throwIfDenied(frame.type, frame.payload)
  return (frame.payload as DomainListPayload).domains ?? []
}

export interface DomainCreateInput {
  domain: string
  accessTtlSeconds?: number
  refreshTtlSeconds?: number
  scope?: string
}

export async function createDomain(input: DomainCreateInput): Promise<DomainInfo> {
  const frame = await wsClient().requestFrame('DOMAIN_CREATE', {
    domain: input.domain,
    access_ttl_seconds: input.accessTtlSeconds ?? 0,
    refresh_ttl_seconds: input.refreshTtlSeconds ?? 0,
    scope: input.scope ?? '*',
  })
  throwIfDenied(frame.type, frame.payload)
  return frame.payload as DomainInfo
}

export interface DomainUpdateInput {
  id: string
  accessTtlSeconds: number
  refreshTtlSeconds: number
  scope: string
}

export async function updateDomain(input: DomainUpdateInput): Promise<DomainInfo> {
  const frame = await wsClient().requestFrame('DOMAIN_UPDATE', {
    id: input.id,
    access_ttl_seconds: input.accessTtlSeconds,
    refresh_ttl_seconds: input.refreshTtlSeconds,
    scope: input.scope,
  })
  throwIfDenied(frame.type, frame.payload)
  return frame.payload as DomainInfo
}

// generateDomainConsent mints (or re-rolls) a domain's per-domain consent value and returns the
// fresh PLAINTEXT value. Calling it again replaces the value (the old one stops working).
export async function generateDomainConsent(
  id: string,
): Promise<DomainGenerateConsentPayload> {
  const frame = await wsClient().requestFrame('DOMAIN_GENERATE_CONSENT', { id })
  throwIfDenied(frame.type, frame.payload)
  return frame.payload as DomainGenerateConsentPayload
}

export async function deleteDomain(id: string): Promise<void> {
  const frame = await wsClient().requestFrame('DOMAIN_DELETE', { id })
  throwIfDenied(frame.type, frame.payload)
}
