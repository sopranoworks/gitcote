import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useIsAdmin } from '../lib/admin'
import {
  useConnectionsQuery,
  useDomainsQuery,
  useConfidentialClientsQuery,
  useProjectsQuery,
  OAUTH_CONNECTIONS_KEY,
  OAUTH_DOMAINS_KEY,
  OAUTH_CLIENTS_KEY,
} from '../lib/queries'
import {
  revokeConnection,
  issueSelfToken,
  clientDomain,
  OAuthDeniedError,
} from '../lib/oauthOps'
import {
  createDomain,
  updateDomain,
  deleteDomain,
  generateDomainConsent,
} from '../lib/domainOps'
import {
  issueConfidentialClient,
  revokeConfidentialClient,
} from '../lib/confidentialOps'
import { useToast } from '../lib/toast'
import type {
  OAuthConnection,
  OAuthIssueSelfPayload,
  DomainInfo,
  ConfidentialClientInfo,
  ConfidentialIssuePayload,
} from '../lib/types'
import { ScopeInput, validateScopeExistence, ScopeConfirmDialog, type ScopeIssue } from '../components/ScopeInput'
import dialogStyles from '../components/ConfirmDialog.module.css'
import styles from './ConnectionsPage.module.css'

// The administrator-only OAuth management view. Per the B-71 binding principle the screen is
// structured BY management mode; this is DOMAIN mode (B-71 Stage 2d): the operator manages
// trusted domains + their per-domain TTL + per-domain consent FROM THE SCREEN (the settings
// that used to live in static config — moved to the UI, not removed), and each domain's
// tokens group beneath its entry. The per-domain consent is WRITE-ONLY: it is set or cleared,
// never displayed (hashed at rest). Self-issued / confidential tokens sit in a separate
// section. The server-side gate is authoritative; useIsAdmin() governs UI exposure only.
export function ConnectionsPage() {
  const isAdmin = useIsAdmin()
  const {
    data: connections,
    isLoading: connLoading,
    error,
  } = useConnectionsQuery(isAdmin)
  const { data: domains, isLoading: domLoading } =
    useDomainsQuery(isAdmin)

  if (!isAdmin) {
    return (
      <div className={styles.page}>
        <div className={styles.body}>
          <p className={styles.hint}>
            You are not authorized to manage OAuth connections.
          </p>
        </div>
      </div>
    )
  }

  const oauthDisabled =
    error instanceof OAuthDeniedError && error.reason === 'oauth_disabled'

  return (
    <div className={styles.page}>
      <div className={styles.toolbar}>
        <span className={styles.scope}>
          <strong>OAuth connections</strong>
          <span className={styles.sub}> · trusted domains &amp; active authorizations</span>
        </span>
      </div>

      <div className={styles.body} data-scroll-restoration-id="connections-body">
        {connLoading || domLoading ? (
          <p className={styles.hint}>Loading…</p>
        ) : oauthDisabled ? (
          <p className={styles.hint}>
            OAuth is not enabled on this server, so there is nothing to manage.
          </p>
        ) : error ? (
          <p className={styles.error}>Failed to load connections.</p>
        ) : (
          <>
            <DomainManagement
              domains={domains ?? []}
              connections={connections ?? []}
              oauthDisabled={oauthDisabled}
            />
            <ConfidentialManagement />
          </>
        )}
      </div>
    </div>
  )
}

// DomainManagement is the domain-mode screen body: the Add-domain form, the list of trusted
// "domain" entries (each with its grouped tokens), and the separate self-issued/other section.
function DomainManagement({
  domains,
  connections,
  oauthDisabled,
}: {
  domains: DomainInfo[]
  connections: OAuthConnection[]
  oauthDisabled: boolean
}) {
  const [issued, setIssued] = useState<OAuthIssueSelfPayload | null>(null)
  const grouped = new Map<string, OAuthConnection[]>()
  const orphans: OAuthConnection[] = [] // domain === "" (self-issued/confidential)
  for (const c of connections) {
    if (!c.domain) {
      orphans.push(c)
      continue
    }
    const arr = grouped.get(c.domain) ?? []
    arr.push(c)
    grouped.set(c.domain, arr)
  }

  return (
    <>
      <section className={styles.domainsSection}>
        <h3 className={styles.sectionTitle}>Trusted domains</h3>
        <AddDomainForm />
        {domains.length === 0 ? (
          <p className={styles.hint} data-testid="domains-empty">
            No trusted domains configured. Add one to allow connections from it.
          </p>
        ) : (
          domains.map((d) => (
            <DomainCard key={d.id} domain={d} tokens={grouped.get(d.domain) ?? []} />
          ))
        )}
      </section>

      <section className={styles.selfSection} data-testid="self-issued-section">
        <h3 className={styles.sectionTitle}>Self-issued &amp; other tokens</h3>
        <IssueTokenButton disabled={oauthDisabled} onIssued={setIssued} />
        {issued && (
          <IssuedTokenPanel token={issued} onDismiss={() => setIssued(null)} />
        )}
        {orphans.length === 0 ? (
          <p className={styles.hint}>No self-issued tokens.</p>
        ) : (
          <TokensTable tokens={orphans} />
        )}
      </section>
    </>
  )
}

