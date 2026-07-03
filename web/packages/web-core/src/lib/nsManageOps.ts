import { wsClient } from './wsClient'

// Namespace/project management ops over /ws/ui (B-28 part 2). All are gated server-side by
// the stage-2 dispatch authz (namespace ops = super-user; project ops = admin on the
// target namespace), so a forbidden request's promise rejects with "permission denied".
// The health read is admin-filtered server-side (a super-user sees all namespaces; a
// namespace-admin sees only theirs), so the client renders whatever it is given.

// Per-project health: "healthy" | "corrupted" | "dangerous" | "missing".
export interface ProjectHealth {
  name: string
  state: string
}

// A directory Shoka does not manage; adoptable iff it is a valid (.git) project/namespace.
export interface ForeignDir {
  name: string
  adoptable: boolean
}

// A stray catalog/index/deleted-log .db whose project dir is gone. files are the actual
// on-disk filenames (e.g. ["shoka.db"]) so the row can show the FULL filename, not the bare
// base (which collided confusingly with a namespace name).
export interface OrphanSibling {
  name: string
  files?: string[]
}

export interface NamespaceHealth {
  name: string
  present: boolean
  healthy: boolean
  projects: ProjectHealth[]
  foreign?: ForeignDir[]
  orphaned?: OrphanSibling[]
}

export interface HealthReport {
  namespaces: NamespaceHealth[]
  foreign_namespaces?: ForeignDir[]
}

// The admin-filtered managed-namespace health picture (stage B).
export function namespaceHealth(): Promise<HealthReport> {
  return wsClient().request('NAMESPACE_HEALTH', {})
}

// Add (super-user) / inspect: an explicit empty namespace.
export function createNamespace(name: string): Promise<{ status: string }> {
  return wsClient().request('CREATE_NAMESPACE', { namespace: name })
}

// Delete a namespace — SUPER-USER ONLY and only when EMPTY (the server refuses a
// non-empty namespace; the UI also disables the control until it is empty).
export function deleteNamespace(name: string): Promise<{ status: string }> {
  return wsClient().request('DELETE_NAMESPACE', { namespace: name })
}

// Add a project (admin on the target namespace).
export function createProject(namespace: string, name: string): Promise<{ status: string }> {
  return wsClient().request('CREATE_PROJECT', { namespace, projectName: name })
}

// Delete a single project (admin on the target namespace), one at a time.
export function deleteProject(namespace: string, name: string): Promise<{ status: string }> {
  return wsClient().request('DELETE_PROJECT', { namespace, projectName: name })
}

// Move a project to another namespace (B-28 project move) — super-user only. The target
// namespace must already exist; the server refuses a name collision. Distinct from delete:
// nothing is destroyed.
export function moveProject(
  namespace: string,
  projectName: string,
  newNamespace: string,
): Promise<{ status: string }> {
  return wsClient().request('MOVE_PROJECT', { namespace, projectName, newNamespace })
}

// Rename a project within its namespace (B-28 ns/proj rename) — admin on the namespace. The
// server refuses a name collision. Distinct from move (which changes the namespace) and
// delete (which destroys).
export function renameProject(
  namespace: string,
  projectName: string,
  newProjectName: string,
): Promise<{ status: string }> {
  return wsClient().request('RENAME_PROJECT', { namespace, projectName, newProjectName })
}

// Rename (relabel) a whole namespace (B-28 ns/proj rename) — super-user only. Carries every
// project + re-homes every grant. Allowed even when non-empty (a relabel, not a delete); the
// server refuses a collision and refuses renaming the `default` namespace.
export function renameNamespace(namespace: string, newNamespace: string): Promise<{ status: string }> {
  return wsClient().request('RENAME_NAMESPACE', { namespace, newNamespace })
}

// The stage-B per-divergence recovery actions. Whole-namespace actions (empty projectName)
// are super-user only; project-level actions need admin on the namespace.
export type RecoverAction = 'drop_missing' | 'clean_orphaned' | 'adopt'

export function namespaceRecover(
  action: RecoverAction,
  namespace: string,
  projectName = '',
): Promise<{ status: string }> {
  return wsClient().request('NAMESPACE_RECOVER', { action, namespace, projectName })
}
