// CoreScreensProvider — the extension seam for the reusable core screens.
//
// The auth/user/OAuth/settings/namespace screens are reusable by a second product
// (e.g. GitYard) that imports this package. Such a consumer needs to (a) add its own
// Settings items and (b) inject extra sections into the namespace/project management
// screen — WITHOUT modifying the screens. This context carries those extension inputs.
//
// Every field is OPTIONAL and defaults to "none", so an app that does NOT wrap with
// CoreScreensProvider (Shoka) behaves exactly as before: no extra settings items, no
// extra management sections. A consumer wraps its tree with <CoreScreensProvider value={…}>
// to inject.
import { createContext, useContext, type ReactNode } from 'react'
import type { SettingsItem } from './settingsRegistry'

export interface CoreScreensConfig {
  // extraSettingsItems are merged after the built-in Settings items (each with its own
  // `component` and `visible` predicate).
  extraSettingsItems?: SettingsItem[]
  // hiddenSettingsItemIds suppresses built-in items by id (e.g. a consumer that does not
  // have a librarian can hide it with ['librarian']). Applied before the visible filter.
  hiddenSettingsItemIds?: string[]
  // renderNamespaceSections injects extra content into each namespace block of the
  // namespace/project management screen (e.g. an SSH-key management section).
  renderNamespaceSections?: (namespace: string) => ReactNode
  // renderProjectSections injects extra content per project (e.g. seed-repo config,
  // sync status, a Resume control).
  renderProjectSections?: (namespace: string, project: string) => ReactNode
}

const CoreScreensContext = createContext<CoreScreensConfig>({})

export function CoreScreensProvider({
  value,
  children,
}: {
  value?: CoreScreensConfig
  children: ReactNode
}) {
  return (
    <CoreScreensContext.Provider value={value ?? {}}>{children}</CoreScreensContext.Provider>
  )
}

// useCoreScreens returns the active extension config (empty defaults when unwrapped).
export function useCoreScreens(): CoreScreensConfig {
  return useContext(CoreScreensContext)
}