// ConfidentialManagement is the confidential-mode screen (B-71 Stage 3), composed alongside the
// domain-mode screen: the operator pre-issues a Client ID + Secret (with a scope + a finite
// expiry) for Claude.ai's custom-connector advanced settings. The secret is shown ONCE at
// issuance (never retrievable after); the list never carries it; revoke cascades to its tokens.
function ConfidentialManagement() {
  const { data: clients, isLoading } = useConfidentialClientsQuery(true)
  const [issued, setIssued] = useState<ConfidentialIssuePayload | null>(null)

  return (
    <section className={styles.domainsSection} data-testid="confidential-section">
      <h3 className={styles.sectionTitle}>Confidential clients (Client ID + Secret)</h3>
      <p className={styles.hintSmall}>
        Pre-issue a Client ID + Secret for a Claude.ai custom connector. The secret is shown once
        — copy it now. PKCE is still required in addition to the secret.
      </p>
      <IssueClientForm onIssued={setIssued} />
      {issued && (
        <IssuedClientPanel issued={issued} onDismiss={() => setIssued(null)} />
      )}
      {isLoading ? (
        <p className={styles.hint}>Loading…</p>
      ) : (clients ?? []).length === 0 ? (
        <p className={styles.hint} data-testid="confidential-empty">
          No confidential clients issued.
        </p>
      ) : (
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Client ID</th>
              <th>Name</th>
              <th>Scope</th>
              <th>Expires</th>
              <th>Issued</th>
              <th aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {(clients ?? []).map((c) => (
              <ConfidentialClientRow key={c.id} client={c} />
            ))}
          </tbody>
        </table>
      )}
    </section>
  )
}

// parseValidityDays: a confidential credential's validity in whole positive days (no indefinite).
// Returns null for a non-finite / non-positive / non-integer value.
function parseValidityDays(v: string): number | null {
  const n = Number(v)
  if (!Number.isInteger(n) || n <= 0) return null
  return n
}

function IssueClientForm({
  onIssued,
}: {
  onIssued: (p: ConfidentialIssuePayload) => void
}) {
  const queryClient = useQueryClient()
  const { add: addToast } = useToast()
  const { data: projects = [] } = useProjectsQuery()
  const [name, setName] = useState('')
  const [scope, setScope] = useState('')
  const [days, setDays] = useState('30')
  const [scopeIssues, setScopeIssues] = useState<ScopeIssue[] | null>(null)

  const validDays = parseValidityDays(days)
  const valid = scope.trim() !== '' && validDays !== null

  const issue = useMutation({
    mutationFn: () =>
      issueConfidentialClient({
        scope: scope.trim(),
        name: name.trim() || undefined,
        validitySeconds: (validDays ?? 0) * 86400,
      }),
    onSuccess: (p) => {
      void queryClient.invalidateQueries({ queryKey: OAUTH_CLIENTS_KEY })
      onIssued(p)
      setName('')
      setScope('')
      setDays('30')
    },
    onError: (e) =>
      addToast({
        level: 'warn',
        text: e instanceof Error ? e.message : 'Failed to issue a confidential client.',
      }),
  })

  return (
    <>
      <form
        className={styles.domainForm}
        onSubmit={(e) => {
          e.preventDefault()
          if (!valid) return
          const issues = validateScopeExistence(scope.trim(), projects)
          if (issues.length > 0) { setScopeIssues(issues); return }
          issue.mutate()
        }}
      >
        <label className={styles.field}>
          <span className={styles.fieldLabel}>Name (optional)</span>
          <input
            className={styles.domainInput}
            placeholder="e.g. production connector"
            value={name}
            onChange={(e) => setName(e.target.value)}
            data-testid="client-issue-name"
          />
        </label>
        <label className={styles.field}>
          <span className={styles.fieldLabel}>Scope</span>
          <ScopeInput
            className={styles.domainInput}
            placeholder="e.g. test:prtest:rw, or * for all access"
            value={scope}
            onChange={setScope}
            data-testid="client-issue-scope"
          />
        </label>
        <label className={styles.field}>
          <span className={styles.fieldLabel}>TTL (days)</span>
          <input
            className={styles.ttlInput}
            placeholder="30"
            value={days}
            onChange={(e) => setDays(e.target.value)}
            data-testid="client-issue-validity"
            aria-invalid={validDays === null}
          />
        </label>
        <button
          type="submit"
          className={styles.issue}
          disabled={!valid || issue.isPending}
          data-testid="client-issue-submit"
        >
          {issue.isPending ? 'Issuing…' : 'Issue client'}
        </button>
        {!valid && (scope !== '' || days !== '30') && (
          <span className={styles.invalid}>
            A scope is required; validity must be a whole positive number of days (no indefinite).
          </span>
        )}
      </form>
      <ScopeConfirmDialog
        open={scopeIssues !== null && scopeIssues.length > 0}
        issues={scopeIssues ?? []}
        onConfirm={() => { setScopeIssues(null); issue.mutate() }}
        onCancel={() => setScopeIssues(null)}
      />
    </>
  )
}

