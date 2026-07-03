import { wsClient } from './wsClient'

// LibrarianStatus is the ask_the_librarian health snapshot (B-73). It carries
// only config validity — provider, model, a kind/detail — NEVER the API key.
export interface LibrarianStatus {
  configured: boolean
  provider?: string
  model?: string
  // kind: ready | model_not_found | auth_failed | unreachable | misconfigured
  //       | unconfigured | unknown
  kind: string
  detail?: string
  checkedAt?: string
}

// librarianStatus reads the cached snapshot (a cheap read; does NOT make an LLM
// call). The server runs the real check at startup and on explicit refresh.
export function librarianStatus(): Promise<LibrarianStatus> {
  return wsClient().request('LIBRARIAN_STATUS', {})
}

// refreshLibrarianStatus re-runs the one-call health-check on the server and
// returns the fresh snapshot (one real, tiny API call per invocation).
export function refreshLibrarianStatus(): Promise<LibrarianStatus> {
  return wsClient().request('REFRESH_LIBRARIAN_STATUS', {})
}

// reloadLibrarianConfig re-reads the server's config FILE, connection-tests the
// new llm block, and on success swaps the live LLM client (a new model/provider)
// WITHOUT a restart — the returned snapshot reflects the new config. On failure
// the previous setting is kept and the snapshot carries the typed detail. Shoka
// never writes config: persistence is the operator's own edit to the YAML.
// Super-user only.
export function reloadLibrarianConfig(): Promise<LibrarianStatus> {
  return wsClient().request('RELOAD_LIBRARIAN_CONFIG', {})
}
