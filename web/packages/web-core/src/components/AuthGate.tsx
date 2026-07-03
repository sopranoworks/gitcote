import { useEffect, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { getStatus, type AuthStatus } from '../lib/authClient'
import { seedAuthStatus } from '../lib/authStatus'
import { wsClient } from '../lib/wsClient'
import { FirstRunWizard } from './FirstRunWizard'
import { LoginScreen } from './LoginScreen'

export function AuthGate({ appName, children }: { appName: string; children: ReactNode }) {
  const queryClient = useQueryClient()
  const [status, setStatus] = useState<AuthStatus | null>(null)
  const [failed, setFailed] = useState(false)

  function refresh() {
    setFailed(false)
    getStatus()
      .then((s) => {
        seedAuthStatus(queryClient, s)
        setStatus(s)
      })
      .catch(() => setFailed(true))
  }
  useEffect(refresh, [])

  const showApp = status != null && shouldShowApp(status)
  useEffect(() => {
    if (showApp) wsClient().connect()
  }, [showApp])

  if (failed) {
    return (
      <div role="alert" style={{ padding: '2rem', textAlign: 'center' }}>
        Could not reach the server. <button onClick={refresh}>Retry</button>
      </div>
    )
  }
  if (status == null) return null

  if (!status.users_exist) {
    if (status.first_run_allowed) {
      return <FirstRunWizard appName={appName} passkeyEnabled={status.passkey_enabled} onDone={refresh} />
    }
    return <>{children}</>
  }
  if (!status.authenticated) {
    return <LoginScreen appName={appName} passkeyEnabled={status.passkey_enabled} onDone={refresh} />
  }
  return <>{children}</>
}

function shouldShowApp(s: AuthStatus): boolean {
  if (!s.users_exist) return !s.first_run_allowed
  return s.authenticated
}
