import { Link, useRouterState } from '@tanstack/react-router'
import { visibleSettingsItems } from '../lib/settingsRegistry'
import { useIsSuperUser, useManagesAnyNamespace } from '../lib/authStatus'
import { useCoreScreens } from '../lib/coreScreens'
import styles from './SettingsItemList.module.css'

// SettingsItemList is the Settings rail mode's sidebar: the permission-filtered list
// of settings items (B-28 stage 3). The gear/Settings view is always present; this
// list shows only the items the current principal may open. Selecting an item sets
// the `?item=` search on the current settings route (project-scoped or global), which
// the SettingsPage reads to render the item's screen in the right pane.
export function SettingsItemList() {
  const isSuperUser = useIsSuperUser()
  const managesAnyNamespace = useManagesAnyNamespace()
  const { extraSettingsItems, hiddenSettingsItemIds } = useCoreScreens()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const selected = useRouterState({
    select: (s) => (s.location.search as { item?: string }).item,
  })
  const items = visibleSettingsItems({ isSuperUser, managesAnyNamespace }, extraSettingsItems, hiddenSettingsItemIds)
  const proj = pathname.match(/^\/p\/([^/]+)\/([^/]+)/)

  return (
    <div className={styles.pane}>
      <div className={styles.sectionHeader}>
        <span className={styles.projTitle}>Settings</span>
      </div>
      <div className={styles.treeWrap}>
        {items.length === 0 ? (
          <div className={styles.empty}>No settings available for your account yet.</div>
        ) : (
          <nav aria-label="Settings items">
            {items.map((it) =>
              proj ? (
                <Link
                  key={it.id}
                  to="/p/$namespace/$project/settings"
                  params={{ namespace: decodeURIComponent(proj[1]), project: decodeURIComponent(proj[2]) }}
                  search={{ item: it.id }}
                  className={styles.settingsItem}
                  data-active={selected === it.id}
                  aria-current={selected === it.id ? 'page' : undefined}
                >
                  {it.label}
                </Link>
              ) : (
                <Link
                  key={it.id}
                  to="/settings"
                  search={{ item: it.id }}
                  className={styles.settingsItem}
                  data-active={selected === it.id}
                  aria-current={selected === it.id ? 'page' : undefined}
                >
                  {it.label}
                </Link>
              ),
            )}
          </nav>
        )}
      </div>
    </div>
  )
}
