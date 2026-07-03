export type FileKind = 'markdown' | 'text' | 'binary'

const BINARY_EXTS = new Set([
  'png', 'jpg', 'jpeg', 'gif', 'webp', 'ico', 'bmp', 'svg', 'pdf', 'zip', 'gz',
  'tar', 'tgz', 'wasm', 'woff', 'woff2', 'ttf', 'otf', 'eot', 'mp3', 'mp4',
  'mov', 'avi', 'webm', 'exe', 'dll', 'so', 'dylib', 'bin', 'class', 'o',
])

const NUL = String.fromCharCode(0)

export function classifyFile(path: string, content: string): FileKind {
  const ext = path.includes('.') ? path.split('.').pop()!.toLowerCase() : ''
  if (BINARY_EXTS.has(ext)) return 'binary'
  if (content.indexOf(NUL) !== -1) return 'binary'
  if (ext === 'md' || ext === 'markdown') return 'markdown'
  return 'text'
}

const CODE_EXTS = new Set([
  'json', 'yaml', 'yml', 'js', 'jsx', 'mjs', 'cjs', 'ts', 'tsx', 'go',
])

export function isHighlightableCode(path: string): boolean {
  const ext = path.includes('.') ? path.split('.').pop()!.toLowerCase() : ''
  return CODE_EXTS.has(ext)
}
