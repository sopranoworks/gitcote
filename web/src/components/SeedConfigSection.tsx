import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useIsSuperUser, useManagesAnyNamespace } from '@shoka/web-core'
import { useToast } from '@shoka/web-core'
import {
  seedConfigGet,
  seedConfigSet,
  seedKeyList,
  seedTest,
  seedPush,
  seedPull,
} from '../ops/seedOps'
import { SeedStatusBadge } from './SeedStatusBadge'
import styles from './SeedConfigSection.module.css'

export function SeedConfigSection({
  namespace,
  project,
}: {
  namespace: string
  project: string
}) {
  const isSuperUser = useIsSuperUser()
  const managesAny = useManagesAnyNamespace()
  const isAdmin = isSuperUser || managesAny

  return (
    <div className={styles.section}>
      <div className={styles.header}>
        <span className={styles.label}>Seed</span>
        <SeedStatusBadge namespace={namespace} project={project} />
        {!isAdmin && <ManualPushButton namespace={namespace} project={project} />}
      </div>
      {isAdmin && (
        <SeedConfigForm namespace={namespace} project={project} />
      )}
    </div>
  )
}

function ManualPushButton({
  namespace,
  project,
}: {
  namespace: string
  project: string
}) {
  const { add: toast } = useToast()
  const [pushing, setPushing] = useState(false)

  async function handlePush() {
    setPushing(true)
    try {
      const result = await seedPush(namespace, project)
      if (result.success) {
        toast({ level: 'warn', text: 'Push succeeded.' })
      } else {
        toast({ level: 'warn', text: result.error ?? 'Push failed.' })
      }
    } catch (e) {
      toast({ level: 'warn', text: e instanceof Error ? e.message : 'Push failed.' })
    } finally {
      setPushing(false)
    }
  }

  return (
    <button className={styles.btn} onClick={() => void handlePush()} disabled={pushing}>
      {pushing ? 'Pushing…' : 'Push'}
    </button>
  )
}

function SeedConfigForm({
  namespace,
  project,
}: {
  namespace: string
  project: string
}) {
  const qc = useQueryClient()
  const { add: toast } = useToast()
  const [seedUrl, setSeedUrl] = useState('')
  const [keyName, setKeyName] = useState('')
  const [pushMode, setPushMode] = useState('disabled')
  const [pushInterval, setPushInterval] = useState('')
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  const [pushing, setPushing] = useState(false)
  const [pulling, setPulling] = useState(false)

  const { data: config } = useQuery({
    queryKey: ['seed-config', namespace, project],
    queryFn: () => seedConfigGet(namespace, project),
    staleTime: 30_000,
  })

  const { data: keysData } = useQuery({
    queryKey: ['seed-keys', namespace],
    queryFn: () => seedKeyList(namespace),
    staleTime: 30_000,
  })

  useEffect(() => {
    if (config) {
      setSeedUrl(config.seedUrl ?? '')
      setKeyName(config.keyName ?? '')
      setPushMode(config.pushMode ?? 'disabled')
      setPushInterval(config.pushInterval ?? '')
    }
  }, [config])

  const keys = keysData?.keys ?? []

  async function handleSave() {
    setSaving(true)
    try {
      await seedConfigSet(namespace, project, seedUrl, keyName, pushMode, pushInterval || undefined)
      toast({ level: 'warn', text: 'Seed config saved.' })
      void qc.invalidateQueries({ queryKey: ['seed-config', namespace, project] })
      void qc.invalidateQueries({ queryKey: ['seed-status', namespace, project] })
    } catch (e) {
      toast({ level: 'warn', text: e instanceof Error ? e.message : 'Save failed.' })
    } finally {
      setSaving(false)
    }
  }

  async function handleTest() {
    setTesting(true)
    try {
      const result = await seedTest(namespace, project)
      if (result.success) {
        toast({ level: 'warn', text: 'Connection successful.' })
      } else {
        toast({ level: 'warn', text: result.error ?? 'Connection failed.' })
      }
    } catch (e) {
      toast({ level: 'warn', text: e instanceof Error ? e.message : 'Test failed.' })
    } finally {
      setTesting(false)
    }
  }

  async function handlePush() {
    setPushing(true)
    try {
      const result = await seedPush(namespace, project)
      if (result.success) {
        toast({ level: 'warn', text: 'Push succeeded.' })
        void qc.invalidateQueries({ queryKey: ['seed-status', namespace, project] })
      } else {
        toast({ level: 'warn', text: result.error ?? 'Push failed.' })
      }
    } catch (e) {
      toast({ level: 'warn', text: e instanceof Error ? e.message : 'Push failed.' })
    } finally {
      setPushing(false)
    }
  }

  async function handlePull() {
    setPulling(true)
    try {
      const result = await seedPull(namespace, project)
      if (result.success) {
        toast({ level: 'warn', text: 'Pull succeeded.' })
        void qc.invalidateQueries({ queryKey: ['seed-status', namespace, project] })
        void qc.invalidateQueries({ queryKey: ['tree', namespace, project] })
      } else {
        toast({ level: 'warn', text: result.error ?? 'Pull failed.' })
      }
    } catch (e) {
      toast({ level: 'warn', text: e instanceof Error ? e.message : 'Pull failed.' })
    } finally {
      setPulling(false)
    }
  }

  return (
    <div className={styles.form}>
      <div className={styles.field}>
        <label className={styles.fieldLabel}>Seed URL</label>
        <input
          className={styles.input}
          type="text"
          placeholder="git@github.com:org/repo.git"
          value={seedUrl}
          onChange={(e) => setSeedUrl(e.target.value)}
        />
      </div>

      <div className={styles.field}>
        <label className={styles.fieldLabel}>SSH Key</label>
        <select
          className={styles.select}
          value={keyName}
          onChange={(e) => setKeyName(e.target.value)}
        >
          <option value="">— none —</option>
          {keys.map((k) => (
            <option key={k.name} value={k.name}>{k.name}</option>
          ))}
        </select>
      </div>

      <div className={styles.field}>
        <label className={styles.fieldLabel}>Push mode</label>
        <select
          className={styles.select}
          value={pushMode}
          onChange={(e) => setPushMode(e.target.value)}
        >
          <option value="disabled">Disabled</option>
          <option value="on-merge">On merge</option>
          <option value="periodic">Periodic</option>
        </select>
      </div>

      {pushMode === 'periodic' && (
        <div className={styles.field}>
          <label className={styles.fieldLabel}>Push interval</label>
          <input
            className={styles.input}
            type="text"
            placeholder="6h"
            value={pushInterval}
            onChange={(e) => setPushInterval(e.target.value)}
          />
        </div>
      )}

      <div className={styles.actions}>
        <button className={styles.btn} onClick={() => void handleSave()} disabled={saving}>
          {saving ? 'Saving…' : 'Save'}
        </button>
        <button className={styles.btn} onClick={() => void handleTest()} disabled={testing || !seedUrl || !keyName}>
          {testing ? 'Testing…' : 'Test Connection'}
        </button>
        <button className={styles.btn} onClick={() => void handlePush()} disabled={pushing || !seedUrl || !keyName}>
          {pushing ? 'Pushing…' : 'Push Now'}
        </button>
        <button className={styles.btn} onClick={() => void handlePull()} disabled={pulling || !seedUrl || !keyName}>
          {pulling ? 'Pulling…' : 'Pull from Seed'}
        </button>
      </div>
    </div>
  )
}
