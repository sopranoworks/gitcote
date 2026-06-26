import { wsClient } from '@shoka/web-core'

export interface UserSshKey {
  fingerprint: string
  email: string
  key_type: string
  public_key: string
  title: string
  created_at: string
}

export function userSshKeyList(): Promise<{ keys: UserSshKey[] }> {
  return wsClient().request('USER_SSH_KEY_LIST', {})
}

export function userSshKeyAdd(
  publicKey: string,
  title: string,
): Promise<{ fingerprint: string; status: string }> {
  return wsClient().request('USER_SSH_KEY_ADD', { publicKey, title })
}

export function userSshKeyDelete(fingerprint: string): Promise<{ status: string }> {
  return wsClient().request('USER_SSH_KEY_DELETE', { fingerprint })
}

export interface SshInfo {
  enabled: boolean
  port?: number
}

export function serverSshInfo(): Promise<SshInfo> {
  return wsClient().request('SERVER_SSH_INFO', {})
}
