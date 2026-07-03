import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { wsClient } from '../lib/wsClient'
import { useToast } from '../lib/toast'
import {
  needsConversion,
  convertedName,
  prepareConversions,
  convertCandidate,
} from '../lib/fileConvert'
import type { FileNode, ConflictPayload } from '../lib/types'
import dlgStyles from './ConfirmDialog.module.css'
import styles from './UploadDialog.module.css'

const ACCEPTED_EXTS = ['.md', '.markdown', '.json', '.yaml', '.yml', '.csv', '.txt']
const ACCEPT_ATTR = ACCEPTED_EXTS.join(',')

export interface UploadDialogProps {
  open: boolean
  namespace: string
  project: string
  tree: FileNode[]
  onClose: () => void
}

function collectDirs(nodes: FileNode[]): string[] {
  const dirs: string[] = []
  const walk = (ns: FileNode[]) => {
    for (const n of ns) {
      if (n.isDir) {
        dirs.push(n.path)
        walk(n.children ?? [])
      }
    }
  }
  walk(nodes)
  return dirs.sort()
}

function normalizeDirPath(raw: string): string {
  return raw.replace(/^\/+|\/+$/g, '').replace(/\/\/+/g, '/')
}

export function validateDirPath(raw: string): string | null {
  if (/[\x00-\x1f\x7f]/.test(raw)) return 'Path contains invalid characters'
  const p = normalizeDirPath(raw)
  if (p === '') return null
  const segments = p.split('/')
  if (segments.some((s) => s === '.' || s === '..'))
    return 'Path cannot contain "." or ".." segments'
  return null
}

function fileToBase64(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const res = String(reader.result)
      const comma = res.indexOf(',')
      resolve(comma >= 0 ? res.slice(comma + 1) : res)
    }
    reader.onerror = () => reject(reader.error ?? new Error('file read failed'))
    reader.readAsDataURL(file)
  })
}

function joinPath(dir: string, name: string): string {
  return dir ? `${dir}/${name}` : name
}

function extOf(name: string): string {
  const dot = name.lastIndexOf('.')
  return dot < 0 ? '' : name.slice(dot).toLowerCase()
}

