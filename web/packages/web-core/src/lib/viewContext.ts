import type { ViewContext } from './notifyRouter'

export function deriveViewContext(pathname: string): ViewContext {
  if (pathname === '/') return { route: 'home' }

  const blob = pathname.match(/^\/p\/([^/]+)\/([^/]+)\/blob\/(.*)$/)
  if (blob) {
    return {
      route: 'blob',
      namespace: decodeURIComponent(blob[1]),
      project: decodeURIComponent(blob[2]),
      path: blob[3],
    }
  }

  const edit = pathname.match(/^\/p\/([^/]+)\/([^/]+)\/edit\/(.*)$/)
  if (edit) {
    return {
      route: 'edit',
      namespace: decodeURIComponent(edit[1]),
      project: decodeURIComponent(edit[2]),
      path: edit[3],
    }
  }

  const project = pathname.match(/^\/p\/([^/]+)\/([^/]+)\/?$/)
  if (project) {
    return {
      route: 'project',
      namespace: decodeURIComponent(project[1]),
      project: decodeURIComponent(project[2]),
    }
  }

  return { route: 'other' }
}
