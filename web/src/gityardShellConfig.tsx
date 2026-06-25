import {
  SettingsItemList,
  useSimpleRailControls,
  useNoopRailReset,
  type ShellConfig,
} from '@shoka/web-core'
import { Toaster } from './components/Toaster'

const REPOS_ICON = (
  <svg width="22" height="22" viewBox="0 0 24 24" fill="none">
    <path
      d="M4 19V5a2 2 0 0 1 2-2h13v14H6a2 2 0 0 0-2 2zm0 0a2 2 0 0 0 2 2h13V17"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
    />
  </svg>
)

const SETTINGS_ICON = (
  <svg width="22" height="22" viewBox="0 0 24 24" fill="none">
    <circle cx="12" cy="12" r="3" stroke="currentColor" strokeWidth="1.6" />
    <path
      d="M12 3.5v2M12 18.5v2M20.5 12h-2M5.5 12h-2M17.5 6.5l-1.4 1.4M7.9 16.1l-1.4 1.4M17.5 17.5l-1.4-1.4M7.9 7.9 6.5 6.5"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
    />
  </svg>
)

function GitYardSidebar({ view }: { view: string }) {
  if (view === 'settings') return <SettingsItemList />
  return <SettingsItemList />
}

export const gityardShellConfig: ShellConfig = {
  brandName: 'GitYard',
  railItems: [
    { id: 'repos', label: 'Repositories', icon: REPOS_ICON },
    { id: 'settings', label: 'Settings', icon: SETTINGS_ICON },
  ],
  renderSidebar: (view) => <GitYardSidebar view={view} />,
  renderToaster: () => <Toaster />,
  useRailControls: useSimpleRailControls,
  useResetRailOnProjectChange: useNoopRailReset,
  layoutAutoSaveId: 'gityard-layout',
}
