import { wsClient } from '@shoka/web-core'

export interface SeedSyncStatus {
  state: string
  direction?: string
  reason?: string
  last_push_at?: string
  last_result?: string
  paused_since?: string
}

export interface SeedConfig {
  seedUrl: string
  keyName: string
  pushMode: string
  pushInterval?: string
  syncStatus?: SeedSyncStatus
}

export interface SSHKeyInfo {
  name: string
  namespace: string
  algorithm: string
  public_key: string
  fingerprint: string
  created_at: string
  created_by: string
}

export interface SeedStatus {
  seedUrl: string
  keyName: string
  pushMode: string
  syncStatus?: SeedSyncStatus
}

export interface SeedTestResult {
  success: boolean
  error?: string
}

export function seedConfigGet(namespace: string, projectName: string): Promise<SeedConfig> {
  return wsClient().request('SEED_CONFIG_GET', { namespace, projectName })
}

export function seedConfigSet(
  namespace: string,
  projectName: string,
  seedUrl: string,
  keyName: string,
  pushMode: string,
  pushInterval?: string,
): Promise<{ status: string }> {
  return wsClient().request('SEED_CONFIG_SET', {
    namespace,
    projectName,
    seedUrl,
    keyName,
    pushMode,
    pushInterval,
  })
}

export function seedKeyGenerate(
  namespace: string,
  name: string,
): Promise<{ publicKey: string }> {
  return wsClient().request('SEED_KEY_GENERATE', { namespace, name })
}

export function seedKeyImport(
  namespace: string,
  name: string,
  privateKeyPem: string,
): Promise<{ publicKey: string; fingerprint: string }> {
  return wsClient().request('SEED_KEY_IMPORT', { namespace, name, privateKeyPem })
}

export function seedKeyList(
  namespace: string,
): Promise<{ keys: SSHKeyInfo[] }> {
  return wsClient().request('SEED_KEY_LIST', { namespace })
}

export function seedKeyDelete(
  namespace: string,
  name: string,
): Promise<{ status: string }> {
  return wsClient().request('SEED_KEY_DELETE', { namespace, name })
}

export function seedTest(
  namespace: string,
  projectName: string,
): Promise<SeedTestResult> {
  return wsClient().request('SEED_TEST', { namespace, projectName })
}

export function seedPush(
  namespace: string,
  projectName: string,
  branch?: string,
): Promise<{ success: boolean; error?: string }> {
  return wsClient().request('SEED_PUSH', { namespace, projectName, branch })
}

export function seedPull(
  namespace: string,
  projectName: string,
): Promise<{ success: boolean; error?: string }> {
  return wsClient().request('SEED_PULL', { namespace, projectName })
}

export function seedResume(
  email: string,
  password: string,
): Promise<{ status: string; vault: string }> {
  return wsClient().request('SEED_RESUME', { email, password })
}

export function seedStatus(
  namespace: string,
  projectName: string,
): Promise<SeedStatus> {
  return wsClient().request('SEED_STATUS', { namespace, projectName })
}

export function seedSyncRetry(
  namespace: string,
  projectName: string,
): Promise<{ status: string; message: string }> {
  return wsClient().request('SEED_SYNC_RETRY', { namespace, projectName })
}

export function seedSyncDismiss(
  namespace: string,
  projectName: string,
): Promise<{ status: string; message: string }> {
  return wsClient().request('SEED_SYNC_DISMISS', { namespace, projectName })
}
