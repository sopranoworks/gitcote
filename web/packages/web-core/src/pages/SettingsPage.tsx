import type { ComponentType } from 'react'
import { useRouterState } from '@tanstack/react-router'
import { MyAccountPage } from './MyAccountPage'
import { UserManagementPage } from './UserManagementPage'
import { ConnectionsPage } from './ConnectionsPage'
import { NamespaceManagementPage } from './NamespaceManagementPage'
import { LibrarianStatusPage } from './LibrarianStatusPage'
import { ServerInfoPage } from './ServerInfoPage'
import { useIsSuperUser, useManagesAnyNamespace } from '../lib/authStatus'
import { SETTINGS_ITEMS, type SettingsItem } from '../lib/settingsRegistry'
import { useCoreScreens } from '../lib/coreScreens'
import styles from './SettingsPage.module.css'

// The built-in items' screens are wired by id here (rather than on the registry items)
// so the screen modules stay code-split in this Settings chunk and are NOT pulled into
// the always-present sidebar via the registry. Injected items carry their own
// `component`; built-ins resolve through this map.
const BUILTIN_COMPONENTS: Record<string, ComponentType> = {
  'server-info': ServerInfoPage,
  account: MyAccountPage,
  users: UserManagementPage,
  oauth: ConnectionsPage,
  namespaces: NamespaceManagementPage,
  librarian: LibrarianStatusPage,
}

function Placeholder({ title, body }: { title: string; body: string }) {
  return (
    <div className={styles.placeholder}>
      <h1 className={styles.title}>{title}</h1>
      <p>{body}</p>
    </div>
  )
}

// SettingsPage is the right-pane content of the Settings rail mode (B-28 stage 3). It
// reads the selected item from the URL (`?item=`) — the sidebar's SettingsItemList
// drives it — and renders that item's screen via registry-driven dispatch (look up the
// item by id, render its component). Permission is re-checked here (not just hidden in
// the list): a viewer reaching a gated item sees that item's deniedBody notice (the
// authoritative gate is server-side on each op). A consumer's injected items (from
// CoreScreensProvider) dispatch through the same path with their own component.
export function SettingsPage() {
  const item = useRouterState({ select: (s) => (s.location.search as { item?: string }).item })
  const isSuperUser = useIsSuperUser()
  const managesAnyNamespace = useManagesAnyNamespace()
  const { extraSettingsItems, hiddenSettingsItemIds } = useCoreScreens()

  if (!item) {
    return <Placeholder title="Settings" body="Choose a setting from the list." />
  }

  const hidden = new Set(hiddenSettingsItemIds ?? [])
  const items: SettingsItem[] = [...SETTINGS_ITEMS, ...(extraSettingsItems ?? [])].filter((it) => !hidden.has(it.id))
  const entry = items.find((it) => it.id === item)
  const Component = entry?.component ?? (entry ? BUILTIN_COMPONENTS[entry.id] : undefined)
  if (!entry || !Component) {
    return <Placeholder title="Settings" body="Unknown settings item." />
  }

  if (!entry.visible({ isSuperUser, managesAnyNamespace })) {
    return <Placeholder title={entry.label} body={entry.deniedBody ?? 'You do not have permission to view this.'} />
  }

  return <Component />
}
