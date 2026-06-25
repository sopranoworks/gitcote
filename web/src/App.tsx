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
  CoreScreensProvider,
  SettingsItemList,
  type CoreScreensConfig,
  type SettingsItem,
} from '@shoka/web-core'
import { SshKeySection } from './components/SshKeySection'
import { SeedConfigSection } from './components/SeedConfigSection'
import { ResumeBanner } from './components/ResumeBanner'
import styles from './App.module.css'

const SettingsPage = lazy(() =>
  import('@shoka/web-core/pages/SettingsPage').then((m) => ({
    default: m.SettingsPage,
  })),
)

const extraSettingsItems: SettingsItem[] = []

const coreConfig: CoreScreensConfig = {
  extraSettingsItems,
  renderNamespaceSections: (namespace: string) => (
    <SshKeySection namespace={namespace} />
  ),
  renderProjectSections: (namespace: string, project: string) => (
    <SeedConfigSection namespace={namespace} project={project} />
  ),
}

function SettingsLayout() {
  return (
    <div className={styles.layout}>
      <SettingsItemList />
      <div className={styles.content}>
        <Suspense fallback={<div className={styles.loading}>Loading…</div>}>
          <SettingsPage />
        </Suspense>
      </div>
    </div>
  )
}

const rootRoute = createRootRoute({
  component: () => (
    <div className={styles.root}>
      <ResumeBanner />
      <Outlet />
    </div>
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
  component: SettingsLayout,
})

const projectSettingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/p/$namespace/$project/settings',
  validateSearch: validateSettingsSearch,
  component: SettingsLayout,
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
