export const CONVERTIBLE_EXTS = ['.csv', '.txt'] as const
export type ConvertibleExt = (typeof CONVERTIBLE_EXTS)[number]

export function needsConversion(filename: string): ConvertibleExt | null {
  const dot = filename.lastIndexOf('.')
  if (dot < 0) return null
  const ext = filename.slice(dot).toLowerCase()
  if (ext === '.csv') return '.csv'
  if (ext === '.txt') return '.txt'
  return null
}

export function convertedName(filename: string): string {
  const dot = filename.lastIndexOf('.')
  if (dot < 0) return filename + '.md'
  return filename.slice(0, dot) + '.md'
}

export function validateUtf8(bytes: Uint8Array): string {
  let data = bytes
  if (bytes.length >= 3 && bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf) {
    data = bytes.slice(3)
  }
  return new TextDecoder('utf-8', { fatal: true }).decode(data)
}

function parseCsv(text: string): string[][] {
  const rows: string[][] = []
  let row: string[] = []
  let field = ''
  let inQuote = false

  for (let i = 0; i < text.length; i++) {
    const ch = text[i]
    if (inQuote) {
      if (ch === '"') {
        if (text[i + 1] === '"') {
          field += '"'
          i++
        } else {
          inQuote = false
        }
      } else {
        field += ch
      }
    } else if (ch === '"') {
      inQuote = true
    } else if (ch === ',') {
      row.push(field)
      field = ''
    } else if (ch === '\r' || ch === '\n') {
      if (ch === '\r' && text[i + 1] === '\n') i++
      row.push(field)
      field = ''
      rows.push(row)
      row = []
    } else {
      field += ch
    }
  }

  row.push(field)
  if (row.length > 1 || row[0] !== '') rows.push(row)

  return rows
}

function escapePipe(s: string): string {
  return s.replace(/\|/g, '\\|')
}

export function csvToMarkdownTable(text: string): string {
  const rows = parseCsv(text)
  if (rows.length === 0) return ''

  const headers = rows[0]
  const data = rows.slice(1)

  const lines: string[] = []
  lines.push('| ' + headers.map(escapePipe).join(' | ') + ' |')
  lines.push('| ' + headers.map(() => '---').join(' | ') + ' |')

  for (const row of data) {
    const cells = headers.map((_, i) => escapePipe(row[i] ?? ''))
    lines.push('| ' + cells.join(' | ') + ' |')
  }

  return lines.join('\n') + '\n'
}

export interface ConversionCandidate {
  originalName: string
  convertedName: string
  type: ConvertibleExt
  text: string
}

function readFileBytes(file: Blob): Promise<Uint8Array> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(new Uint8Array(reader.result as ArrayBuffer))
    reader.onerror = () => reject(reader.error ?? new Error('file read failed'))
    reader.readAsArrayBuffer(file)
  })
}

export async function prepareConversions(
  files: File[],
): Promise<{ candidates: ConversionCandidate[]; errors: string[] }> {
  const candidates: ConversionCandidate[] = []
  const errors: string[] = []

  for (const file of files) {
    const type = needsConversion(file.name)
    if (!type) continue

    try {
      const bytes = await readFileBytes(file)
      const text = validateUtf8(bytes)
      candidates.push({
        originalName: file.name,
        convertedName: convertedName(file.name),
        type,
        text,
      })
    } catch {
      errors.push(
        `${file.name}: This file does not appear to be UTF-8 encoded. Please convert it to UTF-8 before uploading.`,
      )
    }
  }

  return { candidates, errors }
}

export function convertCandidate(c: ConversionCandidate): File {
  const content = c.type === '.csv' ? csvToMarkdownTable(c.text) : c.text
  return new File([content], c.convertedName, { type: 'text/markdown' })
}