// IssuedClientPanel shows the freshly minted client_id + secret ONCE, with copy controls and a
// clear "shown only once" caption — the one place the raw secret crosses /ws/ui.
function IssuedClientPanel({
  issued,
  onDismiss,
}: {
  issued: ConfidentialIssuePayload
  onDismiss: () => void
}) {
  const { add: addToast } = useToast()
  return (
    <div className={styles.tokenPanel} role="status" data-testid="client-issued-panel">
      <div className={styles.tokenWarn}>
        <strong>Copy the secret now.</strong> It is shown only once and cannot be retrieved
        again. Paste the Client ID and Secret into Claude.ai&apos;s connector advanced settings.
      </div>
      <div className={styles.tokenRow}>
        <code className={styles.tokenValue} data-testid="client-issued-id">
          {issued.client_id}
        </code>
        <CopyButton value={issued.client_id} label="Copy ID" ariaLabel="Copy client id" toast={addToast} />
      </div>
      <div className={styles.tokenRow}>
        <code className={styles.tokenValue} data-testid="client-issued-secret">
          {issued.client_secret}
        </code>
        <CopyButton value={issued.client_secret} label="Copy secret" ariaLabel="Copy client secret" toast={addToast} />
        <button className={styles.dismiss} onClick={onDismiss} aria-label="Dismiss">
          Done
        </button>
      </div>
      <div className={styles.tokenExpiry}>
        {issued.name && <>{issued.name} · </>}
        Scope {fmtScope(issued.scope)} · expires {fmtTime(issued.expires_at)}
      </div>
    </div>
  )
}

function ConfidentialClientRow({ client }: { client: ConfidentialClientInfo }) {
  const queryClient = useQueryClient()
  const { add: addToast } = useToast()
  const [confirming, setConfirming] = useState(false)

  const revoke = useMutation({
    mutationFn: () => revokeConfidentialClient(client.id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: OAUTH_CLIENTS_KEY })
      void queryClient.invalidateQueries({ queryKey: OAUTH_CONNECTIONS_KEY })
      addToast({ level: 'warn', text: 'Revoked the confidential client (its tokens were cut).' })
    },
    onError: (e) => {
      addToast({
        level: 'warn',
        text: e instanceof Error ? e.message : 'Failed to revoke the client.',
      })
      setConfirming(false)
    },
  })

  return (
    <tr data-testid={`client-row-${client.client_id}`}>
      <td className={styles.client}>
        <code className={styles.series}>{client.client_id}</code>
      </td>
      <td className={styles.time}>{client.name || '—'}</td>
      <td className={styles.scopeCell}>{fmtScope(client.scope)}</td>
      <td className={styles.time}>{fmtTime(client.expires_at)}</td>
      <td className={styles.time}>{fmtTime(client.created_at)}</td>
      <td className={styles.actions}>
        {confirming ? (
          <>
            <button
              className={styles.danger}
              disabled={revoke.isPending}
              onClick={() => revoke.mutate()}
              data-testid={`client-revoke-confirm-${client.client_id}`}
            >
              {revoke.isPending ? 'Revoking…' : 'Confirm revoke'}
            </button>
            <button className={styles.cancel} onClick={() => setConfirming(false)}>
              Cancel
            </button>
          </>
        ) : (
          <button
            className={styles.revoke}
            onClick={() => setConfirming(true)}
            data-testid={`client-revoke-${client.client_id}`}
          >
            Revoke
          </button>
        )}
      </td>
    </tr>
  )
}

// validTtl: a per-domain TTL is finite and non-negative (0 = unset → the finite global
// default; no indefinite). A blank field is 0 (use the default).
function parseTtl(v: string): number | null {
  if (v.trim() === '') return 0
  const n = Number(v)
  if (!Number.isInteger(n) || n < 0) return null
  return n
}

