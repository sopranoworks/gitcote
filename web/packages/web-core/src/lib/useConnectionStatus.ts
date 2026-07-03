import { useEffect, useState } from 'react'
import { wsClient, type ConnState } from './wsClient'

export function useConnectionStatus(): ConnState {
  const [state, setState] = useState<ConnState>(() => wsClient().getState())
  useEffect(() => {
    setState(wsClient().getState())
    return wsClient().subscribeState(setState)
  }, [])
  return state
}
