import { useCallback, useEffect } from 'react'
import { Link, useNavigate, useRouterState } from '@tanstack/react-router'
import {
  Sidebar,
  ContentProvider,
  type ShellConfig,
} from '@shoka/web-core'
import { Toaster } from './components/Toaster'
import { CloneUrl } from './components/CloneUrl'
import { PRTreeView } from './components/PRTreeView'
import { AgentSettingsProjectControl } from './components/AgentSettingsProjectControl'

const EXPLORER_ICON = (
  <svg width="22" height="22" viewBox="0 0 24 24" fill="none">
    <path d="M4 5.5h5l2 2h9v11H4z" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round" />
  </svg>
)

const SEARCH_ICON = (
  <svg width="22" height="22" viewBox="0 0 24 24" fill="none">
    <circle cx="10.5" cy="10.5" r="6" stroke="currentColor" strokeWidth="1.6" />
    <path d="M15 15l4.5 4.5" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
  </svg>
)

const HISTORY_ICON = (
  <svg width="22" height="22" viewBox="0 0 24 24" fill="none">
    <path d="M4 12a8 8 0 1 0 2.5-5.8" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
    <path d="M4 4v3h3" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
    <path d="M12 8v4l3 2" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
)

const PR_ICON = (
  <svg width="22" height="22" viewBox="0 0 24 24" fill="none">
    <circle cx="6" cy="6" r="2.5" stroke="currentColor" strokeWidth="1.4" />
    <circle cx="6" cy="18" r="2.5" stroke="currentColor" strokeWidth="1.4" />
    <circle cx="18" cy="18" r="2.5" stroke="currentColor" strokeWidth="1.4" />
    <path d="M6 8.5v7M18 15.5V10a2 2 0 0 0-2-2H9" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
  </svg>
)

const SETTINGS_ICON = (
  <svg width="22" height="22" viewBox="0 0 24 24" fill="none">
    <circle cx="12" cy="12" r="3" stroke="currentColor" strokeWidth="1.6" />
    <path
      d="M12 3.5v2M12 18.5v2M20.5 12h-2M5.5 12h-2M17.5 6.5l-1.4 1.4M7.9 16.1l-1.4 1.4M17.5 17.5l-1.4-1.4M7.9 7.9 6.5 6.5"
      stroke="currentColor" strokeWidth="1.6" strokeLinecap="round"
    />
  </svg>
)

function isSettingsPath(pathname: string): boolean {
  return pathname === '/settings' || /^\/p\/[^/]+\/[^/]+\/settings/.test(pathname)
}

type Crumb =
  | { label: string; kind: 'ns'; ns: string }
  | { label: string; kind: 'project'; ns: string; proj: string }
  | { label: string; kind: 'blob'; ns: string; proj: string; path: string }

function useCrumbs(): Crumb[] {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useRouterState({ select: (s) => s.location.search as { ns?: string } })
  const crumbs: Crumb[] = []
  const m = pathname.match(/^\/p\/([^/]+)\/([^/]+)(?:\/(?:blob|history)\/(.*))?$/)
  if (!m) {
    if (typeof search.ns === 'string' && search.ns) {
      crumbs.push({ label: search.ns, kind: 'ns', ns: search.ns })
    }
    return crumbs
  }
  const ns = decodeURIComponent(m[1])
  const proj = decodeURIComponent(m[2])
  crumbs.push({ label: ns, kind: 'ns', ns })
  crumbs.push({ label: proj, kind: 'project', ns, proj })
  const rest = m[3]
  if (rest) {
    const segs = rest.split('/').filter(Boolean)
    let accum = ''
    segs.forEach((seg) => {
      accum = accum ? `${accum}/${seg}` : seg
      crumbs.push({ label: seg, kind: 'blob', ns, proj, path: accum })
    })
  }
  return crumbs
}

function GitYardBreadcrumbs({ styles }: { styles: Record<string, string> }) {
  const crumbs = useCrumbs()
  if (crumbs.length === 0) return null
  return (
    <>
      <span className={styles.brandChevron} aria-hidden="true">›</span>
      <nav className={styles.crumbs} aria-label="Breadcrumb">
        {crumbs.map((c, i) => {
          const isLast = i === crumbs.length - 1
          return (
            <span key={i} className={styles.crumbItem}>
              {i > 0 && <span className={styles.sep}>/</span>}
              {isLast ? (
                <span className={styles.crumbCurrent} aria-current="page">{c.label}</span>
              ) : c.kind === 'ns' ? (
                <Link to="/" search={{ ns: c.ns }} className={styles.crumbLink}>{c.label}</Link>
              ) : c.kind === 'project' ? (
                <Link to="/p/$namespace/$project" params={{ namespace: c.ns, project: c.proj }} className={styles.crumbLink}>{c.label}</Link>
              ) : (
                <span className={styles.crumbDir}>{c.label}</span>
              )}
            </span>
          )
        })}
      </nav>
    </>
  )
}

function parseProjectPrefix(pathname: string): { ns: string; proj: string } | null {
  const m = pathname.match(/^\/p\/([^/]+)\/([^/]+)/)
  if (!m) return null
  return { ns: decodeURIComponent(m[1]), proj: decodeURIComponent(m[2]) }
}

