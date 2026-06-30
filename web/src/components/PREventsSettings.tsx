import { useState, useEffect } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  prEventSettingsGet,
  prEventSettingsSetGlobal,
  prEventSettingsSetProject,
  prEventSettingsClearProject,
  seedEventSettingsGet,
  seedEventSettingsSetGlobal,
  seedEventSettingsSetProject,
  seedEventSettingsClearProject,
  type EventAction,
  type ConfirmAction,
  type PREventSettings,
  type SeedEventSettings,
} from '../ops/eventSettingsOps'
import { AgentSelector } from './AgentSelector'
import styles from './PREventsSettings.module.css'

const NOTIFY_METHODS = ['log', 'slack', 'webhook']

function EventActionForm({
  title,
  role,
  value,
  onChange,
  inherited,
}: {
  title: string
  role: string
  value: EventAction
  onChange: (v: EventAction) => void
  inherited?: EventAction | null
}) {
  const isInherited = (field: string) => inherited && (value as Record<string, unknown>)[field] === undefined
  return (
    <div className={styles.eventBlock}>
      <div className={styles.eventTitle}>{title}</div>
      <div className={styles.row}>
        <span className={styles.label}>Enable agent</span>
        <div className={styles.toggleRow}>
          <input
            type="checkbox"
            checked={value.agent_enabled ?? false}
            onChange={(e) => onChange({ ...value, agent_enabled: e.target.checked })}
          />
          {isInherited('agent_enabled') && <span className={styles.inherited}>(inherited)</span>}
        </div>
      </div>
      <div className={styles.row}>
        <span className={styles.label}>Agent</span>
        <AgentSelector
          role={role}
          value={value.agent_name ?? ''}
          onChange={(name) => onChange({ ...value, agent_name: name })}
          disabled={!value.agent_enabled}
        />
        {isInherited('agent_name') && <span className={styles.inherited}>(inherited)</span>}
      </div>
      <div className={styles.row}>
        <span className={styles.label}>Auto-retry</span>
        <div className={styles.toggleRow}>
          <input
            type="checkbox"
            checked={value.auto_retry ?? false}
            onChange={(e) => onChange({ ...value, auto_retry: e.target.checked })}
          />
          {isInherited('auto_retry') && <span className={styles.inherited}>(inherited)</span>}
        </div>
      </div>
      {value.auto_retry && (
        <div className={styles.row}>
          <span className={styles.label}>Max retries</span>
          <input
            type="number"
            min={1}
            max={10}
            value={value.max_retries ?? 3}
            onChange={(e) => onChange({ ...value, max_retries: parseInt(e.target.value) || 1 })}
          />
        </div>
      )}
      <div className={styles.row}>
        <span className={styles.label}>Notification</span>
        <div className={styles.toggleRow}>
          <input
            type="checkbox"
            checked={value.notify_enabled ?? false}
            onChange={(e) => onChange({ ...value, notify_enabled: e.target.checked })}
          />
          {value.notify_enabled && (
            <select
              value={value.notify_method ?? 'log'}
              onChange={(e) => onChange({ ...value, notify_method: e.target.value })}
            >
              {NOTIFY_METHODS.map((m) => <option key={m} value={m}>{m}</option>)}
            </select>
          )}
          {isInherited('notify_enabled') && <span className={styles.inherited}>(inherited)</span>}
        </div>
      </div>
    </div>
  )
}

function ConfirmActionForm({
  value,
  onChange,
  inherited,
}: {
  value: ConfirmAction
  onChange: (v: ConfirmAction) => void
  inherited?: ConfirmAction | null
}) {
  const isInherited = (field: string) => inherited && (value as Record<string, unknown>)[field] === undefined
  return (
    <div className={styles.eventBlock}>
      <div className={styles.eventTitle}>On PR Confirmed</div>
      <div className={styles.row}>
        <span className={styles.label}>Auto-confirm</span>
        <div className={styles.toggleRow}>
          <input
            type="checkbox"
            checked={value.auto_confirm ?? false}
            onChange={(e) => onChange({ ...value, auto_confirm: e.target.checked })}
          />
          {isInherited('auto_confirm') && <span className={styles.inherited}>(inherited)</span>}
        </div>
      </div>
      {!value.auto_confirm && (
        <div className={styles.row}>
          <span className={styles.label}>Notification</span>
          <div className={styles.toggleRow}>
            <input
              type="checkbox"
              checked={value.notify_enabled ?? false}
              onChange={(e) => onChange({ ...value, notify_enabled: e.target.checked })}
            />
            {value.notify_enabled && (
              <select
                value={value.notify_method ?? 'log'}
                onChange={(e) => onChange({ ...value, notify_method: e.target.value })}
              >
                {NOTIFY_METHODS.map((m) => <option key={m} value={m}>{m}</option>)}
              </select>
            )}
            {isInherited('notify_enabled') && <span className={styles.inherited}>(inherited)</span>}
          </div>
        </div>
      )}
    </div>
  )
}

