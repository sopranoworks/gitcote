export function fuzzyScore(query: string, target: string): number | null {
  if (!query) return 0
  const q = query.toLowerCase()
  const t = target.toLowerCase()
  let score = 0
  let ti = 0
  let prevMatch = -2
  for (let qi = 0; qi < q.length; qi++) {
    const ch = q[qi]
    let found = -1
    for (let k = ti; k < t.length; k++) {
      if (t[k] === ch) {
        found = k
        break
      }
    }
    if (found === -1) return null
    if (found === prevMatch + 1) score += 5
    if (found === 0 || '/-_. '.includes(t[found - 1])) score += 3
    score += 1
    prevMatch = found
    ti = found + 1
  }
  score -= target.length * 0.01
  return score
}

export interface FuzzyResult<T> {
  item: T
  score: number
}

export function fuzzyFilter<T>(
  query: string,
  items: T[],
  key: (item: T) => string,
): FuzzyResult<T>[] {
  const results: FuzzyResult<T>[] = []
  for (const item of items) {
    const s = fuzzyScore(query, key(item))
    if (s !== null) results.push({ item, score: s })
  }
  results.sort((a, b) => b.score - a.score)
  return results
}
