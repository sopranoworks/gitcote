import { Link, useSearch, useNavigate } from '@tanstack/react-router'
import { useProjectsQuery } from '../lib/queries'
import { namespacesOf } from '../lib/tree'
import { RecoverButton } from '../components/RecoverButton'
import styles from './RepoListPage.module.css'

export function RepoListPage() {
  const { ns } = useSearch({ strict: false }) as { ns?: string }
  const navigate = useNavigate()
  const { data: projects = [], isPending, isError, error } = useProjectsQuery()

  const namespaces = namespacesOf(projects)
  const filtered = ns ? projects.filter((p) => p.namespace === ns) : projects

  return (
    <div className={styles.page}>
      <header className={styles.head}>
        <h1 className={styles.title}>Projects</h1>
        <p className={styles.sub}>
          {isPending
            ? 'Loading…'
            : `${projects.length} project${
                projects.length === 1 ? '' : 's'
              } across ${namespaces.length} namespace${
                namespaces.length === 1 ? '' : 's'
              }`}
        </p>
      </header>

      {isError && (
        <div className={styles.noResults}>
          Could not load projects:{' '}
          {error instanceof Error ? error.message : 'unknown error'}
        </div>
      )}

      <div className={styles.filters}>
        <span className={styles.filterLabel}>Namespace</span>
        <button
          className={styles.chip}
          data-active={!ns}
          onClick={() => navigate({ to: '.', search: {} })}
        >
          All
        </button>
        {namespaces.map((n) => (
          <button
            key={n}
            className={styles.chip}
            data-active={ns === n}
            onClick={() => navigate({ to: '.', search: { ns: n } })}
          >
            {n}
          </button>
        ))}
      </div>

      <ul className={styles.grid}>
        {filtered.map((p) => (
          <li key={`${p.namespace}/${p.name}`}>
            <Link
              to="/p/$namespace/$project"
              params={{ namespace: p.namespace, project: p.name }}
              className={styles.card}
            >
              <div className={styles.cardTop}>
                <span className={styles.repoIcon}>
                  <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
                    <path
                      d="M3 2.5h8a1.5 1.5 0 0 1 1.5 1.5v9.5H4.5A1.5 1.5 0 0 1 3 12z"
                      stroke="currentColor"
                      strokeWidth="1.3"
                    />
                  </svg>
                </span>
                <span className={styles.cardName}>{p.name}</span>
              </div>
              <div className={styles.cardNs}>{p.namespace}</div>
              <div className={styles.cardMeta}>
                <span className={styles.stateDot} data-state={p.state} />
                {p.state}
              </div>
            </Link>
            {p.state !== 'healthy' && (
              <RecoverButton namespace={p.namespace} project={p.name} />
            )}
          </li>
        ))}
      </ul>

      {!isPending && !isError && filtered.length === 0 && (
        <div className={styles.noResults}>
          {ns ? `No projects in "${ns}".` : 'No projects yet.'}
        </div>
      )}
    </div>
  )
}