export function PREventsForm({
  settings,
  onSave,
  inherited,
  onReset,
  isProject,
}: {
  settings: PREventSettings
  onSave: (s: PREventSettings) => Promise<void>
  inherited?: PREventSettings | null
  onReset?: () => Promise<void>
  isProject?: boolean
}) {
  const [draft, setDraft] = useState<PREventSettings>(settings)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)

  useEffect(() => { setDraft(settings) }, [settings])

  async function handleSave() {
    setSaving(true)
    try {
      await onSave(draft)
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } catch {
      // ignore
    } finally {
      setSaving(false)
    }
  }

  return (
    <>
      <EventActionForm
        title="On PR Created"
        role="reviewer"
        value={draft.on_created ?? {}}
        onChange={(v) => setDraft({ ...draft, on_created: v })}
        inherited={inherited?.on_created}
      />
      <ConfirmActionForm
        value={draft.on_confirmed ?? {}}
        onChange={(v) => setDraft({ ...draft, on_confirmed: v })}
        inherited={inherited?.on_confirmed}
      />
      <EventActionForm
        title="On PR Rejected"
        role="coder"
        value={draft.on_rejected ?? {}}
        onChange={(v) => setDraft({ ...draft, on_rejected: v })}
        inherited={inherited?.on_rejected}
      />
      <EventActionForm
        title="On Merge Conflict"
        role="merger"
        value={draft.on_merge_conflict ?? {}}
        onChange={(v) => setDraft({ ...draft, on_merge_conflict: v })}
        inherited={inherited?.on_merge_conflict}
      />
      <div>
        <button className={styles.saveBtn} disabled={saving} onClick={() => void handleSave()}>
          {saving ? 'Saving…' : 'Save'}
        </button>
        {isProject && onReset && (
          <button className={styles.resetBtn} onClick={() => void onReset()}>
            Reset to defaults
          </button>
        )}
        {saved && <span className={styles.saved}>Saved</span>}
      </div>
    </>
  )
}

export function SeedEventsForm({
  settings,
  onSave,
  inherited,
  onReset,
  isProject,
}: {
  settings: SeedEventSettings
  onSave: (s: SeedEventSettings) => Promise<void>
  inherited?: SeedEventSettings | null
  onReset?: () => Promise<void>
  isProject?: boolean
}) {
  const [draft, setDraft] = useState<SeedEventSettings>(settings)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)

  useEffect(() => { setDraft(settings) }, [settings])

  async function handleSave() {
    setSaving(true)
    try {
      await onSave(draft)
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } catch {
      // ignore
    } finally {
      setSaving(false)
    }
  }

  return (
    <>
      <EventActionForm
        title="On Push Conflict"
        role="merger"
        value={draft.on_push_conflict ?? {}}
        onChange={(v) => setDraft({ ...draft, on_push_conflict: v })}
        inherited={inherited?.on_push_conflict}
      />
      <EventActionForm
        title="On Pull Conflict"
        role="merger"
        value={draft.on_pull_conflict ?? {}}
        onChange={(v) => setDraft({ ...draft, on_pull_conflict: v })}
        inherited={inherited?.on_pull_conflict}
      />
      <div>
        <button className={styles.saveBtn} disabled={saving} onClick={() => void handleSave()}>
          {saving ? 'Saving…' : 'Save'}
        </button>
        {isProject && onReset && (
          <button className={styles.resetBtn} onClick={() => void onReset()}>
            Reset to defaults
          </button>
        )}
        {saved && <span className={styles.saved}>Saved</span>}
      </div>
    </>
  )
}

export function PREventsSettingsGlobal() {
  const queryClient = useQueryClient()
  const { data } = useQuery({
    queryKey: ['pr-event-settings-global'],
    queryFn: () => prEventSettingsGet('', ''),
    staleTime: 30_000,
  })

  const { data: seedData } = useQuery({
    queryKey: ['seed-event-settings-global'],
    queryFn: () => seedEventSettingsGet('', ''),
    staleTime: 30_000,
  })

  return (
    <div className={styles.container}>
      <div className={styles.sectionHeader}>PR Events (Global Defaults)</div>
      <PREventsForm
        settings={data?.global ?? {}}
        onSave={async (s) => {
          await prEventSettingsSetGlobal(s)
          void queryClient.invalidateQueries({ queryKey: ['pr-event-settings-global'] })
        }}
      />

      <div className={styles.sectionHeader} style={{ marginTop: '1.5rem' }}>Seed Events (Global Defaults)</div>
      <SeedEventsForm
        settings={seedData?.global ?? {}}
        onSave={async (s) => {
          await seedEventSettingsSetGlobal(s)
          void queryClient.invalidateQueries({ queryKey: ['seed-event-settings-global'] })
        }}
      />
    </div>
  )
}

export function PREventsSettingsProject({ namespace, project }: { namespace: string; project: string }) {
  const queryClient = useQueryClient()

  const { data } = useQuery({
    queryKey: ['pr-event-settings', namespace, project],
    queryFn: () => prEventSettingsGet(namespace, project),
    staleTime: 30_000,
  })

  const { data: seedData } = useQuery({
    queryKey: ['seed-event-settings', namespace, project],
    queryFn: () => seedEventSettingsGet(namespace, project),
    staleTime: 30_000,
  })

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ['pr-event-settings', namespace, project] })
    void queryClient.invalidateQueries({ queryKey: ['seed-event-settings', namespace, project] })
  }

  return (
    <div className={styles.container}>
      <div className={styles.sectionHeader}>PR Events (Project Override)</div>
      <PREventsForm
        settings={data?.project ?? {}}
        inherited={data?.global}
        isProject
        onSave={async (s) => {
          await prEventSettingsSetProject(namespace, project, s)
          invalidate()
        }}
        onReset={async () => {
          await prEventSettingsClearProject(namespace, project)
          invalidate()
        }}
      />

      <div className={styles.sectionHeader} style={{ marginTop: '1.5rem' }}>Seed Events (Project Override)</div>
      <SeedEventsForm
        settings={seedData?.project ?? {}}
        inherited={seedData?.global}
        isProject
        onSave={async (s) => {
          await seedEventSettingsSetProject(namespace, project, s)
          invalidate()
        }}
        onReset={async () => {
          await seedEventSettingsClearProject(namespace, project)
          invalidate()
        }}
      />
    </div>
  )
}
