import { useRouterState } from '@tanstack/react-router'
import { useTheme } from '../lib/theme'
import { ConnectionStatus } from './ConnectionStatus'
import styles from './StatusBar.module.css'

export function StatusBar() {
  const { theme, toggle } = useTheme()
  const pathname = useRouterState({ select: (s) => s.location.pathname })

  return (
    <footer className={styles.bar}>
      <div className={styles.left}>
        <ConnectionStatus />
        <span className={styles.sep} />
        <span className={styles.path} title={pathname}>
          {pathname}
        </span>
      </div>

      <div className={styles.right}>
        <button
          className={styles.themeBtn}
          onClick={toggle}
          title={`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`}
          aria-label="Toggle theme"
        >
          {theme === 'dark' ? (
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
              <path
                d="M13 9.5A5.5 5.5 0 0 1 6.5 3 5.5 5.5 0 1 0 13 9.5z"
                fill="currentColor"
              />
            </svg>
          ) : (
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
              <circle cx="8" cy="8" r="3" fill="currentColor" />
              <g stroke="currentColor" strokeWidth="1.3" strokeLinecap="round">
                <path d="M8 1v2M8 13v2M1 8h2M13 8h2M3 3l1.4 1.4M11.6 11.6L13 13M13 3l-1.4 1.4M4.4 11.6L3 13" />
              </g>
            </svg>
          )}
          {theme === 'dark' ? 'Dark' : 'Light'}
        </button>
      </div>
    </footer>
  )
}
