// Client-side mirror of the server authz scope grammar (internal/authz) for the
// user-management scope editor (B-28 stage 3). A scope is a comma-separated grant
// list; each grant is a target ("*" wildcard, or a namespace) at a level (r/rw/admin).
// Bare "*" / empty parse to super-user (wildcard admin). The editor enforces ONE grant
// per target.

export type Level = 'r' | 'rw' | 'admin'

export interface Grant {
  target: string // '*' for wildcard, otherwise the namespace name
  level: Level
}

const LEVEL_LABEL: Record<Level, string> = { r: 'read-only', rw: 'read-write', admin: 'admin' }

export function levelLabel(l: Level): string {
  return LEVEL_LABEL[l]
}

function parseLevel(s: string | undefined): Level {
  if (s === 'admin' || s === 'rw' || s === 'r') return s
  return 'rw' // legacy level-less namespace grant ⇒ read-write (matches the server)
}

// parseScope turns a scope string into grants. "" or "*" ⇒ a single wildcard-admin
// (super-user) grant.
export function parseScope(scope: string): Grant[] {
  const s = (scope ?? '').trim()
  if (s === '' || s === '*') return [{ target: '*', level: 'admin' }]
  const out: Grant[] = []
  for (const raw of s.split(',')) {
    const g = raw.trim()
    if (!g) continue
    if (g === '*') {
      out.push({ target: '*', level: 'admin' })
      continue
    }
    if (g.startsWith('*:')) {
      out.push({ target: '*', level: parseLevel(g.slice(2)) })
      continue
    }
    if (g.startsWith('namespace:')) {
      const rest = g.slice('namespace:'.length)
      // rest is "<ns>[/<proj>][:level]"; the editor models namespace-level grants,
      // so we read the namespace (before any '/') and the trailing level.
      const lvlMatch = rest.match(/:(admin|rw|r)$/)
      const level = lvlMatch ? (lvlMatch[1] as Level) : 'rw'
      const body = lvlMatch ? rest.slice(0, -1 - lvlMatch[1].length) : rest
      const ns = body.split('/')[0]
      if (ns) out.push({ target: ns, level })
    }
  }
  return out
}

// serializeScope turns grants back into the scope string. Duplicate targets keep the
// most-permissive (matching the server fallback). An empty result is returned as ""
// — callers must guard, since the server reads "" as super-user.
export function serializeScope(grants: Grant[]): string {
  const rank: Record<Level, number> = { r: 1, rw: 2, admin: 3 }
  const byTarget = new Map<string, Level>()
  for (const g of grants) {
    const cur = byTarget.get(g.target)
    if (!cur || rank[g.level] > rank[cur]) byTarget.set(g.target, g.level)
  }
  const parts: string[] = []
  for (const [target, level] of byTarget) {
    parts.push(target === '*' ? `*:${level}` : `namespace:${target}:${level}`)
  }
  return parts.join(',')
}

// describeScope renders a scope readably for the user list, e.g. "all: admin" or
// "foo: read-write, bar: read-only".
export function describeScope(scope: string): string {
  const grants = parseScope(scope)
  if (grants.some((g) => g.target === '*' && g.level === 'admin')) {
    // A wildcard-admin is the super-user; show it plainly (other grants are subsumed).
    if (grants.length === 1) return 'all namespaces: admin (super-user)'
  }
  return grants
    .map((g) => `${g.target === '*' ? 'all' : g.target}: ${levelLabel(g.level)}`)
    .join(', ')
}
