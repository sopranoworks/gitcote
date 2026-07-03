import { useEffect, useState } from 'react'
import { useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { useSearchQuery } from '../lib/search'
import { useDebouncedValue } from '../lib/useDebouncedValue'
import styles from './SearchPage.module.css'

export function SearchPage() {
  const { namespace, project } = useParams({ strict: false }) as {
    namespace: string
    project: string
  }
  const { q = '' } = useSearch({ strict: false }) as { q?: string }
  const navigate = useNavigate()

  const [term, setTerm] = useState(q)
  const debounced = useDebouncedValue(term, 250)

  useEffect(() => {
    setTerm(q)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q])

  useEffect(() => {
    if (debounced === q) return
    void navigate({
      to: '/p/$namespace/$project/search',
      params: { namespace, project },
      search: debounced ? { q: debounced } : {},
      replace: true,
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debounced])

  const { data: matches, isFetching, isError } = useSearchQuery(
    namespace,
    project,
    q,
  )

  const hasQuery = q.trim() !== ''

  return (
    <div className={styles.page}>
      <div className={styles.toolbar}>
        <span className={styles.scope} title={`${namespace}/${project}`}>
          Search <strong>{project}</strong>
        </span>
        <form
          className={styles.form}
          onSubmit={(e) => {
            e.preventDefault()
            void navigate({
              to: '/p/$namespace/$project/search',
              params: { namespace, project },
              search: term ? { q: term } : {},
              replace: true,
            })
          }}
        >
          <input
            autoFocus
            className={styles.input}
            type="search"
            value={term}
            onChange={(e) => setTerm(e.target.value)}
            placeholder="Search files in this project…"
            aria-label="Search query"
          />
        </form>
      </div>

      <div className={styles.body} data-scroll-restoration-id="search-body">
        {!hasQuery ? (
          <div className={styles.hint}>
            Type to search file names and contents in{' '}
            <code>{namespace}/{project}</code>.
          </div>
        ) : isError ? (
          <div className={styles.error}>Search failed. Try again.</div>
        ) : !matches ? (
          <div className={styles.hint}>Searching…</div>
        ) : matches.length === 0 ? (
          <div className={styles.hint}>
            No results for <code>{q}</code>.
          </div>
        ) : (
          <>
            <div className={styles.count}>
              {matches.length} {matches.length === 1 ? 'result' : 'results'}
              {isFetching && <span className={styles.fetching}> · updating…</span>}
            </div>
            <ul className={styles.results}>
              {matches.map((m) => (
                <li key={m.path}>
                  <button
                    className={styles.result}
                    onClick={() =>
                      void navigate({
                        to: '/p/$namespace/$project/blob/$',
                        params: { namespace, project, _splat: m.path },
                      })
                    }
                  >
                    <span className={styles.resultPath}>{m.path}</span>
                    {m.snippet && (
                      <span className={styles.snippet}>{m.snippet}</span>
                    )}
                  </button>
                </li>
              ))}
            </ul>
          </>
        )}
      </div>
    </div>
  )
}
