import { createContext, useContext, type ReactNode } from 'react'

// The client-side administrator predicate for the OAuth management surface (the
// 2026-06-03 MCP OAuth (c) directive §2.1a). It gates whether the management
// view and its palette entry are EXPOSED — but this is the SECONDARY gate: the
// authoritative one is server-side (manager.go's AdminAuthorizer on
// OAUTH_LIST/OAUTH_REVOKE). Hiding the UI alone is not security; the server
// refuses a non-admin regardless.
//
// B-28 ATTACH POINT: Shoka has no login/role concept on the Web UI yet, and the
// single-user operator IS the administrator, so this defaults to true today —
// the operator sees and uses the screen normally. When the Web-auth / multi-user
// leg (B-28) lands, the real authenticated identity drives this value (and the
// matching server seam), and non-admins stop seeing the entry. The `value` prop
// is the injection point (and lets tests drive the non-admin path).

const Ctx = createContext<boolean>(true)

export function AdminProvider({
  value = true,
  children,
}: {
  value?: boolean
  children: ReactNode
}) {
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

export function useIsAdmin(): boolean {
  return useContext(Ctx)
}
