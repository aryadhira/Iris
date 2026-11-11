import type { ChatMessage, StoredMessage } from './types'

export function isLocalId(id: string): boolean {
  return id.startsWith('local-')
}

export function messageFromStored(row: StoredMessage): ChatMessage {
  return {
    id: row.id,
    role: row.role === 'user' ? 'user' : row.role === 'system' ? 'system' : 'assistant',
    body: row.body,
    createdAt: row.created_at,
    local: false,
  }
}

/**
 * Merge rows fetched from the server into the messages already on screen.
 *
 * The rule that matters: a message the stream already showed must not appear
 * twice once the server repeats it. A row is therefore matched by id first,
 * and then — for messages that only exist locally (optimistic user message,
 * a reply confirmed without a messageId) — by role and body.
 */
export function mergeStored(current: ChatMessage[], incoming: StoredMessage[]): ChatMessage[] {
  const merged = current.slice()
  const seen = new Set(merged.map((m) => m.id))

  for (const row of incoming) {
    if (seen.has(row.id)) {
      const index = merged.findIndex((m) => m.id === row.id)
      if (index !== -1) {
        merged[index] = {
          ...merged[index],
          body: row.body,
          role: messageFromStored(row).role,
          local: false,
          incomplete: false,
        }
      }
      continue
    }

    const localIndex = merged.findIndex(
      (m) => isLocalId(m.id) && m.role === messageFromStored(row).role && sameBody(m.body, row.body),
    )
    if (localIndex !== -1) {
      merged[localIndex] = messageFromStored(row)
      seen.add(row.id)
      continue
    }

    merged.push(messageFromStored(row))
    seen.add(row.id)
  }

  return merged
}

function sameBody(a: string, b: string): boolean {
  return a === b || a.trim() === b.trim()
}
