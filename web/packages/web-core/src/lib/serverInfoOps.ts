import { wsClient } from './wsClient'
import type { NetworkElement, NetworkInfoPayload } from './types'

export function getServerNetworkInfo(): Promise<NetworkElement[]> {
  return wsClient()
    .request<NetworkInfoPayload>('SERVER_NETWORK_INFO', {})
    .then((p) => p.elements ?? [])
}
