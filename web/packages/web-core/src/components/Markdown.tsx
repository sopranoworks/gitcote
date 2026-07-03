import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import rehypeHighlight from 'rehype-highlight'
import type { Element } from 'hast'
import { MermaidDiagram } from './MermaidDiagram'
import styles from './Markdown.module.css'
import './highlight.css'

const rehypePlugins = [
  [rehypeHighlight, { detect: false, ignoreMissing: true }],
] as const

function mermaidSource(node: Element | undefined): string | null {
  const code = node?.children?.find((c): c is Element => c.type === 'element' && c.tagName === 'code')
  if (!code) return null
  const cls = code.properties?.className
  const isMermaid = Array.isArray(cls) && cls.includes('language-mermaid')
  if (!isMermaid) return null
  const text = code.children
    .map((c) => (c.type === 'text' ? c.value : ''))
    .join('')
  return text.replace(/\n$/, '')
}

const components: Components = {
  pre(props) {
    const { node, children, ...rest } = props
    const src = mermaidSource(node)
    if (src !== null) {
      return <MermaidDiagram code={src} />
    }
    return <pre {...rest}>{children}</pre>
  },
}

export function Markdown({ content }: { content: string }) {
  return (
    <div className={styles.md}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={rehypePlugins as never}
        components={components}
      >
        {content}
      </ReactMarkdown>
    </div>
  )
}
