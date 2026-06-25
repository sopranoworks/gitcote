import {
  SettingsItemList,
  useSimpleRailControls,
  useNoopRailReset,
  type ShellConfig,
} from '@shoka/web-core'
import { Toaster } from './components/Toaster'
import { ResumeBanner } from './components/ResumeBanner'

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

function GitYardSidebar() {
  return (
    <div>
      <ResumeBanner />
      <SettingsItemList />
    </div>
  )
}

export const gityardShellConfig: ShellConfig = {
  brandName: 'GitYard',
  railItems: [
    { id: 'settings', label: 'Settings', icon: SETTINGS_ICON },
  ],
  renderSidebar: () => <GitYardSidebar />,
  renderToaster: () => <Toaster />,
  useRailControls: useSimpleRailControls,
  useResetRailOnProjectChange: useNoopRailReset,
  layoutAutoSaveId: 'gityard-layout',
}
