// @shoka/web-core — the reusable core surface: screens, shell, and providers.
//
// A second product (e.g. GitYard) imports the shell layout, the content screens,
// the settings framework, the WebSocket client + ops, and the shared hooks/providers
// from here, and extends them via ShellProvider + CoreScreensProvider — without
// modifying this package. Design tokens: `@shoka/web-core/tokens.css`.

// --- shell layout ----------------------------------------------------------
export { Shell } from './components/Shell'
export { TitleBar, titleBarStyles } from './components/TitleBar'
export { ActivityRail, activityRailStyles } from './components/ActivityRail'
export { StatusBar } from './components/StatusBar'
export { ConnectionStatus } from './components/ConnectionStatus'
export { Banner } from './components/Banner'

// --- shell config (injection seam) -----------------------------------------
export {
  ShellProvider,
  useShellConfig,
  useSimpleRailControls,
  useNoopRailReset,
  type ShellConfig,
  type RailItemDef,
} from './lib/shellConfig'

// --- shell providers -------------------------------------------------------
export { ThemeProvider, useTheme, type Theme } from './lib/theme'
export { PaletteProvider, usePalette } from './lib/palette'
export { BannerProvider, useBanner } from './lib/banner'
export { EditSignalProvider, useEditSignal, type EditSignal } from './lib/editSignal'
export { useMediaQuery } from './lib/useMediaQuery'
export { useConnectionStatus } from './lib/useConnectionStatus'

// --- content screens -------------------------------------------------------
export { RepoListPage } from './pages/RepoListPage'
export { ProjectPage } from './pages/ProjectPage'
export { BlobPage } from './pages/BlobPage'
export { HistoryPage } from './pages/HistoryPage'
export { SearchPage } from './pages/SearchPage'

// --- admin/settings screens ------------------------------------------------
export { MyAccountPage } from './pages/MyAccountPage'
export { UserManagementPage } from './pages/UserManagementPage'
export { ConnectionsPage } from './pages/ConnectionsPage'
export { NamespaceManagementPage } from './pages/NamespaceManagementPage'
export { LibrarianStatusPage } from './pages/LibrarianStatusPage'
export { ServerInfoPage } from './pages/ServerInfoPage'
export { SettingsPage } from './pages/SettingsPage'

// --- content components ----------------------------------------------------
export { Sidebar, sidebarStyles } from './components/Sidebar'
export { FileTree, fileTreeStyles, fileOpenRoute, type TreeOpenMode } from './components/FileTree'
export { Markdown } from './components/Markdown'
export { CodeView } from './components/CodeView'
export { DiffView, type DiffViewProps } from './components/DiffView'
export { RecoverButton } from './components/RecoverButton'

// --- content config (injection seam) ---------------------------------------
export {
  ContentProvider,
  useContentConfig,
  type ContentConfig,
} from './lib/contentConfig'

// --- content supporting libs -----------------------------------------------
export {
  toTreeNodes,
  filterTree,
  sortTree,
  flattenFilePaths,
  ancestorDirs,
  namespacesOf,
  dirOf,
  type SortMode,
} from './lib/tree'
export { classifyFile, isHighlightableCode, type FileKind } from './lib/fileKind'
export {
  needsConversion,
  convertedName,
  validateUtf8,
  csvToMarkdownTable,
  prepareConversions,
  convertCandidate,
  type ConversionCandidate,
  type ConvertibleExt,
} from './lib/fileConvert'
export { searchFiles, useSearchQuery } from './lib/search'
export { fuzzyScore, fuzzyFilter, type FuzzyResult } from './lib/fuzzy'
export { deriveViewContext } from './lib/viewContext'
export { useDebouncedValue } from './lib/useDebouncedValue'
export { lineDiff, type DiffRow, type DiffRowType } from './lib/lineDiff'
export { languageForPath } from './lib/cmLanguages'

// --- settings framework ----------------------------------------------------
export { SettingsItemList } from './components/SettingsItemList'

// --- reusable dialogs (used by the management screen; also consumed by the app) ---
export { ConversionConfirmDialog } from './components/ConversionConfirmDialog'
export { UploadDialog, validateDirPath, type UploadDialogProps } from './components/UploadDialog'
export { PromptDialog } from './components/PromptDialog'
export { RenameDialog } from './components/RenameDialog'
export { MoveProjectDialog } from './components/MoveProjectDialog'
export { TypeToConfirmDialog } from './components/TypeToConfirmDialog'
export {
  SETTINGS_ITEMS,
  visibleSettingsItems,
  type SettingsItem,
  type SettingsVisibility,
} from './lib/settingsRegistry'

// --- extension seam --------------------------------------------------------
export {
  CoreScreensProvider,
  useCoreScreens,
  type CoreScreensConfig,
} from './lib/coreScreens'

// --- WebSocket client + protocol -------------------------------------------
export * from './lib/wsClient'

// --- shared payload types --------------------------------------------------
export * from './lib/types'

// --- auth hooks ------------------------------------------------------------
export * from './lib/authClient'
export * from './lib/authStatus'

// --- auth components -------------------------------------------------------
export { AuthGate } from './components/AuthGate'
export { LoginScreen } from './components/LoginScreen'
export { FirstRunWizard } from './components/FirstRunWizard'
export { RedeemInvite } from './components/RedeemInvite'
export * from './lib/admin'

// --- toast / notify --------------------------------------------------------
export * from './lib/toast'
export * from './lib/notifyRouter'

// --- ops + reusable queries ------------------------------------------------
export * from './lib/accountOps'
// adminOps minus InviteInfo: the public InviteInfo is authClient's (redeem-side); adminOps'
// admin-list InviteInfo is consumed only internally by UserManagementPage. Re-exporting both
// via `export *` would be an ambiguous-name error.
export {
  type UserInfo,
  type InviteCreated,
  listUsers,
  setUserScope,
  setUserEnabled,
  setUserPassword,
  removeUser,
  createInvite,
  listInvites,
  revokeInvite,
} from './lib/adminOps'
export * from './lib/oauthOps'
export * from './lib/domainOps'
export * from './lib/confidentialOps'
export * from './lib/nsManageOps'
export * from './lib/librarianStatus'
export * from './lib/serverInfoOps'
export * from './lib/scope'
export * from './lib/queries'
