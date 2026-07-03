import CodeMirror from '@uiw/react-codemirror'
import { EditorView } from '@codemirror/view'
import { languageForPath } from '../lib/cmLanguages'
import { useTheme } from '../lib/theme'
import styles from './CodeView.module.css'

export function CodeView({ path, content }: { path: string; content: string }) {
  const { theme } = useTheme()
  return (
    <CodeMirror
      value={content}
      readOnly
      editable={false}
      theme={theme === 'dark' ? 'dark' : 'light'}
      extensions={[...languageForPath(path), EditorView.lineWrapping]}
      basicSetup={{ highlightActiveLine: false, foldGutter: false }}
      className={styles.codeView}
    />
  )
}

export default CodeView
