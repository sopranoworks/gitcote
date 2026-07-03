import { wsClient } from './wsClient'

// Self-service "My Account" ops over /ws/ui (B-28). Unlike adminOps these are NOT
// super-user-only — any authenticated user may call them, and they act on the
// caller's OWN account only (the server takes the identity from the session, never
// from these payloads — there is no target field to send). Email is the account id
// and has no setter. A rejected op (wrong current password, policy, empty name)
// rejects the promise with the server's message.

export interface AccountInfo {
  email: string
  display_name: string
  scope: string
  is_admin: boolean
  has_totp: boolean
  created_at: string
}

export function getAccount(): Promise<AccountInfo> {
  return wsClient().request('ACCOUNT_GET', {})
}

export function setAccountName(displayName: string): Promise<AccountInfo> {
  return wsClient().request('ACCOUNT_SET_NAME', { display_name: displayName })
}

export function setAccountPassword(
  currentPassword: string,
  newPassword: string,
): Promise<{ status: string }> {
  return wsClient().request('ACCOUNT_SET_PASSWORD', {
    current_password: currentPassword,
    new_password: newPassword,
  })
}

export interface TOTPEnrollResponse {
  secret: string
  otpauth_url: string
}

export function enrollTOTP(): Promise<TOTPEnrollResponse> {
  return wsClient().request('ACCOUNT_TOTP_ENROLL', {})
}

export function verifyTOTP(secret: string, code: string): Promise<AccountInfo> {
  return wsClient().request('ACCOUNT_TOTP_VERIFY', {
    totp_secret: secret,
    totp_code: code,
  })
}

export function disableTOTP(): Promise<AccountInfo> {
  return wsClient().request('ACCOUNT_TOTP_DISABLE', {})
}
