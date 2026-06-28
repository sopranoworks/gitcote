import { wsClient } from '@shoka/web-core'

export interface AgentInfo {
  name: string
  role: string
  display_name: string
}

export function agentList(): Promise<{ agents: AgentInfo[] }> {
  return wsClient().request('AGENT_LIST', {})
}
