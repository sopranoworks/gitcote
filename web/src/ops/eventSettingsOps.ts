import { wsClient } from '@shoka/web-core'

export interface EventAction {
  agent_enabled?: boolean
  agent_name?: string
  auto_retry?: boolean
  max_retries?: number
  notify_enabled?: boolean
  notify_method?: string
}

export interface ConfirmAction {
  auto_confirm?: boolean
  notify_enabled?: boolean
  notify_method?: string
}

export interface PREventSettings {
  on_created?: EventAction
  on_confirmed?: ConfirmAction
  on_rejected?: EventAction
  on_merge_conflict?: EventAction
}

export interface SeedEventSettings {
  on_push_conflict?: EventAction
  on_pull_conflict?: EventAction
}

export function prEventSettingsGet(
  namespace: string,
  projectName: string,
): Promise<{ global: PREventSettings | null; project: PREventSettings | null }> {
  return wsClient().request('PR_EVENT_SETTINGS_GET', { namespace, projectName })
}

export function prEventSettingsSetGlobal(
  settings: PREventSettings,
): Promise<{ status: string }> {
  return wsClient().request('PR_EVENT_SETTINGS_SET_GLOBAL', settings)
}

export function prEventSettingsSetProject(
  namespace: string,
  projectName: string,
  settings: PREventSettings,
): Promise<{ status: string }> {
  return wsClient().request('PR_EVENT_SETTINGS_SET_PROJECT', { namespace, projectName, settings })
}

export function prEventSettingsClearProject(
  namespace: string,
  projectName: string,
): Promise<{ status: string }> {
  return wsClient().request('PR_EVENT_SETTINGS_CLEAR_PROJECT', { namespace, projectName })
}

export function seedEventSettingsGet(
  namespace: string,
  projectName: string,
): Promise<{ global: SeedEventSettings | null; project: SeedEventSettings | null }> {
  return wsClient().request('SEED_EVENT_SETTINGS_GET', { namespace, projectName })
}

export function seedEventSettingsSetGlobal(
  settings: SeedEventSettings,
): Promise<{ status: string }> {
  return wsClient().request('SEED_EVENT_SETTINGS_SET_GLOBAL', settings)
}

export function seedEventSettingsSetProject(
  namespace: string,
  projectName: string,
  settings: SeedEventSettings,
): Promise<{ status: string }> {
  return wsClient().request('SEED_EVENT_SETTINGS_SET_PROJECT', { namespace, projectName, settings })
}

export function seedEventSettingsClearProject(
  namespace: string,
  projectName: string,
): Promise<{ status: string }> {
  return wsClient().request('SEED_EVENT_SETTINGS_CLEAR_PROJECT', { namespace, projectName })
}
