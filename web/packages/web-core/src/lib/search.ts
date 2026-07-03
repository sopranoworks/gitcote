import { useQuery } from '@tanstack/react-query'
import { wsClient } from './wsClient'
import type { SearchMatch } from './types'

interface SearchResultPayload {
  matches: SearchMatch[]
}

export async function searchFiles(
  namespace: string,
  project: string,
  query: string,
): Promise<SearchMatch[]> {
  const res = await wsClient().request<SearchResultPayload>('SEARCH_FILES', {
    namespace,
    projectName: project,
    query,
    search_in: 'both',
  })
  return res.matches ?? []
}

export function useSearchQuery(
  namespace: string,
  project: string,
  query: string,
) {
  const q = query.trim()
  return useQuery({
    queryKey: ['search', namespace, project, q],
    enabled: q !== '',
    queryFn: () => searchFiles(namespace, project, q),
  })
}
