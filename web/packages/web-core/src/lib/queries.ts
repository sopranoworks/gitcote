import { useQuery, useQueries } from '@tanstack/react-query'
import { wsClient } from './wsClient'
import { listConnections } from './oauthOps'
import { listDomains } from './domainOps'
import { listConfidentialClients } from './confidentialOps'
import { flattenFilePaths } from './tree'
import type {
  ProjectInfo,
  FileNode,
  FileContent,
  HistoryPayload,
  FileAtContent,
  FileDiff,
} from './types'

// Reusable read-queries shared by the core screens (OAuth management + the project
// list used by user/namespace management). The document-tree/file/history queries live
// in the app (web/src/lib/queries.ts), which re-exports these names for its own callers.

// The OAuth connections list query key — invalidated after a revoke so the list
// reflects the store. There is no oauth NOTIFY (the store emits none), so a
// live-refresh is manual: invalidate-on-revoke is the floor, plus an explicit
// Refresh action on the management view.
export const OAUTH_CONNECTIONS_KEY = ['oauth-connections'] as const

// The live OAuth/MCP connections, for the admin management view. `enabled` gates
// the fetch on the admin predicate so a non-admin never even issues OAUTH_LIST.
export function useConnectionsQuery(enabled = true) {
  return useQuery({
    queryKey: OAUTH_CONNECTIONS_KEY,
    enabled,
    queryFn: () => listConnections(),
  })
}

// B-71 Stage 2d: the dynamic "domain" entries (trusted domain + per-domain TTL + a
// consent-set indicator) for the domain-mode management screen. Admin-gated like the
// connections query; invalidated after a create/update/delete.
export const OAUTH_DOMAINS_KEY = ['oauth-domains'] as const

export function useDomainsQuery(enabled = true) {
  return useQuery({
    queryKey: OAUTH_DOMAINS_KEY,
    enabled,
    queryFn: () => listDomains(),
  })
}

// B-71 Stage 3: the confidential pre-issued clients (Client ID + Secret) for the
// confidential-mode management screen. Admin-gated like the domains query; invalidated after an
// issue/revoke. The secret is never in this list (only on the issue response, once).
export const OAUTH_CLIENTS_KEY = ['oauth-confidential-clients'] as const

export function useConfidentialClientsQuery(enabled = true) {
  return useQuery({
    queryKey: OAUTH_CLIENTS_KEY,
    enabled,
    queryFn: () => listConfidentialClients(),
  })
}

// The project list (['projects']). Read over /ws/ui and shared by the user/namespace
// management screens (and the app's document UI, via the web/ re-export).
export function useProjectsQuery() {
  return useQuery({
    queryKey: ['projects'],
    queryFn: () => wsClient().request<ProjectInfo[]>('GET_PROJECTS', {}),
  })
}

// --- Content queries (document tree / file / history) ----------------------

export function useTreeQuery(namespace: string, project: string) {
  return useQuery({
    queryKey: ['tree', namespace, project],
    queryFn: async () =>
      (await wsClient().request<FileNode[] | null>('GET_TREE', {
        namespace,
        projectName: project,
      })) ?? [],
  })
}

export function useFileQuery(namespace: string, project: string, path: string) {
  return useQuery({
    queryKey: ['file', namespace, project, path],
    enabled: path !== '',
    queryFn: () =>
      wsClient().request<FileContent>('READ_FILE', {
        namespace,
        projectName: project,
        path,
      }),
  })
}

export function useHistoryQuery(
  namespace: string,
  project: string,
  path: string,
) {
  return useQuery({
    queryKey: ['history', namespace, project, path],
    enabled: path !== '',
    queryFn: () =>
      wsClient().request<HistoryPayload>('GET_HISTORY', {
        namespace,
        projectName: project,
        path,
      }),
  })
}

export function useFileAtQuery(
  namespace: string,
  project: string,
  path: string,
  hash: string,
) {
  return useQuery({
    queryKey: ['file-at', namespace, project, path, hash],
    enabled: path !== '' && hash !== '',
    queryFn: () =>
      wsClient().request<FileAtContent>('GET_FILE_AT', {
        namespace,
        projectName: project,
        path,
        hash,
      }),
  })
}

export function useDiffQuery(
  namespace: string,
  project: string,
  path: string,
  fromHash: string,
  toHash: string,
) {
  return useQuery({
    queryKey: ['diff', namespace, project, path, fromHash, toHash],
    enabled: path !== '' && fromHash !== '' && toHash !== '',
    queryFn: () =>
      wsClient().request<FileDiff>('GET_DIFF', {
        namespace,
        projectName: project,
        path,
        fromHash,
        toHash,
      }),
  })
}

export interface GlobalFile {
  namespace: string
  project: string
  path: string
}

export function useAllProjectFiles(enabled: boolean): GlobalFile[] {
  const { data: projects = [] } = useProjectsQuery()

  const results = useQueries({
    queries: projects.map((p) => ({
      queryKey: ['tree', p.namespace, p.name],
      queryFn: async () =>
        (await wsClient().request<FileNode[] | null>('GET_TREE', {
          namespace: p.namespace,
          projectName: p.name,
        })) ?? [],
      enabled,
    })),
  })

  const files: GlobalFile[] = []
  results.forEach((r, i) => {
    const p = projects[i]
    if (!p || !r.data) return
    for (const path of flattenFilePaths(r.data)) {
      files.push({ namespace: p.namespace, project: p.name, path })
    }
  })
  return files
}
