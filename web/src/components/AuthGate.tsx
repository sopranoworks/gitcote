import { useEffect, useState, type ReactNode } from 'react'
import { getStatus, wsClient, type AuthStatus } from '@shoka/web-core'
import { FirstRunWizard } from './FirstRunWizard'
import { LoginScreen } from './LoginScreen'

export function AuthGate({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<AuthStatus | null>(null)
  const [failed, setFailed] = useState(false)

  function refresh() {
    setFailed(false)
    getStatus()
      .then(setStatus)
      .catch(() => setFailed(true))
  }
  useEffect(refresh, [])

  const showApp = status != null && shouldShowApp(status)
  useEffect(() => {
    if (showApp) wsClient().connect()
  }, [showApp])

  if (failed) {
    return (
      <div role="alert" style={{ padding: '2rem', textAlign: 'center', color: 'var(--c-text)' }}>
        Could not reach the server. <button onClick={refresh}>Retry</button>
      </div>
    )
  }
  if (status == null) return null

  if (!status.users_exist) {
    if (status.first_run_allowed) {
      return <FirstRunWizard onDone={refresh} />
    }
    return <>{children}</>
  }
  if (!status.authenticated) {
    return <LoginScreen onDone={refresh} />
  }
  return <>{children}</>
}

function shouldShowApp(s: AuthStatus): boolean {
  if (!s.users_exist) return !s.first_run_allowed
  return s.authenticated
}