export function UploadDialog({
  open,
  namespace,
  project,
  tree,
  onClose,
}: UploadDialogProps) {
  const qc = useQueryClient()
  const { add: addToast } = useToast()
  const fileInputRef = useRef<HTMLInputElement>(null)

  const [files, setFiles] = useState<File[]>([])
  const [targetMode, setTargetMode] = useState<'existing' | 'new'>('existing')
  const [existingDir, setExistingDir] = useState('')
  const [newPath, setNewPath] = useState('')
  const [uploading, setUploading] = useState(false)
  const [fileErrors, setFileErrors] = useState<Map<string, string>>(new Map())
  const [dragOver, setDragOver] = useState(false)

  const dirs = useMemo(() => collectDirs(tree), [tree])

  const pathError = useMemo(() => {
    if (targetMode !== 'new') return null
    return validateDirPath(newPath)
  }, [targetMode, newPath])

  const targetDir = useMemo(() => {
    if (targetMode === 'existing') return existingDir
    return normalizeDirPath(newPath)
  }, [targetMode, existingDir, newPath])

  const canUpload = files.length > 0 && !pathError && !uploading

  useEffect(() => {
    if (open) {
      setFiles([])
      setTargetMode('existing')
      setExistingDir('')
      setNewPath('')
      setFileErrors(new Map())
      setUploading(false)
      setDragOver(false)
    }
  }, [open])

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        onClose()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  const addFiles = useCallback((incoming: FileList | File[]) => {
    const arr = Array.from(incoming).filter((f) =>
      ACCEPTED_EXTS.includes(extOf(f.name)),
    )
    setFiles((prev) => {
      const existing = new Set(prev.map((f) => f.name))
      const unique = arr.filter((f) => !existing.has(f.name))
      return [...prev, ...unique]
    })
    setFileErrors(new Map())
  }, [])

  const removeFile = useCallback((name: string) => {
    setFiles((prev) => prev.filter((f) => f.name !== name))
    setFileErrors((prev) => {
      const next = new Map(prev)
      next.delete(name)
      return next
    })
  }, [])

  const handleDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    setDragOver(true)
  }, [])

  const handleDragLeave = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    setDragOver(false)
  }, [])

  const handleDrop = useCallback(
    (e: React.DragEvent) => {
      e.preventDefault()
      setDragOver(false)
      if (e.dataTransfer.files.length) {
        addFiles(e.dataTransfer.files)
      }
    },
    [addFiles],
  )

  const handleUpload = useCallback(async () => {
    if (!canUpload) return
    setUploading(true)
    setFileErrors(new Map())

    const convertible = files.filter((f) => needsConversion(f.name))
    const passThrough = files.filter((f) => !needsConversion(f.name))

    let convertedFiles: File[] = []
    if (convertible.length > 0) {
      const { candidates, errors: convErrors } = await prepareConversions(convertible)
      if (convErrors.length > 0) {
        const errMap = new Map<string, string>()
        for (const err of convErrors) {
          const colonIdx = err.indexOf(':')
          const name = colonIdx > 0 ? err.slice(0, colonIdx) : ''
          errMap.set(name, colonIdx > 0 ? err.slice(colonIdx + 2) : err)
        }
        setFileErrors(errMap)
        setUploading(false)
        return
      }
      convertedFiles = candidates.map(convertCandidate)
    }

    type UploadItem = { file: File; destPath: string; originalName: string }
    const items: UploadItem[] = []

    for (const f of passThrough) {
      items.push({
        file: f,
        destPath: joinPath(targetDir, f.name),
        originalName: f.name,
      })
    }
    for (const f of convertedFiles) {
      const orig = convertible.find((c) => convertedName(c.name) === f.name)
      items.push({
        file: f,
        destPath: joinPath(targetDir, f.name),
        originalName: orig?.name ?? f.name,
      })
    }

    let successCount = 0
    const errors = new Map<string, string>()

    for (const item of items) {
      try {
        const content = await fileToBase64(item.file)
        const payload: Record<string, unknown> = {
          namespace,
          projectName: project,
          path: item.destPath,
          content,
          content_encoding: 'base64',
        }

        const frame = await wsClient().requestFrame('SAVE_FILE', payload)
        if (frame.type === 'CONFLICT') {
          const conflict = frame.payload as ConflictPayload
          payload.if_match = conflict.current_etag
          const retry = await wsClient().requestFrame('SAVE_FILE', payload)
          if (retry.type === 'CONFLICT') {
            errors.set(item.originalName, 'File changed during upload')
            continue
          }
        }
        successCount++
      } catch (e) {
        errors.set(
          item.originalName,
          e instanceof Error ? e.message : 'Upload failed',
        )
      }
    }

    setUploading(false)

    if (errors.size > 0) {
      setFileErrors(errors)
      if (successCount > 0) {
        void qc.invalidateQueries({ queryKey: ['tree', namespace, project] })
        addToast({
          level: 'warn',
          text: `Uploaded ${successCount} file(s), ${errors.size} failed`,
        })
      }
      return
    }

    void qc.invalidateQueries({ queryKey: ['tree', namespace, project] })
    if (successCount === 1) {
      addToast({ level: 'warn', text: `Uploaded ${items[0].destPath}` })
    } else {
      addToast({ level: 'warn', text: `Uploaded ${successCount} files` })
    }
    onClose()
  }, [canUpload, files, targetDir, namespace, project, qc, addToast, onClose])

  if (!open) return null

  return (
    <div className={dlgStyles.overlay} onClick={onClose} data-testid="upload-dialog">
      <div
        className={styles.dialog}
        role="dialog"
        aria-modal="true"
        aria-label="Upload Files"
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className={dlgStyles.title}>Upload Files</h2>

        <div className={styles.section}>
          <label className={styles.label}>Files</label>
          <div
            className={`${styles.dropArea} ${dragOver ? styles.dropAreaActive : ''}`}
            onDragOver={handleDragOver}
            onDragLeave={handleDragLeave}
            onDrop={handleDrop}
          >
            <button
              type="button"
              className={styles.chooseBtn}
              onClick={() => fileInputRef.current?.click()}
            >
              Choose Files…
            </button>
            <span className={styles.dropHint}> or drag files here</span>
            <input
              ref={fileInputRef}
              type="file"
              multiple
              accept={ACCEPT_ATTR}
              className={styles.hiddenInput}
              onChange={(e) => {
                if (e.target.files?.length) addFiles(e.target.files)
                e.target.value = ''
              }}
              data-testid="upload-file-input"
            />
          </div>

          {files.length > 0 && (
            <ul className={styles.fileList} data-testid="upload-file-list">
              {files.map((f) => {
                const conv = needsConversion(f.name)
                const error = fileErrors.get(f.name)
                return (
                  <li key={f.name} className={styles.fileItem}>
                    <div className={styles.fileRow}>
                      <span className={styles.fileName}>{f.name}</span>
                      {conv && (
                        <span className={styles.convHint}>
                          → will be converted to {convertedName(f.name)}
                        </span>
                      )}
                      <button
                        type="button"
                        className={styles.fileRemove}
                        onClick={() => removeFile(f.name)}
                        aria-label={`Remove ${f.name}`}
                      >
                        ×
                      </button>
                    </div>
                    {error && <div className={styles.fileError}>{error}</div>}
                  </li>
                )
              })}
            </ul>
          )}
        </div>

        <div className={styles.section}>
          <label className={styles.label}>Target directory</label>
          <div className={styles.radioGroup}>
            <label className={styles.radioLabel}>
              <input
                type="radio"
                name="upload-target-mode"
                checked={targetMode === 'existing'}
                onChange={() => setTargetMode('existing')}
              />
              <select
                className={styles.dirSelect}
                value={existingDir}
                onChange={(e) => {
                  setExistingDir(e.target.value)
                  setTargetMode('existing')
                }}
                data-testid="upload-dir-select"
              >
                <option value="">(project root)</option>
                {dirs.map((d) => (
                  <option key={d} value={d}>
                    {d}
                  </option>
                ))}
              </select>
            </label>
            <label className={styles.radioLabel}>
              <input
                type="radio"
                name="upload-target-mode"
                checked={targetMode === 'new'}
                onChange={() => setTargetMode('new')}
              />
              <span className={styles.radioText}>New path:</span>
              <input
                type="text"
                className={`${styles.pathInput} ${targetMode === 'new' && pathError ? styles.pathInputError : ''}`}
                value={newPath}
                onChange={(e) => {
                  setNewPath(e.target.value)
                  setTargetMode('new')
                }}
                placeholder="e.g. reports/2026/july"
                data-testid="upload-new-path"
              />
            </label>
            {targetMode === 'new' && pathError && (
              <div className={styles.errorText} data-testid="upload-path-error">
                {pathError}
              </div>
            )}
          </div>
        </div>

        <div className={dlgStyles.actions}>
          <button
            className={dlgStyles.cancel}
            onClick={onClose}
            disabled={uploading}
          >
            Cancel
          </button>
          <button
            className={dlgStyles.confirm}
            onClick={handleUpload}
            disabled={!canUpload}
            data-testid="upload-confirm"
          >
            {uploading ? 'Uploading…' : 'Upload'}
          </button>
        </div>
      </div>
    </div>
  )
}
