import { useBanner } from '../lib/banner'
import styles from './Banner.module.css'

export function Banner() {
  const { banner, clear } = useBanner()
  if (!banner) return null
  return (
    <div className={styles.banner} role="status">
      <span className={styles.text}>
        {banner.text}
        {banner.by ? <span className={styles.by}> · by {banner.by}</span> : null}
      </span>
      <div className={styles.actions}>
        <button
          className={styles.reload}
          onClick={() => {
            banner.reload()
            clear()
          }}
        >
          Reload
        </button>
        <button className={styles.dismiss} onClick={clear}>
          Dismiss
        </button>
      </div>
    </div>
  )
}