function AddDomainForm() {
  const queryClient = useQueryClient()
  const { add: addToast } = useToast()
  const [domain, setDomain] = useState('')
  const [accessTtl, setAccessTtl] = useState('')
  const [refreshTtl, setRefreshTtl] = useState('')
  const [scope, setScope] = useState('*')

  const access = parseTtl(accessTtl)
  const refresh = parseTtl(refreshTtl)
  const valid = domain.trim() !== '' && access !== null && refresh !== null && scope.trim() !== ''

  const create = useMutation({
    mutationFn: () =>
      createDomain({
        domain: domain.trim(),
        accessTtlSeconds: access ?? 0,
        refreshTtlSeconds: refresh ?? 0,
        scope: scope.trim(),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: OAUTH_DOMAINS_KEY })
      addToast({ level: 'warn', text: `Added trusted domain ${domain.trim()}.` })
      setDomain('')
      setAccessTtl('')
      setRefreshTtl('')
      setScope('*')
    },
    onError: (e) => {
      addToast({
        level: 'warn',
        text: e instanceof Error ? e.message : 'Failed to add domain.',
      })
    },
  })

  return (
    <form
      className={styles.domainForm}
      onSubmit={(e) => {
        e.preventDefault()
        if (valid) create.mutate()
      }}
    >
      <label className={styles.field}>
        <span className={styles.fieldLabel}>Trusted domain</span>
        <input
          className={styles.domainInput}
          placeholder="e.g. connector.example.com"
          value={domain}
          onChange={(e) => setDomain(e.target.value)}
          data-testid="domain-add-identifier"
        />
      </label>
      <label className={styles.field}>
        <span className={styles.fieldLabel}>Scope</span>
        <ScopeInput
          className={styles.domainInput}
          value={scope}
          onChange={setScope}
          placeholder="* (all access)"
          data-testid="domain-add-scope"
        />
      </label>
      <label className={styles.field}>
        <span className={styles.fieldLabel}>Access TTL (s)</span>
        <input
          className={styles.ttlInput}
          placeholder="0 = default"
          value={accessTtl}
          onChange={(e) => setAccessTtl(e.target.value)}
          data-testid="domain-add-access-ttl"
          aria-invalid={access === null}
        />
      </label>
      <label className={styles.field}>
        <span className={styles.fieldLabel}>Refresh TTL (s)</span>
        <input
          className={styles.ttlInput}
          placeholder="0 = default"
          value={refreshTtl}
          onChange={(e) => setRefreshTtl(e.target.value)}
          data-testid="domain-add-refresh-ttl"
          aria-invalid={refresh === null}
        />
      </label>
      <button
        type="submit"
        className={styles.issue}
        disabled={!valid || create.isPending}
        data-testid="domain-add-submit"
      >
        {create.isPending ? 'Adding…' : 'Add domain'}
      </button>
      {!valid && (domain !== '' || accessTtl !== '' || refreshTtl !== '') && (
        <span className={styles.invalid}>
          A domain is required; TTLs must be whole non-negative seconds (0 = default).
        </span>
      )}
    </form>
  )
}

function fmtTtl(seconds: number): string {
  if (seconds <= 0) return 'default'
  if (seconds % 86400 === 0) return `${seconds / 86400}d`
  if (seconds % 3600 === 0) return `${seconds / 3600}h`
  if (seconds % 60 === 0) return `${seconds / 60}m`
  return `${seconds}s`
}

