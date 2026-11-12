import type { SearchResult } from './types'

export interface SnippetPart {
  text: string
  hit: boolean
}

/**
 * ts_headline marks matches with <mark>…</mark> but leaves the message text
 * untouched. Escape everything first, then restore only those exact tags, so a
 * message containing HTML can never become HTML in the rooms panel.
 */
export function snippetParts(raw: string): SnippetPart[] {
  const escaped = raw
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')

  const parts: SnippetPart[] = []
  let hit = false
  for (const token of escaped.split(/(&lt;mark&gt;|&lt;\/mark&gt;)/)) {
    if (token === '&lt;mark&gt;') {
      hit = true
      continue
    }
    if (token === '&lt;/mark&gt;') {
      hit = false
      continue
    }
    if (token !== '') parts.push({ text: token, hit })
  }
  return parts
}

export function roleLabel(role: string): string {
  switch (role) {
    case 'user':
      return 'Kamu'
    case 'assistant':
      return 'Hermes'
    default:
      return 'Sistem'
  }
}

export function resultTime(result: SearchResult): string {
  const when = Date.parse(result.createdAt)
  if (Number.isNaN(when)) return ''
  return new Intl.DateTimeFormat('id-ID', {
    day: 'numeric',
    month: 'short',
    hour: '2-digit',
    minute: '2-digit',
  }).format(when)
}