function parseProjectFile(pathname: string): { ns: string; proj: string; path: string | null } | null {
  const m = pathname.match(/^\/p\/([^/]+)\/([^/]+)(?:\/(?:blob|history)\/(.*))?$/)
  if (!m) return null
  return { ns: decodeURIComponent(m[1]), proj: decodeURIComponent(m[2]), path: m[3] ? decodeURIComponent(m[3]) : null }
}

function useGitYardRailControls(
  rail: string,
  sidebarOpen: boolean,
  setRail: (v: string) => void,
  setSidebarOpen: (open: boolean) => void,
): { onSelect: (v: string) => void; disabledItems: string[] } {
  const navigate = useNavigate()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const onProjectRoute = pathname.startsWith('/p/')
  const onHistoryRoute = /^\/p\/[^/]+\/[^/]+\/history(\/|$)/.test(pathname)
  const onPRsRoute = /^\/p\/[^/]+\/[^/]+\/prs/.test(pathname)
  const onSettingsRoute = isSettingsPath(pathname)

  const onSelect = useCallback(
    (v: string) => {
      if (v !== 'settings' && !onProjectRoute) return

      if (v === 'settings') {
        if (onSettingsRoute && sidebarOpen) { setSidebarOpen(false); return }
        setSidebarOpen(true)
        const ref = parseProjectPrefix(pathname)
        if (ref) {
          void navigate({ to: '/p/$namespace/$project/settings', params: { namespace: ref.ns, project: ref.proj } })
        } else {
          void navigate({ to: '/settings' })
        }
        return
      }

      if (v === 'explorer' && (onSettingsRoute || onPRsRoute)) {
        setRail('explorer')
        setSidebarOpen(true)
        const ref = parseProjectPrefix(pathname)
        if (ref) {
          void navigate({ to: '/p/$namespace/$project', params: { namespace: ref.ns, project: ref.proj } })
        } else {
          void navigate({ to: '/' })
        }
        return
      }

      if (v === 'explorer' && onHistoryRoute) {
        const ref = parseProjectFile(pathname)
        setRail('explorer')
        setSidebarOpen(true)
        if (ref?.path) {
          void navigate({ to: '/p/$namespace/$project/blob/$', params: { namespace: ref.ns, project: ref.proj, _splat: ref.path } })
        } else if (ref) {
          void navigate({ to: '/p/$namespace/$project', params: { namespace: ref.ns, project: ref.proj } })
        }
        return
      }

      if (v === rail && sidebarOpen) { setSidebarOpen(false); return }
      setRail(v)
      setSidebarOpen(true)

      if (v === 'history') {
        const ref = parseProjectFile(pathname)
        if (ref) {
          void navigate({ to: '/p/$namespace/$project/history/$', params: { namespace: ref.ns, project: ref.proj, _splat: ref.path ?? '' } })
        }
      }

      if (v === 'prs') {
        const ref = parseProjectPrefix(pathname)
        if (ref) {
          void navigate({ to: '/p/$namespace/$project/prs', params: { namespace: ref.ns, project: ref.proj } })
        }
      }
    },
    [onProjectRoute, onHistoryRoute, onPRsRoute, onSettingsRoute, rail, sidebarOpen, pathname, navigate, setRail, setSidebarOpen],
  )

  return { onSelect, disabledItems: onProjectRoute ? [] : ['explorer', 'search', 'history', 'prs'] }
}

function useResetRailOnProjectChange(setRail: (v: string) => void): void {
  const projectKey = useRouterState({
    select: (s) => {
      const m = s.location.pathname.match(/^\/p\/([^/]+)\/([^/]+)/)
      return m ? `${m[1]}/${m[2]}` : null
    },
  })
  useEffect(() => {
    if (projectKey) setRail('explorer')
  }, [projectKey, setRail])
}

function deriveActiveRail(pathname: string, rail: string): string {
  if (isSettingsPath(pathname)) return 'settings'
  if (/^\/p\/[^/]+\/[^/]+\/prs/.test(pathname)) return 'prs'
  return rail === 'settings' ? 'explorer' : rail
}

const gityardContentConfig = {
  renderProjectExtra: (ns: string, proj: string) => (
    <>
      <CloneUrl namespace={ns} project={proj} />
      <AgentSettingsProjectControl namespace={ns} project={proj} />
    </>
  ),
}

function GitYardShellWrapper({ children }: { children: React.ReactNode }) {
  return <ContentProvider value={gityardContentConfig}>{children}</ContentProvider>
}

export const gityardShellConfig: ShellConfig = {
  brandName: 'GitYard',
  railItems: [
    { id: 'explorer', label: 'Explorer', icon: EXPLORER_ICON },
    { id: 'search', label: 'Search', icon: SEARCH_ICON },
    { id: 'history', label: 'History', icon: HISTORY_ICON },
    { id: 'prs', label: 'Pull Requests', icon: PR_ICON },
    { id: 'settings', label: 'Settings', icon: SETTINGS_ICON },
  ],
  renderSidebar: (view) => {
    if (view === 'prs') return <PRTreeView />
    return <Sidebar view={view} />
  },
  renderBreadcrumbs: (styles) => <GitYardBreadcrumbs styles={styles} />,
  renderToaster: () => <Toaster />,
  shellWrapper: GitYardShellWrapper,
  useRailControls: useGitYardRailControls,
  useResetRailOnProjectChange: useResetRailOnProjectChange,
  deriveActiveRail,
  layoutAutoSaveId: 'gityard-layout',
}
