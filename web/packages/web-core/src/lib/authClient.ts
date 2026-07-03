// authClient — the WebUI multi-user login surface client (B-28 stage 1). It talks
// to the server /auth/* endpoints over fetch (the session cookie is set/cleared by
// the server; same-origin requests carry it automatically) and wraps the WebAuthn
// browser ceremonies with the standard base64url <-> ArrayBuffer glue go-webauthn
// expects. The MCP token surface is never involved here (B-50 separation).

export interface AuthPrincipal {
  email: string
  display_name: string
  is_admin: boolean
}

export interface AuthStatus {
  users_exist: boolean
  authenticated: boolean
  first_run_allowed: boolean
  passkey_enabled: boolean
  principal?: AuthPrincipal
  // manages_any_namespace: server-derived (super-user OR namespace-admin of ≥1 namespace),
  // the B-28 part-2 ns/proj-management Settings item's visibility predicate.
  manages_any_namespace?: boolean
}

async function asJSON<T>(res: Response): Promise<T> {
  const body = (await res.json().catch(() => ({}))) as T & { error?: string; totp_required?: boolean }
  if (!res.ok) {
    const err = new Error(body?.error || `request failed (${res.status})`) as Error & {
      status?: number
      totpRequired?: boolean
    }
    err.status = res.status
    err.totpRequired = !!body?.totp_required
    throw err
  }
  return body
}

function post(path: string, body?: unknown): Promise<Response> {
  return fetch(path, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
}

export async function getStatus(): Promise<AuthStatus> {
  return asJSON<AuthStatus>(await fetch('/auth/status', { credentials: 'same-origin' }))
}

export async function registerFirstAdmin(input: {
  email: string
  display_name: string
  password: string
  totp_secret?: string
  totp_code?: string
}): Promise<AuthStatus> {
  return asJSON<AuthStatus>(await post('/auth/register', input))
}

export async function login(input: {
  email: string
  password: string
  totp_code?: string
}): Promise<AuthStatus> {
  return asJSON<AuthStatus>(await post('/auth/login', input))
}

export async function logout(): Promise<void> {
  await post('/auth/logout')
}

export interface NewTOTP {
  secret: string
  otpauth_url: string
}

export async function newTOTP(email: string): Promise<NewTOTP> {
  return asJSON<NewTOTP>(await post('/auth/totp/new', { email }))
}

export interface InviteInfo {
  email: string
  scope: string
}

export async function inviteInfo(code: string): Promise<InviteInfo> {
  return asJSON<InviteInfo>(await post('/auth/invite/info', { code }))
}

export async function redeemInvite(input: {
  code: string
  display_name: string
  password: string
  totp_secret?: string
  totp_code?: string
}): Promise<AuthStatus> {
  return asJSON<AuthStatus>(await post('/auth/invite/redeem', input))
}

// --- WebAuthn (passkey) glue -------------------------------------------------

function b64urlToBuf(s: string): ArrayBuffer {
  const pad = '='.repeat((4 - (s.length % 4)) % 4)
  const b64 = (s + pad).replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(b64)
  const buf = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) buf[i] = raw.charCodeAt(i)
  return buf.buffer
}

function bufToB64url(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf)
  let s = ''
  for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i])
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

// isPasskeyCapable reports whether this browser context can run WebAuthn (a secure
// context with the API present). On a bare-IP HTTP origin this is false and the UI
// falls back to the password/TOTP floor.
export function isPasskeyCapable(): boolean {
  return (
    typeof window !== 'undefined' &&
    !!window.PublicKeyCredential &&
    !!window.isSecureContext &&
    !!navigator.credentials
  )
}

// registerPasskey runs the attestation ceremony for the LOGGED-IN user (adds a
// passkey on top of the password floor).
export async function registerPasskey(): Promise<void> {
  const options = await asJSON<{ publicKey: PublicKeyCredentialCreationOptionsJSON }>(
    await post('/auth/webauthn/register/begin'),
  )
  const pk = options.publicKey
  const created = (await navigator.credentials.create({
    publicKey: {
      ...pk,
      challenge: b64urlToBuf(pk.challenge),
      user: { ...pk.user, id: b64urlToBuf(pk.user.id) },
      excludeCredentials: (pk.excludeCredentials || []).map((c) => ({ ...c, id: b64urlToBuf(c.id) })),
    } as unknown as PublicKeyCredentialCreationOptions,
  })) as PublicKeyCredential
  const att = created.response as AuthenticatorAttestationResponse
  await asJSON(
    await post('/auth/webauthn/register/finish', {
      id: created.id,
      rawId: bufToB64url(created.rawId),
      type: created.type,
      response: {
        attestationObject: bufToB64url(att.attestationObject),
        clientDataJSON: bufToB64url(att.clientDataJSON),
      },
      clientExtensionResults: created.getClientExtensionResults(),
    }),
  )
}

// loginPasskey runs the assertion ceremony for a named account and, on success,
// establishes the session (the server sets the cookie).
export async function loginPasskey(email: string): Promise<AuthStatus> {
  const options = await asJSON<{ publicKey: PublicKeyCredentialRequestOptionsJSON }>(
    await post('/auth/webauthn/login/begin', { email }),
  )
  const pk = options.publicKey
  const assertion = (await navigator.credentials.get({
    publicKey: {
      ...pk,
      challenge: b64urlToBuf(pk.challenge),
      allowCredentials: (pk.allowCredentials || []).map((c) => ({ ...c, id: b64urlToBuf(c.id) })),
    } as unknown as PublicKeyCredentialRequestOptions,
  })) as PublicKeyCredential
  const asr = assertion.response as AuthenticatorAssertionResponse
  return asJSON<AuthStatus>(
    await post('/auth/webauthn/login/finish', {
      id: assertion.id,
      rawId: bufToB64url(assertion.rawId),
      type: assertion.type,
      response: {
        authenticatorData: bufToB64url(asr.authenticatorData),
        clientDataJSON: bufToB64url(asr.clientDataJSON),
        signature: bufToB64url(asr.signature),
        userHandle: asr.userHandle ? bufToB64url(asr.userHandle) : undefined,
      },
      clientExtensionResults: assertion.getClientExtensionResults(),
    }),
  )
}

// Minimal JSON shapes for the option documents the server returns (challenge/id are
// base64url strings on the wire; we convert them to ArrayBuffers above).
interface PublicKeyCredentialCreationOptionsJSON {
  challenge: string
  user: { id: string; name: string; displayName: string }
  excludeCredentials?: Array<{ id: string; type: string; transports?: string[] }>
  [k: string]: unknown
}
interface PublicKeyCredentialRequestOptionsJSON {
  challenge: string
  allowCredentials?: Array<{ id: string; type: string; transports?: string[] }>
  [k: string]: unknown
}
