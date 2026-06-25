import { lazy, Suspense } from 'react'
import {
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  redirect,
  RouterProvider,
} from '@tanstack/react-router'
import {
  Shell,
  CoreScreensProvider,
  type CoreScreensConfig,
  type SettingsItem,
} from '@shoka/web-core'
import { SshKeySection } from './components/SshKeySection'
import { SeedConfigSection } from './components/SeedConfigSection'
import { SshKeysPage } from './components/SshKeysPage'
import { ResumeBanner } from './components/ResumeBanner'

const SettingsPage = lazy(() =>
  import('@shoka/web-core/pages/SettingsPage').then((m) => ({
    default: m.SettingsPage,
  })),
)

const extraSettingsItems: SettingsItem[] = [
  {
    id: 'sshkeys',
    label: 'SSH Keys',
    visible: (v) => v.isSuperUser || v.managesAnyNamespace,
    component: SshKeysPage,
    deniedBody: 'You do not have permission to manage SSH keys.',
  },
]

const coreConfig: CoreScreensConfig = {
  extraSettingsItems,
  hiddenSettingsItemIds: ['librarian'],
  renderNamespaceSections: (namespace: string) => (
    <SshKeySection namespace={namespace} />
  ),
  renderProjectSections: (namespace: string, project: string) => (
    <SeedConfigSection namespace={namespace} project={project} />
  ),
}

function SettingsLazy() {
  return (
    <Suspense fallback={<div style={{ padding: '2rem', color: 'var(--c-text-dim)' }}>Loading…</div>}>
      <SettingsPage />
    </Suspense>
  )
}

const rootRoute = createRootRoute({
  component: () => (
    <Shell>
      <ResumeBanner />
      <Outlet />
    </Shell>
  ),
})

interface SettingsSearch {
  item?: string
}

function validateSettingsSearch(search: Record<string, unknown>): SettingsSearch {
  const item = typeof search.item === 'string' && search.item ? search.item : undefined
  return item ? { item } : {}
}

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  beforeLoad: () => {
    throw redirect({ to: '/settings', search: { item: 'namespaces' } })
  },
})

const settingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/settings',
  validateSearch: validateSettingsSearch,
  component: SettingsLazy,
})

const projectSettingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/p/$namespace/$project/settings',
  validateSearch: validateSettingsSearch,
  component: SettingsLazy,
})

const routeTree = rootRoute.addChildren([indexRoute, settingsRoute, projectSettingsRoute])

const router = createRouter({
  routeTree,
  defaultPreload: 'intent',
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
