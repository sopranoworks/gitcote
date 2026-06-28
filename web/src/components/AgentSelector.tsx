import { useQuery } from '@tanstack/react-query'
import { agentList, type AgentInfo } from '../ops/agentOps'

export function AgentSelector({
  role,
  value,
  onChange,
  disabled,
}: {
  role: string
  value: string
  onChange: (name: string) => void
  disabled?: boolean
}) {
  const { data } = useQuery({
    queryKey: ['agent-list'],
    queryFn: agentList,
    staleTime: 60_000,
  })

  const filtered = (data?.agents ?? []).filter((a: AgentInfo) => a.role === role)

  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      disabled={disabled}
      style={{
        padding: '4px 8px',
        borderRadius: 4,
        border: '1px solid var(--c-border, #444)',
        background: 'var(--c-bg-raised, #2a2a2a)',
        color: 'var(--c-text, #e0e0e0)',
        fontSize: '0.85rem',
      }}
    >
      <option value="">— select —</option>
      {filtered.map((a: AgentInfo) => (
        <option key={a.name} value={a.name}>
          {a.display_name}{a.display_name !== a.name ? ` (${a.name})` : ''}
        </option>
      ))}
    </select>
  )
}
