import { useEffect, useRef, useState, useCallback, type RefObject } from 'react'
import styles from './FileSearchBar.module.css'

interface FileSearchBarProps {
  containerRef: RefObject<HTMLDivElement | null>
  initialQuery?: string
  contentKey?: string
  onClose: () => void
}

function findTextRanges(
  container: HTMLElement,
  query: string,
  caseSensitive: boolean,
): Range[] {
  const ranges: Range[] = []
  if (!query) return ranges
  const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT)
  const searchStr = caseSensitive ? query : query.toLowerCase()
  let node: Text | null
  while ((node = walker.nextNode() as Text | null)) {
    const text = caseSensitive
      ? node.textContent!
      : node.textContent!.toLowerCase()
    let start = 0
    let idx: number
    while ((idx = text.indexOf(searchStr, start)) !== -1) {
      const range = new Range()
      range.setStart(node, idx)
      range.setEnd(node, idx + query.length)
      ranges.push(range)
      start = idx + 1
    }
  }
  return ranges
}

function scrollRangeIntoView(range: Range) {
  const el = range.startContainer.parentElement
  if (el) el.scrollIntoView({ block: 'center', behavior: 'smooth' })
}

const HAS_HIGHLIGHT =
  typeof globalThis !== 'undefined' &&
  'Highlight' in globalThis &&
  typeof CSS !== 'undefined' &&
  'highlights' in CSS

function setHighlight(name: string, ranges: Range[]) {
  if (!HAS_HIGHLIGHT) return
  const reg = CSS.highlights!
  if (ranges.length > 0) {
    reg.set(name, new Highlight(...ranges))
  } else {
    reg.delete(name)
  }
}

function clearHighlights() {
  if (!HAS_HIGHLIGHT) return
  CSS.highlights!.delete('file-search')
  CSS.highlights!.delete('file-search-current')
}

export function FileSearchBar({
  containerRef,
  initialQuery,
  contentKey,
  onClose,
}: FileSearchBarProps) {
  const inputRef = useRef<HTMLInputElement>(null)
  const [query, setQuery] = useState(initialQuery ?? '')
  const [caseSensitive, setCaseSensitive] = useState(false)
  const [matchCount, setMatchCount] = useState(0)
  const [currentIndex, setCurrentIndex] = useState(-1)
  const rangesRef = useRef<Range[]>([])

  useEffect(() => {
    if (!containerRef.current || !query) {
      clearHighlights()
      rangesRef.current = []
      setMatchCount(0)
      setCurrentIndex(-1)
      return clearHighlights
    }

    const timer = setTimeout(() => {
      if (!containerRef.current) return
      const ranges = findTextRanges(containerRef.current, query, caseSensitive)
      rangesRef.current = ranges
      setMatchCount(ranges.length)
      const idx = ranges.length > 0 ? 0 : -1
      setCurrentIndex(idx)
      setHighlight('file-search', ranges)
      if (idx >= 0) {
        setHighlight('file-search-current', [ranges[idx]])
        scrollRangeIntoView(ranges[idx])
      } else {
        setHighlight('file-search-current', [])
      }
    }, 80)

    return () => {
      clearTimeout(timer)
      clearHighlights()
    }
    // contentKey triggers re-highlight when rendered content changes
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [containerRef, query, caseSensitive, contentKey])

  const goTo = useCallback(
    (idx: number) => {
      const ranges = rangesRef.current
      if (ranges.length === 0) return
      const next = ((idx % ranges.length) + ranges.length) % ranges.length
      setCurrentIndex(next)
      setHighlight('file-search-current', [ranges[next]])
      scrollRangeIntoView(ranges[next])
    },
    [],
  )

  const next = useCallback(
    () => goTo(currentIndex + 1),
    [goTo, currentIndex],
  )
  const prev = useCallback(
    () => goTo(currentIndex - 1),
    [goTo, currentIndex],
  )

  useEffect(() => {
    inputRef.current?.focus()
    inputRef.current?.select()
  }, [])

  return (
    <div className={styles.bar} data-testid="file-search-bar">
      <input
        ref={inputRef}
        className={styles.input}
        type="text"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        placeholder="Find in file…"
        aria-label="Find in file"
        onKeyDown={(e) => {
          if (e.key === 'Escape') onClose()
          else if (e.key === 'Enter') {
            e.preventDefault()
            e.shiftKey ? prev() : next()
          }
        }}
      />
      {query && (
        <span className={styles.matchInfo} data-testid="search-match-count">
          {matchCount > 0
            ? `${currentIndex + 1}/${matchCount}`
            : 'No matches'}
        </span>
      )}
      <button
        type="button"
        className={styles.navBtn}
        onClick={prev}
        disabled={matchCount === 0}
        aria-label="Previous match"
        title="Previous (Shift+Enter)"
      >
        <svg width="14" height="14" viewBox="0 0 16 16">
          <path d="M8 4L3 9h10z" fill="currentColor" />
        </svg>
      </button>
      <button
        type="button"
        className={styles.navBtn}
        onClick={next}
        disabled={matchCount === 0}
        aria-label="Next match"
        title="Next (Enter)"
      >
        <svg width="14" height="14" viewBox="0 0 16 16">
          <path d="M8 12L3 7h10z" fill="currentColor" />
        </svg>
      </button>
      <button
        type="button"
        className={`${styles.navBtn} ${caseSensitive ? styles.active : ''}`}
        onClick={() => setCaseSensitive(!caseSensitive)}
        aria-label="Match case"
        title="Match case"
      >
        Aa
      </button>
      <button
        type="button"
        className={styles.closeBtn}
        onClick={onClose}
        aria-label="Close search"
        title="Close (Escape)"
      >
        <svg width="14" height="14" viewBox="0 0 16 16">
          <path
            d="M4 4l8 8M12 4l-8 8"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinecap="round"
          />
        </svg>
      </button>
    </div>
  )
}
