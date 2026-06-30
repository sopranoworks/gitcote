import { useState, useEffect } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  prEventSettingsGet,
  prEventSettingsSetProject,
  prEventSettingsClearProject,
  seedEventSettingsGet,
  seedEventSettingsSetProject,
  seedEventSettingsClearProject,
} from '../ops/eventSettingsOps'
import { PREventsForm, SeedEventsForm } from './PREventsSettings'
import styles from './AgentSettingsProjectControl.module.css'

export function AgentSettingsProjectControl({
  namespace,
  project,
}: {
  namespace: string
  project: string
}) {
  const queryClient = useQueryClient()
  const [modalOpen, setModalOpen] = useState(false)

  const { data: prData } = useQuery({
    queryKey: ['pr-event-settings', namespace, project],
    queryFn: () => prEventSettingsGet(namespace, project),
    staleTime: 30_000,
  })
  const { data: seedData } = useQuery({
    queryKey: ['seed-event-settings', namespace, project],
    queryFn: () => seedEventSettingsGet(namespace, project),
    staleTime: 30_000,
  })

  const hasCustomSettings = prData?.project != null || seedData?.project != null
  const enabled = hasCustomSettings || modalOpen

  useEffect(() => {
    if (!modalOpen) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        setModalOpen(false)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [modalOpen])

  function invalidate() {
    void queryClient.invalidateQueries({ queryKey: ['pr-event-settings', namespace, project] })
    void queryClient.invalidateQueries({ queryKey: ['seed-event-settings', namespace, project] })
  }

  async function handleDisable() {
    await prEventSettingsClearProject(namespace, project)
    await seedEventSettingsClearProject(namespace, project)
    invalidate()
  }

  const prSettings = prData?.project ?? prData?.global ?? {}
  const seedSettings = seedData?.project ?? seedData?.global ?? {}

  return (
    <>
      <div className={styles.control}>
        <label className={styles.checkboxLabel}>
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => {
              if (e.target.checked) {
                setModalOpen(true)
              } else {
                void handleDisable()
              }
            }}
          />
          Custom agent settings
        </label>
        <button
          className={styles.settingsBtn}
          disabled={!enabled}
          onClick={() => setModalOpen(true)}
        >
          Agent settings
        </button>
      </div>

      {modalOpen && (
        <div className={styles.overlay} onClick={() => setModalOpen(false)}>
          <div
            className={styles.dialog}
            role="dialog"
            aria-modal="true"
            aria-label={`Agent Settings — ${namespace}/${project}`}
            onClick={(e) => e.stopPropagation()}
          >
            <div className={styles.dialogHeader}>
              <h2 className={styles.dialogTitle}>
                Agent Settings &mdash; {namespace}/{project}
              </h2>
              <button
                className={styles.closeBtn}
                onClick={() => setModalOpen(false)}
                aria-label="Close"
              >
                &times;
              </button>
            </div>
            <div className={styles.dialogBody}>
              <div className={styles.section}>PR Events</div>
              <PREventsForm
                settings={prSettings}
                inherited={prData?.global}
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
              <div className={styles.section} style={{ marginTop: '1.5rem' }}>
                Seed Events
              </div>
              <SeedEventsForm
                settings={seedSettings}
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
          </div>
        </div>
      )}
    </>
  )
}