function DomainCard({ domain, tokens }: { domain: DomainInfo; tokens: OAuthConnection[] }) {
  const queryClient = useQueryClient()
  const { add: addToast } = useToast()
  const [editing, setEditing] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const del = useMutation({
    mutationFn: () => deleteDomain(domain.id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: OAUTH_DOMAINS_KEY })
      void queryClient.invalidateQueries({ queryKey: OAUTH_CONNECTIONS_KEY })
      addToast({ level: 'warn', text: `Deleted ${domain.domain} (its tokens were revoked).` })
    },
    onError: (e) =>
      addToast({ level: 'warn', text: e instanceof Error ? e.message : 'Delete failed.' }),
  })

  const generate = useMutation({
    mutationFn: () => generateDomainConsent(domain.id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: OAUTH_DOMAINS_KEY })
      addToast({ level: 'warn', text: `Generated a consent value for ${domain.domain}.` })
    },
    onError: (e) =>
      addToast({ level: 'warn', text: e instanceof Error ? e.message : 'Generate failed.' }),
  })

  const copyConsent = () => {
    copyToClipboard(domain.consent, addToast, 'Consent value')
  }

  return (
    <div className={styles.domainCard} data-testid={`domain-row-${domain.domain}`}>
      <div className={styles.domainHeader}>
        <span className={styles.domainName}>{domain.domain}</span>
        <span className={styles.domainTtl}>
          access {fmtTtl(domain.access_ttl_seconds)} · refresh{' '}
          {fmtTtl(domain.refresh_ttl_seconds)} · scope{' '}
          <code data-testid={`domain-scope-${domain.domain}`}>{domain.scope || '*'}</code>
        </span>
        <span className={styles.domainActions}>
          <button
            className={styles.cancel}
            onClick={() => setEditing((v) => !v)}
            data-testid={`domain-edit-${domain.domain}`}
          >
            {editing ? 'Close' : 'Edit'}
          </button>
          {confirmDelete ? (
            <>
              <button
                className={styles.danger}
                disabled={del.isPending}
                onClick={() => del.mutate()}
                data-testid={`domain-delete-confirm-${domain.domain}`}
              >
                {del.isPending ? 'Deleting…' : 'Confirm delete'}
              </button>
              <button className={styles.cancel} onClick={() => setConfirmDelete(false)}>
                Cancel
              </button>
            </>
          ) : (
            <button
              className={styles.revoke}
              onClick={() => setConfirmDelete(true)}
              data-testid={`domain-delete-${domain.domain}`}
            >
              Delete
            </button>
          )}
        </span>
      </div>

      {/* Per-domain consent — PLAINTEXT, always visible, server-generated, refreshable
          (2026-06-20). The connecting party types this value on the /authorize consent page. */}
      <div className={styles.consentRow} data-testid={`domain-consent-${domain.domain}`}>
        {domain.consent === '' ? (
          <>
            <span className={styles.consentUnset}>
              consent: none — clients cannot connect until you generate a consent value
            </span>
            <button
              className={styles.issue}
              disabled={generate.isPending}
              onClick={() => generate.mutate()}
              data-testid={`domain-consent-generate-${domain.domain}`}
            >
              {generate.isPending ? 'Generating…' : 'Generate consent'}
            </button>
          </>
        ) : (
          <>
            <span className={styles.consentLabel}>consent:</span>
            <code
              className={styles.consentValue}
              data-testid={`domain-consent-value-${domain.domain}`}
            >
              {domain.consent}
            </code>
            <button
              className={styles.copy}
              onClick={copyConsent}
              aria-label="Copy consent value"
              data-testid={`domain-consent-copy-${domain.domain}`}
            >
              Copy
            </button>
            <button
              className={styles.cancel}
              disabled={generate.isPending}
              onClick={() => generate.mutate()}
              data-testid={`domain-consent-generate-${domain.domain}`}
            >
              {generate.isPending ? 'Regenerating…' : 'Regenerate'}
            </button>
          </>
        )}
      </div>

      {editing && <EditDomainForm domain={domain} onDone={() => setEditing(false)} />}

      <div className={styles.domainTokens} data-testid={`domain-tokens-${domain.domain}`}>
        {tokens.length === 0 ? (
          <p className={styles.hintSmall}>No active tokens for this domain.</p>
        ) : (
          <TokensTable tokens={tokens} />
        )}
      </div>
    </div>
  )
}

function EditDomainForm({ domain, onDone }: { domain: DomainInfo; onDone: () => void }) {
  const queryClient = useQueryClient()
  const { add: addToast } = useToast()
  const [accessTtl, setAccessTtl] = useState(String(domain.access_ttl_seconds || ''))
  const [refreshTtl, setRefreshTtl] = useState(String(domain.refresh_ttl_seconds || ''))
  const [scope, setScope] = useState(domain.scope || '*')
  const [showScopeModal, setShowScopeModal] = useState(false)

  const access = parseTtl(accessTtl)
  const refresh = parseTtl(refreshTtl)
  const valid = access !== null && refresh !== null && scope.trim() !== ''
  const scopeChanged = scope.trim() !== (domain.scope || '*')

  const save = useMutation({
    mutationFn: () =>
      updateDomain({
        id: domain.id,
        accessTtlSeconds: access ?? 0,
        refreshTtlSeconds: refresh ?? 0,
        scope: scope.trim(),
      }),
    onSuccess: (result) => {
      void queryClient.invalidateQueries({ queryKey: OAUTH_DOMAINS_KEY })
      void queryClient.invalidateQueries({ queryKey: OAUTH_CONNECTIONS_KEY })
      const msg =
        result.revoked_tokens && result.revoked_tokens > 0
          ? `Updated ${domain.domain} — ${result.revoked_tokens} token(s) revoked.`
          : `Updated ${domain.domain}.`
      addToast({ level: 'warn', text: msg })
      onDone()
    },
    onError: (e) =>
      addToast({ level: 'warn', text: e instanceof Error ? e.message : 'Update failed.' }),
  })

  const handleSubmit = () => {
    if (!valid) return
    if (scopeChanged) {
      setShowScopeModal(true)
    } else {
      save.mutate()
    }
  }

  return (
    <>
      <form
        className={styles.domainForm}
        onSubmit={(e) => {
          e.preventDefault()
          handleSubmit()
        }}
      >
        <label className={styles.field}>
          <span className={styles.fieldLabel}>Scope</span>
          <ScopeInput
            className={styles.domainInput}
            value={scope}
            onChange={setScope}
            placeholder="* (all access)"
            data-testid={`domain-edit-scope-${domain.domain}`}
          />
        </label>
        <label className={styles.field}>
          <span className={styles.fieldLabel}>Access TTL (s)</span>
          <input
            className={styles.ttlInput}
            value={accessTtl}
            placeholder="0 = default"
            onChange={(e) => setAccessTtl(e.target.value)}
            data-testid={`domain-edit-access-ttl-${domain.domain}`}
            aria-invalid={access === null}
          />
        </label>
        <label className={styles.field}>
          <span className={styles.fieldLabel}>Refresh TTL (s)</span>
          <input
            className={styles.ttlInput}
            value={refreshTtl}
            placeholder="0 = default"
            onChange={(e) => setRefreshTtl(e.target.value)}
            data-testid={`domain-edit-refresh-ttl-${domain.domain}`}
            aria-invalid={refresh === null}
          />
        </label>
        <button
          type="submit"
          className={styles.issue}
          disabled={!valid || save.isPending}
          data-testid={`domain-edit-save-${domain.domain}`}
        >
          {save.isPending ? 'Saving…' : 'Save'}
        </button>
      </form>
      <ScopeChangeModal
        open={showScopeModal}
        domain={domain.domain}
        oldScope={domain.scope || '*'}
        newScope={scope.trim()}
        pending={save.isPending}
        onCancel={() => {
          setShowScopeModal(false)
          setScope(domain.scope || '*')
        }}
        onConfirm={() => {
          setShowScopeModal(false)
          save.mutate()
        }}
      />
    </>
  )
}

