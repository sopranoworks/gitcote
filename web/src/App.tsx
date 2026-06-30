import { lazy, Suspense } from 'react'
import {
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  redirect,
  RouterProvider,
  useRouterState,
} from '@tanstack/react-router'
import {
  Shell,
  RepoListPage,
  ProjectPage,
  BlobPage,
  HistoryPage,
  SearchPage,
  CoreScreensProvider,
  type CoreScreensConfig,
  type SettingsItem,
} from '@shoka/web-core'
import { SshKeySection } from './components/SshKeySection'
import { SeedConfigSection } from './components/SeedConfigSection'
import { SshKeysPage } from './components/SshKeysPage'
import { UserSshKeysSection } from './components/UserSshKeysSection'
import { ResumeBanner } from './components/ResumeBanner'
import { PREventsSettingsGlobal } from './components/PREventsSettings'
import { AgentSettingsProjectControl } from './components/AgentSettingsProjectControl'
import { PRViewPane } from './components/PRViewPane'

function SettingsResumeBanner() {
  const item = useRouterState({ select: (s) => (s.location.search as { item?: string }).item })
  if (item !== 'namespaces') return null
  return <ResumeBanner />
}

const SettingsPage = lazy(() =>
  import('@shoka/web-core/pages/SettingsPage').then((m) => ({
    default: m.SettingsPage,
  })),
)

const extraSettingsItems: SettingsItem[] = [
  {
    id: 'my-ssh-keys',
    label: 'My SSH Keys',
    visible: () => true,
    component: UserSshKeysSection,
    deniedBody: '',
  },
  {
    id: 'sshkeys',
    label: 'Deploy Keys',
    visible: (v) => v.isSuperUser || v.managesAnyNamespace,
    component: SshKeysPage,
    deniedBody: 'You do not have permission to manage deploy keys.',
  },
  {
    id: 'agent-settings',
    label: 'Agent Settings',
    visible: (v) => v.isSuperUser || v.managesAnyNamespace,
    component: PREventsSettingsGlobal,
    deniedBody: 'You do not have permission to manage agent settings.',
  },
]

const coreConfig: CoreScreensConfig = {
  extraSettingsItems,
  hiddenSettingsItemIds: ['librarian'],
  renderNamespaceSections: (namespace: string) => (
    <SshKeySection namespace={namespace} />
  ),
  renderProjectSections: (namespace: string, project: string) => (
    <>
      <SeedConfigSection namespace={namespace} project={project} />
      <AgentSettingsProjectControl namespace={namespace} project={project} />
    </>
  ),
}

function SettingsLazy() {
  return (
    <>
      <SettingsResumeBanner />
      <Suspense fallback={<div style={{ padding: '2rem', color: 'var(--c-text-dim)' }}>Loading…</div>}>
        <SettingsPage />
      </Suspense>
    </>
  )
}

const rootRoute = createRootRoute({
  component: () => (
    <Shell>
      <Outlet />
    </Shell>
  ),
})

interface IndexSearch { ns?: string }

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  validateSearch: (search: Record<string, unknown>): IndexSearch => {
    const ns = typeof search.ns === 'string' ? search.ns : undefined
    return ns ? { ns } : {}
  },
  component: RepoListPage,
})

const projectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/p/$namespace/$project',
  component: ProjectPage,
})

const blobRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/p/$namespace/$project/blob/$',
  component: BlobPage,
})

interface HistorySearch { at?: string; from?: string; to?: string; mode?: 'version' | 'diff' }

const historyRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/p/$namespace/$project/history/$',
  validateSearch: (search: Record<string, unknown>): HistorySearch => {
    const str = (v: unknown) => (typeof v === 'string' && v ? v : undefined)
    const mode = search.mode === 'version' || search.mode === 'diff' ? search.mode : undefined
    return {
      ...(str(search.at) ? { at: str(search.at) } : {}),
      ...(str(search.from) ? { from: str(search.from) } : {}),
      ...(str(search.to) ? { to: str(search.to) } : {}),
      ...(mode ? { mode } : {}),
    }
  },
  component: HistoryPage,
})

interface SearchSearch { q?: string }

const searchRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/p/$namespace/$project/search',
  validateSearch: (search: Record<string, unknown>): SearchSearch => {
    const q = typeof search.q === 'string' ? search.q : undefined
    return q ? { q } : {}
  },
  component: SearchPage,
})

interface PRsSearch { pr?: string }

const prsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/p/$namespace/$project/prs',
  validateSearch: (search: Record<string, unknown>): PRsSearch => {
    const pr = typeof search.pr === 'string' && search.pr ? search.pr : undefined
    return pr ? { pr } : {}
  },
  component: PRViewPane,
})

const connectionsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/admin/connections',
  beforeLoad: () => {
    throw redirect({ to: '/settings', search: { item: 'oauth' } })
  },
})

interface SettingsSearch { item?: string }

function validateSettingsSearch(search: Record<string, unknown>): SettingsSearch {
  const item = typeof search.item === 'string' && search.item ? search.item : undefined
  return item ? { item } : {}
}

const projectSettingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/p/$namespace/$project/settings',
  validateSearch: validateSettingsSearch,
  component: SettingsLazy,
})

const settingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/settings',
  validateSearch: validateSettingsSearch,
  component: SettingsLazy,
})

const routeTree = rootRoute.addChildren([
  indexRoute,
  projectRoute,
  blobRoute,
  historyRoute,
  searchRoute,
  prsRoute,
  connectionsRoute,
  projectSettingsRoute,
  settingsRoute,
])

const router = createRouter({
  routeTree,
  defaultPreload: 'intent',
  scrollRestoration: true,
  getScrollRestorationKey: (location) => location.href,
})

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}

export function App() {
  return (
    <CoreScreensProvider value={coreConfig}>
      <RouterProvider router={router} />
    </CoreScreensProvider>
  )
}
