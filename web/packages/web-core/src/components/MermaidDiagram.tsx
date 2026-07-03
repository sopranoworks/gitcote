import { useEffect, useId, useState } from 'react'
import styles from './MermaidDiagram.module.css'

let mermaidPromise: Promise<typeof import('mermaid').default> | null = null
function loadMermaid() {
  if (!mermaidPromise) {
    mermaidPromise = import('mermaid').then((mod) => {
      mod.default.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'default' })
      return mod.default
    })
  }
  return mermaidPromise
}

export function MermaidDiagram({ code }: { code: string }) {
  const id = 'mermaid-' + useId().replace(/:/g, '')
  const [svg, setSvg] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    let cancelled = false
    setSvg(null)
    setFailed(false)
    loadMermaid()
      .then(async (mermaid) => {
        const valid = await mermaid.parse(code, { suppressErrors: true })
        if (valid === false) throw new Error('invalid mermaid diagram')
        const { svg } = await mermaid.render(id, code)
        if (!cancelled) setSvg(svg)
      })
      .catch(() => {
        if (!cancelled) setFailed(true)
      })
    return () => {
      cancelled = true
    }
  }, [code, id])

  if (failed) {
    return (
      <div className={styles.error} role="alert" data-testid="mermaid-error">
        <span className={styles.errorLabel}>Diagram could not be rendered</span>
        <pre className={styles.raw}>{code}</pre>
      </div>
    )
  }
  if (svg) {
    return (
      <div
        className={styles.diagram}
        data-testid="mermaid-diagram"
        dangerouslySetInnerHTML={{ __html: svg }}
      />
    )
  }
  return (
    <div className={styles.loading} data-testid="mermaid-loading">
      Rendering diagram…
    </div>
  )
}