function ScopeChangeModal({
  open,
  domain,
  oldScope,
  newScope,
  pending,
  onCancel,
  onConfirm,
}: {
  open: boolean
  domain: string
  oldScope: string
  newScope: string
  pending: boolean
  onCancel: () => void
  onConfirm: () => void
}) {
  if (!open) return null
  return (
    <div className={dialogStyles.overlay} onClick={onCancel}>
      <div
        className={dialogStyles.dialog}
        role="dialog"
        aria-modal="true"
        aria-label="Scope change confirmation"
        onClick={(e) => e.stopPropagation()}
        data-testid="scope-change-modal"
      >
        <h2 className={dialogStyles.title}>
          Scope Change &mdash; Token Revocation Required
        </h2>
        <div className={dialogStyles.message}>
          Changing the scope for <strong>{domain}</strong> from{' '}
          <code>{oldScope}</code> to <code>{newScope}</code> will revoke all
          currently active tokens issued to this domain.
          <br />
          <br />
          Clients connecting from this domain will need to re-authenticate to
          receive new tokens with the updated scope.
        </div>
        <div className={dialogStyles.actions}>
          <button
            className={dialogStyles.cancel}
            onClick={onCancel}
            data-testid="scope-change-cancel"
          >
            Cancel
          </button>
          <button
            className={dialogStyles.danger}
            onClick={onConfirm}
            disabled={pending}
            data-testid="scope-change-confirm"
          >
            {pending ? 'Revoking…' : 'Revoke and Update'}
          </button>
        </div>
      </div>
    </div>
  )
}

function TokensTable({ tokens }: { tokens: OAuthConnection[] }) {
  return (
    <table className={styles.table}>
      <thead>
        <tr>
          <th>Client</th>
          <th>Name</th>
          <th>Principal</th>
          <th>Scope</th>
          <th>Issued</th>
          <th>Access expires</th>
          <th>Series</th>
          <th aria-label="Actions" />
        </tr>
      </thead>
      <tbody>
        {tokens.map((c) => (
          <ConnectionRow key={c.series_id} conn={c} />
        ))}
      </tbody>
    </table>
  )
}

