import { lazy, Suspense } from 'react'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import {
  useHistoryQuery,
  useFileAtQuery,
  useDiffQuery,
} from '../lib/queries'
import { classifyFile, isHighlightableCode } from '../lib/fileKind'
import { Markdown } from '../components/Markdown'
import type { HistoryCommit, FileDiff } from '../lib/types'
import filePage from './FilePage.module.css'
import styles from './HistoryPage.module.css'

type HistorySearch = { at?: string; from?: string; to?: string; mode?: 'version' | 'diff' }

const CodeView = lazy(() => import('../components/CodeView'))

const shortHash = (h: string) => h.slice(0, 8)

function formatDate(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString()
}

const SUPPRESSED_LABEL: Record<string, string> = {
  binary: 'diff omitted — this version is a binary file',
  too_large: 'diff omitted — the file is too large to diff',
  timeout: 'diff omitted — the diff timed out',
}

export function HistoryPage() {
  const params = useParams({ strict: false }) as {
    namespace?: string
    project?: string
    _splat?: string
  }
  const namespace = params.namespace ?? ''
  const project = params.project ?? ''
  const path = params._splat ?? ''
  const search = useSearch({ strict: false }) as HistorySearch
  const mode = search.mode ?? 'diff'

  const { data: history, isError: histErr } = useHistoryQuery(
    namespace,
    project,
    path,
  )
  const commits: HistoryCommit[] = history?.commits ?? []

  const selected = search.at ?? commits[0]?.hash ?? ''
  const toHash = search.to ?? selected
  const toIdx = commits.findIndex((c) => c.hash === toHash)
  const prevHash = toIdx >= 0 ? commits[toIdx + 1]?.hash : undefined
  const fromHash = search.from ?? prevHash ?? ''

  if (!path) {
    return (
      <div className={styles.page}>
        <div className={styles.empty}>Select a file to see its history.</div>
      </div>
    )
  }

  return (
    <div className={styles.page}>
      <div className={filePage.toolbar}>
        <span className={filePage.filePath} title={path}>
          {path}
        </span>
        <span className={styles.spacer} />
        <div className={styles.tabs} role="tablist" aria-label="History mode">
          <ModeTab mode="version" current={mode} label="Version" />
          <ModeTab mode="diff" current={mode} label="Diff" />
        </div>
      </div>

      <div className={styles.split}>
        <aside className={styles.commitList} aria-label="Commit history">
          {histErr ? (
            <div className={styles.empty}>Could not load history.</div>
          ) : !history ? (
            <div className={styles.empty}>Loading…</div>
          ) : commits.length === 0 ? (
            <div className={styles.empty}>No commits for this file.</div>
          ) : (
            <ul className={styles.commits}>
              {commits.map((c) => (
                <li key={c.hash}>
                  <Link
                    to="/p/$namespace/$project/history/$"
                    params={{ namespace, project, _splat: path }}
                    search={{ at: c.hash, mode }}
                    className={styles.commit}
                    data-active={c.hash === selected}
                    aria-current={c.hash === selected ? 'true' : undefined}
                  >
                    <span className={styles.commitSubject}>{c.subject}</span>
                    <span className={styles.commitMeta}>
                      <span className={styles.committer}>{c.committer}</span>
                      <span className={styles.commitDate}>
                        {formatDate(c.commitDate)}
                      </span>
                      <code className={styles.shortHash}>
                        {shortHash(c.hash)}
                      </code>
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </aside>

        <section className={styles.detail}>
          {mode === 'version' ? (
            <VersionView
              namespace={namespace}
              project={project}
              path={path}
              hash={selected}
            />
          ) : (
            <DiffPanel
              namespace={namespace}
              project={project}
              path={path}
              fromHash={fromHash}
              toHash={toHash}
              commits={commits}
            />
          )}
        </section>
      </div>
    </div>
  )
}

function ModeTab({
  mode,
  current,
  label,
}: {
  mode: 'version' | 'diff'
  current: 'version' | 'diff'
  label: string
}) {
  return (
    <Link
      to="."
      search={(prev: Record<string, unknown>) => ({ ...prev, mode })}
      className={styles.tab}
      data-active={current === mode}
      role="tab"
      aria-selected={current === mode}
    >
      {label}
    </Link>
  )
}

function VersionView({
  namespace,
  project,
  path,
  hash,
}: {
  namespace: string
  project: string
  path: string
  hash: string
}) {
  const { data, isError } = useFileAtQuery(namespace, project, path, hash)

  if (!hash) {
    return <div className={styles.empty}>Select a commit to view its content.</div>
  }
  if (isError) {
    return (
      <div className={filePage.error}>
        Could not load this version of <code>{path}</code>.
      </div>
    )
  }
  if (!data) {
    return <div className={filePage.loading}>Loading…</div>
  }

  const kind = classifyFile(path, data.content)
  return (
    <div className={styles.versionBody}>
      {kind === 'markdown' ? (
        <Markdown content={data.content} />
      ) : kind === 'binary' ? (
        <div className={filePage.placeholder}>Binary file — cannot preview.</div>
      ) : isHighlightableCode(path) ? (
        <Suspense fallback={<pre className={filePage.plain}>{data.content}</pre>}>
          <CodeView path={path} content={data.content} />
        </Suspense>
      ) : (
        <pre className={filePage.plain}>{data.content}</pre>
      )}
    </div>
  )
}

function DiffPanel({
  namespace,
  project,
  path,
  fromHash,
  toHash,
  commits,
}: {
  namespace: string
  project: string
  path: string
  fromHash: string
  toHash: string
  commits: HistoryCommit[]
}) {
  const { data, isError } = useDiffQuery(
    namespace,
    project,
    path,
    fromHash,
    toHash,
  )

  return (
    <div className={styles.diffPanel}>
      <div className={styles.compareBar}>
        <CommitSelect
          label="From"
          which="from"
          value={fromHash}
          commits={commits}
        />
        <span className={styles.compareArrow} aria-hidden="true">
          →
        </span>
        <CommitSelect
          label="To"
          which="to"
          value={toHash}
          commits={commits}
        />
      </div>
      {!fromHash || !toHash ? (
        <div className={styles.empty}>No earlier version to compare against.</div>
      ) : isError ? (
        <div className={filePage.error}>Could not load the diff.</div>
      ) : !data ? (
        <div className={filePage.loading}>Loading…</div>
      ) : (
        <DiffBody diff={data} />
      )}
    </div>
  )
}

function CommitSelect({
  label,
  which,
  value,
  commits,
}: {
  label: string
  which: 'from' | 'to'
  value: string
  commits: HistoryCommit[]
}) {
  const navigate = useNavigate()
  return (
    <label className={styles.compareField}>
      <span className={styles.compareLabel}>{label}</span>
      <select
        className={styles.compareSelect}
        value={value}
        aria-label={`${label} version`}
        onChange={(e) =>
          void navigate({
            to: '.',
            search: (prev: Record<string, unknown>) => ({
              ...prev,
              [which]: e.target.value,
            }),
          })
        }
      >
        {commits.map((c) => (
          <option key={c.hash} value={c.hash}>
            {shortHash(c.hash)} — {c.subject}
          </option>
        ))}
      </select>
    </label>
  )
}

function DiffBody({ diff }: { diff: FileDiff }) {
  if (diff.suppressed) {
    const label =
      SUPPRESSED_LABEL[diff.suppressed] ?? `diff omitted (${diff.suppressed})`
    return (
      <div className={styles.suppressed} role="status">
        {label}
      </div>
    )
  }
  const hunks = diff.hunks ?? []
  if (hunks.length === 0) {
    return <div className={styles.empty}>No changes between these versions.</div>
  }

  return (
    <div className={styles.diff} data-status={diff.status}>
      {hunks.map((h, hi) => {
        let oldLn = h.oldStart
        let newLn = h.newStart
        return (
          <div key={hi} className={styles.hunk}>
            <div className={styles.hunkHeader}>
              @@ -{h.oldStart},{h.oldLines} +{h.newStart},{h.newLines} @@
            </div>
            {h.lines.map((l, li) => {
              const oldCell = l.op === 'add' ? '' : String(oldLn++)
              const newCell = l.op === 'delete' ? '' : String(newLn++)
              const sign = l.op === 'add' ? '+' : l.op === 'delete' ? '−' : ' '
              return (
                <div key={li} className={styles.diffLine} data-op={l.op}>
                  <span className={styles.lineNo}>{oldCell}</span>
                  <span className={styles.lineNo}>{newCell}</span>
                  <span className={styles.lineSign}>{sign}</span>
                  <span className={styles.lineText}>{l.text || ' '}</span>
                </div>
              )
            })}
          </div>
        )
      })}
    </div>
  )
}
