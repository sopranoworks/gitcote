import { useQuery, keepPreviousData, type QueryClient } from '@tanstack/react-query'
import { getStatus, type AuthStatus } from './authClient'

export const AUTH_STATUS_KEY = ['auth-status'] as const

export function useAuthStatus() {
  return useQuery<AuthStatus>({
    queryKey: AUTH_STATUS_KEY,
    queryFn: getStatus,
    staleTime: 30_000,
    gcTime: Infinity,
    retry: 2,
    placeholderData: keepPreviousData,
  })
}

export function seedAuthStatus(qc: QueryClient, status: AuthStatus): void {
  qc.setQueryData(AUTH_STATUS_KEY, status)
}

// useIsSuperUser reports whether the current viewer is a super-user (admin over all
// namespaces) — the UI gate for the super-user settings items (user management, OAuth
// connections). It mirrors the SERVER's notion of super-user on both its paths:
//   - an authenticated session whose principal is admin (is_admin), AND
//   - the no-lockout empty-store posture (no users yet) — the de-facto single operator,
//     which the server treats as super-user (the no-session scope()="*" pass-through).
// Returns false until /auth/status has loaded (no flash of the items mid-fetch).
export function useIsSuperUser(): boolean {
  const { data } = useAuthStatus()
  if (!data) return false
  return !data.users_exist || !!data.principal?.is_admin
}

// useManagesAnyNamespace reports whether the viewer manages at least one namespace — a
// super-user OR a namespace-admin of ≥1 namespace (B-28 part 2). It is the UI gate for the
// "Namespace / project management" item (NOT super-user-only). It unions the server-derived
// manages_any_namespace flag with useIsSuperUser (which also covers the no-lockout
// empty-store operator). Returns false until /auth/status has loaded.
export function useManagesAnyNamespace(): boolean {
  const { data } = useAuthStatus()
  if (!data) return false
  return !data.users_exist || !!data.principal?.is_admin || !!data.manages_any_namespace
}