function fmtTime(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

// Expiry status drives the row's attention colour (the 2026-06-15 admin-UI
// directive): surface problems, keep healthy rows quiet. Derived CLIENT-side from
// access_expiry so it stays live without a refetch. An unparseable time yields
// 'healthy' (never raise a false alarm on bad data).
const NEAR_EXPIRY_MS = 15 * 60 * 1000

type ExpiryStatus = 'expired' | 'near' | 'healthy'

export function expiryStatus(
  accessExpiry: string,
  now: number = Date.now(),
): ExpiryStatus {
  const exp = new Date(accessExpiry).getTime()
  if (Number.isNaN(exp)) return 'healthy'
  const left = exp - now
  if (left <= 0) return 'expired'
  if (left <= NEAR_EXPIRY_MS) return 'near'
  return 'healthy'
}

// fmtRelative renders a short, legible distance to/from the expiry instant:
// "expires in 8m" / "expired 2h ago". Returns '' for an unparseable time.
export function fmtRelative(accessExpiry: string, now: number = Date.now()): string {
  const exp = new Date(accessExpiry).getTime()
  if (Number.isNaN(exp)) return ''
  const diff = exp - now
  const abs = Math.abs(diff)
  let unit: string
  if (abs < 60_000) unit = '<1m'
  else if (abs < 3_600_000) unit = `${Math.round(abs / 60_000)}m`
  else if (abs < 86_400_000) unit = `${Math.round(abs / 3_600_000)}h`
  else unit = `${Math.round(abs / 86_400_000)}d`
  return diff < 0 ? `expired ${unit} ago` : `expires in ${unit}`
}

// fmtScope renders the token's authorization grant: "*" reads clearly as
// "all access"; a scoped value (e.g. "namespace:foo") is shown verbatim.
export function fmtScope(scope: string): string {
  return scope === '*' || scope === '' ? 'all access' : scope
}

// IssueTokenButton mints a "token to self" for the CLI (B-46b §2.2). The minted token is the one
// secret that crosses /ws/ui; on success it is handed up to the page (onIssued) for a display-once
// panel, never stored or toasted. B-71 Stage 4: the operator chooses a per-issuance FINITE expiry
// (GitHub-PAT-style, whole positive days — no indefinite option) via the SAME no-indefinite rule
// as confidential issuance (parseValidityDays); the chosen lifetime bounds the token.
function IssueTokenButton({
  disabled,
  onIssued,
}: {
  disabled: boolean
  onIssued: (t: OAuthIssueSelfPayload) => void
}) {
  const { add: addToast } = useToast()
  const { data: projects = [] } = useProjectsQuery()
  const [name, setName] = useState('')
  const [scope, setScope] = useState('')
  const [days, setDays] = useState('30')
  const [scopeIssues, setScopeIssues] = useState<ScopeIssue[] | null>(null)
  const validDays = parseValidityDays(days)
  const issue = useMutation({
    mutationFn: () => issueSelfToken((validDays ?? 0) * 86400, name.trim(), scope.trim()),
    onSuccess: (t) => {
      onIssued(t)
      setName('')
      setScope('')
      setDays('30')
    },
    onError: (e) =>
      addToast({
        level: 'warn',
        text:
          e instanceof OAuthDeniedError
            ? e.message
            : 'Failed to generate a CLI token.',
      }),
  })
  return (
    <>
      <form
        className={styles.domainForm}
        onSubmit={(e) => {
          e.preventDefault()
          if (validDays === null) return
          const issues = validateScopeExistence(scope.trim(), projects)
          if (issues.length > 0) { setScopeIssues(issues); return }
          issue.mutate()
        }}
      >
        <label className={styles.field}>
          <span className={styles.fieldLabel}>Name (optional)</span>
          <input
            className={styles.domainInput}
            placeholder="e.g. laptop CLI"
            value={name}
            onChange={(e) => setName(e.target.value)}
            disabled={disabled}
            data-testid="self-issue-name"
          />
        </label>
        <label className={styles.field}>
          <span className={styles.fieldLabel}>Scope</span>
          <ScopeInput
            className={styles.domainInput}
            placeholder="e.g. test:prtest:rw, or * for all access"
            value={scope}
            onChange={setScope}
            disabled={disabled}
            data-testid="self-issue-scope"
          />
        </label>
        <label className={styles.field}>
          <span className={styles.fieldLabel}>TTL (days)</span>
          <input
            className={styles.ttlInput}
            placeholder="30"
            value={days}
            onChange={(e) => setDays(e.target.value)}
            disabled={disabled}
            data-testid="self-issue-days"
            aria-invalid={validDays === null}
          />
        </label>
        <button
          type="submit"
          className={styles.issue}
          disabled={disabled || validDays === null || issue.isPending}
          aria-label="Generate a token for the CLI"
        >
          {issue.isPending ? 'Generating…' : 'Generate token'}
        </button>
      </form>
      <ScopeConfirmDialog
        open={scopeIssues !== null && scopeIssues.length > 0}
        issues={scopeIssues ?? []}
        onConfirm={() => { setScopeIssues(null); issue.mutate() }}
        onCancel={() => setScopeIssues(null)}
      />
    </>
  )
}

// IssuedTokenPanel shows the freshly minted token ONCE with a copy button and a
// warning that it will not be shown again.
function IssuedTokenPanel({
  token,
  onDismiss,
}: {
  token: OAuthIssueSelfPayload
  onDismiss: () => void
}) {
  const { add: addToast } = useToast()
  return (
    <div className={styles.tokenPanel} role="status">
      <div className={styles.tokenWarn}>
        <strong>Copy this token now.</strong> It is shown once. Paste it into your
        CLI config with <code>shoka-cli auth</code>.
      </div>
      <div className={styles.tokenRow}>
        <code className={styles.tokenValue}>{token.access_token}</code>
        <CopyButton value={token.access_token} label="Copy" ariaLabel="Copy token" toast={addToast} />
        <button
          className={styles.dismiss}
          onClick={onDismiss}
          aria-label="Dismiss token"
        >
          Done
        </button>
      </div>
      <div className={styles.tokenExpiry}>
        {token.name && <>{token.name} · </>}
        Scope {fmtScope(token.scope)} · expires {fmtTime(token.access_expiry)}
      </div>
    </div>
  )
}

type ToastFn = (t: { level: 'warn'; text: string }) => void

function copyToClipboard(value: string, toast: ToastFn, successLabel?: string) {
  if (navigator.clipboard) {
    void navigator.clipboard
      .writeText(value)
      .then(() => toast({ level: 'warn', text: successLabel ? `${successLabel} copied.` : 'Copied.' }))
      .catch(() => {
        selectFallback(value)
        toast({ level: 'warn', text: 'Selected — press Ctrl+C to copy.' })
      })
  } else {
    selectFallback(value)
    toast({ level: 'warn', text: 'Selected — press Ctrl+C to copy.' })
  }
}

function selectFallback(text: string) {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  document.body.removeChild(ta)
}

function CopyButton({ value, label, ariaLabel, toast }: { value: string; label: string; ariaLabel?: string; toast: ToastFn }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      className={styles.copy}
      onClick={() => {
        if (navigator.clipboard) {
          void navigator.clipboard
            .writeText(value)
            .then(() => setCopied(true))
            .catch(() => {
              selectFallback(value)
              toast({ level: 'warn', text: 'Selected — press Ctrl+C to copy.' })
            })
        } else {
          selectFallback(value)
          toast({ level: 'warn', text: 'Selected — press Ctrl+C to copy.' })
        }
      }}
      aria-label={ariaLabel ?? label}
    >
      {copied ? 'Copied' : label}
    </button>
  )
}

