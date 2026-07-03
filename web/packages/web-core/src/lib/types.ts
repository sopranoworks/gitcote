// Shapes mirroring the Shoka /ws/ui request/response surface
// (internal/ui/manager.go). The Web UI reads all data over that WebSocket.

// Health state of a project, from GET_PROJECTS; drives the status badge.
export type ProjectState = 'healthy' | 'corrupted' | 'dangerous' | (string & {})

// One project entry in the GET_PROJECTS response.
export interface ProjectInfo {
  namespace: string
  name: string
  state: ProjectState
}

// The RECOVER_PROJECT (RECOVER_ACK) response: a project's health after re-syncing
// its write-path baseline to the on-disk git HEAD. `recovered` is true iff it is now
// healthy and writable; otherwise `message` explains why (genuine drift / dangerous).
export interface RecoverAck {
  namespace: string
  project: string
  state: ProjectState
  recovered: boolean
  message: string
}

// A file-tree node in the GET_TREE response (mirrors Go's ui.FileNode).
// Directories carry children; files do not.
export interface FileNode {
  name: string
  path: string
  isDir: boolean
  modifiedAt?: string
  children?: FileNode[]
}

// The READ_FILE response payload. `etag` is the sha256 of the content
// (added by the ws-ui-versioning precursor); the editor uses it as the
// optimistic-concurrency `if_match` on the next SAVE_FILE.
export interface FileContent {
  path: string
  content: string
  etag: string
}

// The SAVE_ACK response payload: the new etag becomes the buffer's next
// if_match. The CONFLICT payload carries the server's current etag so a
// "Force overwrite" can save against it.
export interface SaveAck {
  path: string
  status: string
  etag: string
}
export interface ConflictPayload {
  path: string
  current_etag: string
  message: string
}

// The MOVE_ACK response payload (mirrors Go's ui.MoveAckPayload). A move is a
// pure path change (B-33): new_etag is the moved file's etag (equal to the
// source's, since content is unchanged), and links_rewritten is ALWAYS 0 — the
// field is retained for forward compatibility but the UI never surfaces it.
export interface MoveAck {
  source_path: string
  target_path: string
  new_etag: string
  links_rewritten: number
}

// The DELETE_ACK response payload (mirrors Go's ui.DeleteAckPayload): the path
// that was deleted, so the client drops it from its caches/tree and clears the
// trash item. A stale if_match comes back as a CONFLICT frame instead (the file
// changed during the client-side grace), never a DELETE_ACK.
export interface DeleteAck {
  path: string
}

// One SEARCH_RESULT match (mirrors Go's storage.SearchMatch). The snippet is a
// short context window around the match, not a line; the backend does not carry
// a line number, so the result UI shows path + snippet and navigates to the
// blob view (no scroll-to-line). Search is project-scoped.
export interface SearchMatch {
  path: string
  snippet?: string
}

// One live OAuth/MCP connection in the OAUTH_LIST response (mirrors Go's
// ui.OAuthConnectionInfo). NO SECRETS: this carries the connecting client's
// identity (client_id = its CIMD metadata URL), the bound principal, the times,
// and the series id (the revoke target + a short prefix for display) — never an
// access/refresh token, code, or PKCE value. Times are RFC3339 strings.
export interface OAuthConnection {
  series_id: string
  series_id_short: string
  name?: string
  client_id: string
  principal_name: string
  principal_email: string
  issued_at: string
  access_expiry: string
  // The token's authorization grant (the 2026-06-15 authz foundation): "*" =
  // all-access (every DCR/self-issued token today), or a namespace grant for a
  // future pre-issued scoped token. Non-secret; shown in the connections table.
  scope: string
  // The trusted-"domain" entry this connection groups under (B-71 Stage 2d) — the
  // matched entry's identifier; "" for the operator self-issued / confidential /
  // untrusted-leftover section. Non-secret.
  domain: string
}

// The OAUTH_LIST response payload (mirrors Go's ui.OAuthListPayload). The slice
// is always present (possibly empty — the management view's empty state).
export interface OAuthListPayload {
  connections: OAuthConnection[]
}

// DomainInfo is the no-secret view of a "domain" RegistrationEntry (B-71 Stage 2d):
// the trusted-domain identifier, its per-domain access/refresh TTL in seconds (0 =
// unset → the finite global default), and whether a per-domain consent is SET. The
// consent VALUE/hash is NEVER sent — only the set/unset indicator (Stage 0/2b).
export interface DomainInfo {
  id: string
  domain: string
  access_ttl_seconds: number
  refresh_ttl_seconds: number
  // The per-domain consent VALUE (PLAINTEXT, operator-readable — the 2026-06-20 model). "" means no
  // consent is set, so the domain cannot authorize connections until the operator generates one.
  consent: string
  scope: string
  revoked_tokens?: number
}

