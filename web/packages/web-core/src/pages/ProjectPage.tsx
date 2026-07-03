import { Link, useParams } from '@tanstack/react-router'
import { useTreeQuery } from '../lib/queries'
import { flattenFilePaths } from '../lib/tree'
import { usePalette } from '../lib/palette'
import { useContentConfig } from '../lib/contentConfig'
import styles from './ProjectPage.module.css'

export function ProjectPage() {
  const { namespace, project } = useParams({ strict: false }) as {
    namespace: string
    project: string
  }
  const { data: tree = [], isError } = useTreeQuery(namespace, project)
  const { openPalette } = usePalette()
  const { renderNewFileLink, renderProjectExtra } = useContentConfig()

  if (isError) {
    return (
      <div className={styles.page}>
        <h1>Project not found</h1>
        <p>
          <code>
            {namespace}/{project}
          </code>{' '}
          could not be loaded.
        </p>
        <Link to="/">← Back to projects</Link>
      </div>
    )
  }

  const suggestions = flattenFilePaths(tree)
    .filter((p) => !p.includes('/'))
    .slice(0, 5)

  return (
    <div className={styles.page}>
      <div className={styles.welcome}>
        <div className={styles.kicker}>{namespace}</div>
        <h1 className={styles.title}>{project}</h1>
        <p className={styles.lead}>
          Browse the tree on the left, or jump straight to a file.
        </p>

        <div className={styles.actions}>
          <button className={styles.primaryBtn} onClick={() => openPalette('files')}>
            Go to File <kbd>⌘P</kbd>
          </button>
          {renderNewFileLink && renderNewFileLink(namespace, project, styles)}
          <Link
            to="/p/$namespace/$project/search"
            params={{ namespace, project }}
            className={styles.ghostBtn}
          >
            Search <kbd>⌘⇧F</kbd>
          </Link>
          <button className={styles.ghostBtn} onClick={() => openPalette('commands')}>
            Command Palette <kbd>⌘K</kbd>
          </button>
        </div>

        {renderProjectExtra?.(namespace, project)}

        {suggestions.length > 0 && (
          <div className={styles.suggest}>
            <div className={styles.suggestHead}>Top-level docs</div>
            <ul className={styles.suggestList}>
              {suggestions.map((path) => (
                <li key={path}>
                  <Link
                    to="/p/$namespace/$project/blob/$"
                    params={{ namespace, project, _splat: path }}
                    className={styles.suggestLink}
                  >
                    {path}
                  </Link>
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </div>
  )
}