function ConnectionRow({ conn }: { conn: OAuthConnection }) {
  const queryClient = useQueryClient()
  const { add: addToast } = useToast()
  const [confirming, setConfirming] = useState(false)

  const revoke = useMutation({
    mutationFn: () => revokeConnection(conn.series_id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: OAUTH_CONNECTIONS_KEY })
      addToast({
        level: 'warn',
        text: `Revoked the connection for ${clientDomain(conn.client_id)}.`,
      })
    },
    onError: (e) => {
      const text =
        e instanceof OAuthDeniedError
          ? e.message
          : 'Failed to revoke the connection.'
      addToast({ level: 'warn', text })
      setConfirming(false)
    },
  })

  const busy = revoke.isPending
  const status = expiryStatus(conn.access_expiry)

  return (
    <tr data-testid={`token-row-${conn.series_id_short}`}>
      <td className={styles.client} title={conn.client_id}>
        {clientDomain(conn.client_id)}
      </td>
      <td className={styles.time}>{conn.name || '—'}</td>
      <td className={styles.principal}>
        <span className={styles.principalName}>
          {conn.principal_name || '—'}
        </span>
        {conn.principal_email && (
          <span className={styles.principalEmail}>{conn.principal_email}</span>
        )}
      </td>
      <td className={styles.scopeCell} title={conn.scope}>
        {fmtScope(conn.scope)}
      </td>
      <td className={styles.time}>{fmtTime(conn.issued_at)}</td>
      <td className={styles.time} data-status={status}>
        <div className={styles.expiryCell}>
          <span>{fmtTime(conn.access_expiry)}</span>
          {status !== 'healthy' && (
            <span
              className={
                status === 'expired' ? styles.badgeExpired : styles.badgeNear
              }
            >
              {fmtRelative(conn.access_expiry)}
            </span>
          )}
        </div>
      </td>
      <td className={styles.series}>
        <code>{conn.series_id_short}</code>
      </td>
      <td className={styles.actions}>
        {confirming ? (
          <>
            <button
              className={styles.danger}
              disabled={busy}
              onClick={() => revoke.mutate()}
            >
              {busy ? 'Revoking…' : 'Confirm revoke'}
            </button>
            <button
              className={styles.cancel}
              disabled={busy}
              onClick={() => setConfirming(false)}
            >
              Cancel
            </button>
          </>
        ) : (
          <button
            className={styles.revoke}
            disabled={busy}
            onClick={() => setConfirming(true)}
          >
            Revoke
          </button>
        )}
      </td>
    </tr>
  )
}
