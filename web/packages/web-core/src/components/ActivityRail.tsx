import { useShellConfig } from '../lib/shellConfig'
import styles from './ActivityRail.module.css'

export { styles as activityRailStyles }

export function ActivityRail({
  active,
  onSelect,
  disabled = [],
}: {
  active: string
  onSelect: (v: string) => void
  disabled?: string[]
}) {
  const { railItems, renderRailBottom } = useShellConfig()

  return (
    <div className={styles.rail}>
      <nav className={styles.nav} aria-label="Activity bar">
        {railItems.map((it) => {
          const isDisabled = disabled.includes(it.id)
          return (
            <button
              key={it.id}
              className={styles.item}
              data-active={!isDisabled && active === it.id}
              data-disabled={isDisabled}
              disabled={isDisabled}
              aria-disabled={isDisabled}
              title={isDisabled ? `${it.label} — not available here` : it.label}
              aria-label={it.label}
              aria-pressed={!isDisabled && active === it.id}
              onClick={() => onSelect(it.id)}
            >
              {it.icon}
            </button>
          )
        })}
      </nav>

      {renderRailBottom && (
        <div className={styles.bottom}>
          {renderRailBottom(styles)}
        </div>
      )}
    </div>
  )
}