// The DOMAIN_LIST response payload (mirrors Go's ui.DomainListPayload).
export interface DomainListPayload {
  domains: DomainInfo[]
}

// The DOMAIN_GENERATE_CONSENT response payload (mirrors Go's ui.DomainGenerateConsentPayload): the
// freshly minted plaintext consent value for a domain (re-rolled on each call).
export interface DomainGenerateConsentPayload {
  id: string
  consent: string
}

// ConfidentialClientInfo is the no-secret view of a "confidential" RegistrationEntry (B-71 Stage
// 3): the issued client_id, its pre-issued scope, its finite credential expiry (RFC3339), and
// creation time. The secret is NEVER sent here — only the issue response carries it, once.
export interface ConfidentialClientInfo {
  id: string
  client_id: string
  name?: string
  scope: string
  expires_at: string
  created_at: string
}

// The CLIENT_LIST response payload (mirrors Go's ui.ConfidentialListPayload).
export interface ConfidentialListPayload {
  clients: ConfidentialClientInfo[]
}

// The CLIENT_ISSUE response payload (mirrors Go's ui.ConfidentialIssuePayload): the no-secret info
// PLUS the raw client_secret — shown ONCE at issuance, never persisted or returned again. The
// client_id + client_secret are what the operator pastes into Claude.ai's advanced settings.
export interface ConfidentialIssuePayload extends ConfidentialClientInfo {
  client_secret: string
}

// The OAUTH_ISSUE_SELF response payload (mirrors Go's ui.OAuthIssueSelfPayload):
// the freshly minted access token for the operator and its expiry. THIS IS THE ONE
// SECRET that crosses /ws/ui — shown once so the operator can copy it into their
// CLI client config; it is never stored client-side beyond the transient display.
export interface OAuthIssueSelfPayload {
  access_token: string
  access_expiry: string
  name?: string
  scope: string
}

// The OAUTH_DENIED frame (mirrors Go's ui.OAuthDeniedPayload): a typed refusal of
// an admin-only OAuth request. reason is "forbidden" (caller is not an
// administrator) or "oauth_disabled" (OAuth is off on this server). Distinct from
// a generic ERROR so the client can recognise an authorization refusal.
export interface OAuthDenied {
  reason: 'forbidden' | 'oauth_disabled' | (string & {})
  message: string
}

// One commit row in the GET_HISTORY response (mirrors Go's ui.HistoryCommit).
// The summary is subject + commit date + committer ONLY — Shoka commits one file
// per commit, so there is no changed-file list. commitDate is an RFC3339 string.
export interface HistoryCommit {
  hash: string
  subject: string
  committer: string
  commitDate: string
}

// The GET_HISTORY response payload (mirrors Go's ui.HistoryPayload).
export interface HistoryPayload {
  commits: HistoryCommit[]
}

// The GET_FILE_AT response payload (mirrors Go's ui.FileAtPayload): one version's
// content at an explicit commit.
export interface FileAtContent {
  path: string
  hash: string
  content: string
}

// One line of a diff hunk (mirrors Go's storage.DiffLine). text carries no
// trailing newline.
export interface DiffLine {
  op: 'equal' | 'add' | 'delete'
  text: string
}

// One contiguous run of changed lines with context (mirrors Go's
// storage.DiffHunk). A pure-add hunk has oldStart/oldLines 0; a pure-delete hunk
// has newStart/newLines 0.
export interface DiffHunk {
  oldStart: number
  oldLines: number
  newStart: number
  newLines: number
  lines: DiffLine[]
}

// The GET_DIFF response payload (mirrors Go's storage.FileDiff). When a cap is
// hit the diff is omitted (hunks empty/absent) and the reason is in `suppressed`
// — the UI shows a banner rather than implying an empty diff.
export interface FileDiff {
  path: string
  fromHash: string
  toHash: string
  status: 'modified' | 'added' | 'deleted' | (string & {})
  binary: boolean
  suppressed?: '' | 'binary' | 'too_large' | 'timeout' | (string & {})
  hunks?: DiffHunk[]
}

// One server network endpoint in the SERVER_NETWORK_INFO response.
export interface NetworkElement {
  label: string
  protocol: string
  listen_address: string
  external_url?: string
  status: 'active' | 'disabled' | (string & {})
  description?: string
}

// The SERVER_NETWORK_INFO response payload.
export interface NetworkInfoPayload {
  elements: NetworkElement[]
}

// react-arborist node, derived from FileNode via lib/tree.
export interface TreeNode {
  id: string // unique within a project; for files this is the file path
  name: string // leaf label (last path segment)
  path: string // full slash-joined path (directories included)
  isFile: boolean
  modifiedAt?: string
  children?: TreeNode[]
}
