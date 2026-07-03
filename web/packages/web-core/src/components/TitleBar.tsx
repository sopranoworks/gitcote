import { Link } from '@tanstack/react-router'
import { usePalette } from '../lib/palette'
import { useShellConfig } from '../lib/shellConfig'
import styles from './TitleBar.module.css'

export { styles as titleBarStyles }

export function TitleBar({
  onToggleSidebar,
}: {
  onToggleSidebar: () => void
}) {
  const { brandName = 'Shoka', renderBreadcrumbs } = useShellConfig()
  const { openPalette } = usePalette()

  return (
    <header className={styles.bar}>
      <div className={styles.left}>
        <button
          className={styles.iconBtn}
          onClick={onToggleSidebar}
          title="Toggle sidebar"
          aria-label="Toggle sidebar"
        >
          <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
            <rect
              x="1.5"
              y="2.5"
              width="13"
              height="11"
              rx="1.5"
              stroke="currentColor"
            />
            <line x1="6" y1="2.5" x2="6" y2="13.5" stroke="currentColor" />
          </svg>
        </button>

        <Link
          to="/"
          activeOptions={{ exact: true }}
          className={styles.brand}
          title="All projects"
          aria-label="All projects"
        >
          <span className={styles.brandWord}>{brandName}</span>
        </Link>

        {renderBreadcrumbs?.(styles)}
      </div>

      <button
        className={styles.commandCentre}
        onClick={() => openPalette('commands')}
        title="Command palette"
      >
        <svg width="13" height="13" viewBox="0 0 16 16" fill="none">
          <path
            d="M2 4l4 4-4 4M8 12h6"
            stroke="currentColor"
            strokeWidth="1.4"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
        <span className={styles.ccLabel}>Search or run a command</span>
        <kbd className={styles.ccKbd}>⌘K</kbd>
      </button>

      <div className={styles.right} />
    </header>
  )
}
