import type { QueryClient } from '@tanstack/react-query'
import type { FileContent } from './types'

// Routes /ws/ui NOTIFY events (and reconnect revalidation) into TanStack Query
// cache effects + user-facing banner/toast intents.
//
// Event shape is source-verified from internal/notify/center.go: the kinds that
// reach the browser are file.write, file.delete, file.move, project.create, and
// catalog.invariant_violation. `target` is the JOINED string "namespace/project"
// (not separate fields — the directive §1.2 hint was wrong here); there is no
// project.delete event. See reports/progress/2026-06-01-session-2-notify-event-inventory.md.
//
// `sourcePath` (wire `source_path`) is carried ONLY by file.move: it is the old
// path, while `path` is the new path. Session 2's parse dropped unknown fields,
// so the move-file directive extends both the type and parseNotifyEvent to keep
// it — without this the displayed view could not be matched against the move's
// origin and the follow (§2.9) would be impossible.

export interface NotifyEvent {
  kind: string
  target: string // "namespace/project"
  path?: string
  sourcePath?: string // file.move only: the old path (wire `source_path`)
  seq?: number
  timestamp?: string
}

export interface ViewContext {
  route: 'home' | 'project' | 'blob' | 'edit' | 'other'
  namespace?: string
  project?: string
  path?: string
}

export interface BannerIntent {
  text: string
  reload: () => void
  // Optional authorship slot (feasibility §1.2.1). NOTIFY events carry no author
  // today, so nothing populates this yet; the seam exists so a future
  // author-bearing event flows through routeNotify → BannerIntent.by → the
  // banner with a one-line change, not a rewrite.
  by?: string
}
export interface ToastIntent {
  level: 'warn'
  text: string
}
// An external-change signal for the edit route. Unlike `banner` (whose Reload
// refetches the displayed query), this never touches the editor buffer — the
// editor renders its own banner with buffer-safe actions. See lib/editSignal.
export type EditSignalIntent =
  | { kind: 'write'; path: string }
  | { kind: 'delete'; path: string }
  // The edited file was moved elsewhere: the editor follows to `to` itself,
  // buffer-safe (NotifyBridge does not navigate the edit route, so the editor's
  // dirty guard cannot block the follow).
  | { kind: 'move'; path: string; to: string }
// A follow intent for file.move: the open view of a moved file should navigate
// from the old path to the new, preserving mode (blob→blob, edit→edit). The
// router relocates the file cache; NotifyBridge performs the navigation. For the
// edit route this is buffer-safe — EditorPage keeps its initialized buffer across
// a same-route param change, so a dirty buffer rides along to the new path.
export interface FollowIntent {
  route: 'blob' | 'edit'
  namespace: string
  project: string
  path: string
}
export interface RouterResult {
  banner?: BannerIntent
  toast?: ToastIntent
  editSignal?: EditSignalIntent
  follow?: FollowIntent
}

export function parseNotifyEvent(payload: unknown): NotifyEvent | null {
  if (!payload || typeof payload !== 'object') return null
  const p = payload as Record<string, unknown>
  if (typeof p.kind !== 'string' || typeof p.target !== 'string') return null
  return {
    kind: p.kind,
    target: p.target,
    path: typeof p.path === 'string' ? p.path : undefined,
    sourcePath: typeof p.source_path === 'string' ? p.source_path : undefined,
    seq: typeof p.seq === 'number' ? p.seq : undefined,
    timestamp: typeof p.timestamp === 'string' ? p.timestamp : undefined,
  }
}

// Split the joined "namespace/project" target on the first slash (both are
// single path segments in Shoka's filesystem layout).
export function splitTarget(target: string): {
  namespace: string
  project: string
} {
  const i = target.indexOf('/')
  if (i < 0) return { namespace: target, project: '' }
  return { namespace: target.slice(0, i), project: target.slice(i + 1) }
}

// The query key that is the "core content" of the currently displayed view.
export function coreKeyForView(view: ViewContext): unknown[] | null {
  if (view.route === 'home') return ['projects']
  if (view.route === 'project' && view.namespace && view.project)
    return ['tree', view.namespace, view.project]
  if (view.route === 'blob' && view.namespace && view.project)
    return ['file', view.namespace, view.project, view.path ?? '']
  return null
}

function sameKey(a: unknown[] | null, b: unknown[] | null): boolean {
  if (!a || !b || a.length !== b.length) return false
  return a.every((v, i) => v === b[i])
}

// Route one NOTIFY event. Performs the cache invalidations directly and returns
// any banner/toast intent for the app to render.
export function routeNotify(
  event: NotifyEvent,
  queryClient: QueryClient,
  view: ViewContext,
): RouterResult {
  const { kind } = event

  if (kind === 'project.create') {
    const key = ['projects']
    if (view.route === 'home') {
      // Displayed core: mark stale without auto-refetch; banner gates the reload.
      queryClient.invalidateQueries({ queryKey: key, refetchType: 'none' })
      return {
        banner: {
          text: 'Projects changed',
          reload: () => void queryClient.refetchQueries({ queryKey: key }),
        },
      }
    }
    queryClient.invalidateQueries({ queryKey: key })
    return {}
  }

  if (kind === 'file.write' || kind === 'file.delete') {
    const { namespace, project } = splitTarget(event.target)
    const path = event.path ?? ''
    const treeKey = ['tree', namespace, project]
    const fileKey = ['file', namespace, project, path]

    const onThisFile =
      view.route === 'blob' &&
      view.namespace === namespace &&
      view.project === project &&
      (view.path ?? '') === path
    const editingThisFile =
      view.route === 'edit' &&
      view.namespace === namespace &&
      view.project === project &&
      (view.path ?? '') === path

    if (editingThisFile) {
      // The edit route's "core" is the in-memory buffer, not a query. Mark the
      // file query stale WITHOUT refetching (a refetch-and-replace would discard
      // the user's edits — §2), refresh the sidebar tree (peripheral), and emit a
      // buffer-safe signal the editor turns into its own banner. We deliberately
      // do NOT return a `banner` here: the generic banner's Reload refetches.
      queryClient.invalidateQueries({ queryKey: fileKey, refetchType: 'none' })
      queryClient.invalidateQueries({ queryKey: treeKey })
      return {
        editSignal: { kind: kind === 'file.delete' ? 'delete' : 'write', path },
      }
    }

    if (onThisFile) {
      queryClient.invalidateQueries({ queryKey: fileKey, refetchType: 'none' })
      queryClient.invalidateQueries({ queryKey: treeKey }) // sidebar: peripheral
      return {
        banner: {
          text:
            kind === 'file.delete'
              ? 'This file was deleted'
              : 'This file was updated',
          reload: () => void queryClient.refetchQueries({ queryKey: fileKey }),
        },
      }
    }
    // No file open / on the bare project root (or any non-file view): a file
    // update has no actionable meaning to raise a notification about — the sidebar
    // tree is an active query that refreshes live, so the change still surfaces in
    // the visible context, but WITHOUT a banner. (A "project top" banner with no
    // file selected was meaningless.) Only the open blob/edit file raises one.
    queryClient.invalidateQueries({ queryKey: treeKey })
    queryClient.invalidateQueries({ queryKey: fileKey })
    return {}
  }

  if (kind === 'file.move') {
    // A move is a pure path change (B-33): there is NO link surface here — no
    // count, no banner about stale links. The issuer is sender-excluded and
    // drives its own follow from MOVE_ACK (lib/moveController), so this branch
    // only runs on OTHER connections.
    const { namespace, project } = splitTarget(event.target)
    const src = event.sourcePath ?? ''
    const dst = event.path ?? ''
    const treeKey = ['tree', namespace, project]
    // The tree is peripheral: refresh it live so the rename shows.
    queryClient.invalidateQueries({ queryKey: treeKey })

    const onMoved =
      (view.route === 'blob' || view.route === 'edit') &&
      view.namespace === namespace &&
      view.project === project &&
      (view.path ?? '') === src

    if (onMoved) {
      // Relocate the file cache old→new so the follow lands on warm data with no
      // flash. A pure move leaves content (hence etag) unchanged, so the cached
      // etag carries over to the new path.
      const old = queryClient.getQueryData<FileContent>(['file', namespace, project, src])
      if (old) {
        queryClient.setQueryData<FileContent>(['file', namespace, project, dst], {
          path: dst,
          content: old.content,
          etag: old.etag,
        })
      }
      queryClient.removeQueries({
        queryKey: ['file', namespace, project, src],
        exact: true,
      })
      // Blob: NotifyBridge navigates (no guard there). Edit: the editor follows
      // itself (buffer-safe; NotifyBridge must NOT navigate the edit route or the
      // editor's dirty guard would block it and prompt to discard).
      if (view.route === 'edit') {
        return { editSignal: { kind: 'move', path: src, to: dst } }
      }
      return { follow: { route: 'blob', namespace, project, path: dst } }
    }

    // Not the displayed file: drop any stale cache for the old path; no banner.
    queryClient.removeQueries({
      queryKey: ['file', namespace, project, src],
      exact: true,
    })
    return {}
  }

  if (kind === 'oauth.change') {
    queryClient.invalidateQueries({ queryKey: ['oauth-connections'] })
    queryClient.invalidateQueries({ queryKey: ['oauth-domains'] })
    queryClient.invalidateQueries({ queryKey: ['oauth-confidential-clients'] })
    return {}
  }

  if (kind === 'catalog.invariant_violation') {
    const where = event.path ? `${event.target} (${event.path})` : event.target
    return { toast: { level: 'warn', text: `Catalog inconsistency in ${where}` } }
  }

  // Unknown kind: log and ignore (forward-compatible; §1.6 "no mapping" case).
  if (typeof console !== 'undefined')
    console.warn('notifyRouter: unmapped NOTIFY kind', kind)
  return {}
}

// On reconnect (§1.4 stale-while-revalidate): everything is potentially stale.
// Mark all queries stale without refetching, then background-refetch every
// active query EXCEPT the displayed view's core (never auto-reload displayed
// content — §2); surface a banner so the user pulls the core when ready.
export function routeReconnect(
  queryClient: QueryClient,
  view: ViewContext,
): RouterResult {
  queryClient.invalidateQueries({ refetchType: 'none' })
  const core = coreKeyForView(view)
  void queryClient.refetchQueries({
    type: 'active',
    predicate: (q) => !sameKey(q.queryKey as unknown[], core),
  })
  if (!core) return {}
  return {
    banner: {
      text: 'Reconnected — content may have changed while offline',
      reload: () => void queryClient.refetchQueries({ queryKey: core }),
    },
  }
}
