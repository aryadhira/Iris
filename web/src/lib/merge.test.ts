import { describe, expect, it } from 'vitest'
import { mergeStored } from './merge'
import type { ChatMessage, StoredMessage } from './types'

function stored(id: string, role: string, body: string, at: string): StoredMessage {
  return { id, room_id: 'room-1', role, body, created_at: at }
}

describe('mergeStored', () => {
  it('merges rows by id without duplicating them', () => {
    const rows = [
      stored('u1', 'user', 'halo', '2026-11-12T10:00:00Z'),
      stored('a1', 'assistant', 'hai', '2026-11-12T10:00:05Z'),
    ]
    const once = mergeStored([], rows)
    const twice = mergeStored(once, rows)
    expect(once.map((m) => m.id)).toEqual(['u1', 'a1'])
    expect(twice.map((m) => m.id)).toEqual(['u1', 'a1'])
  })

  it('replaces an optimistic user message with the stored row of the same body', () => {
    const optimistic: ChatMessage = {
      id: 'local-1',
      role: 'user',
      body: 'halo',
      createdAt: '2026-11-12T10:00:00.000Z',
      local: true,
    }
    const merged = mergeStored([optimistic], [stored('u1', 'user', 'halo', '2026-11-12T10:00:01Z')])
    expect(merged).toHaveLength(1)
    expect(merged[0].id).toBe('u1')
    expect(merged[0].local).toBe(false)
  })

  it('adopts a stored reply over a local reply with the same body (stream repeated it)', () => {
    const localReply: ChatMessage = {
      id: 'local-2',
      role: 'assistant',
      body: 'jawaban',
      createdAt: '2026-11-12T10:00:02.000Z',
      local: true,
    }
    const merged = mergeStored(
      [
        { id: 'u1', role: 'user', body: 'tanya', createdAt: '2026-11-12T10:00:00Z' },
        localReply,
      ],
      [
        stored('u1', 'user', 'tanya', '2026-11-12T10:00:00Z'),
        stored('a1', 'assistant', 'jawaban', '2026-11-12T10:00:03Z'),
      ],
    )
    expect(merged.map((m) => m.id)).toEqual(['u1', 'a1'])
    expect(merged[1].local).toBe(false)
  })

  it('updates the body of an already-known message instead of appending', () => {
    const current: ChatMessage[] = [
      { id: 'a1', role: 'assistant', body: 'partial', createdAt: '2026-11-12T10:00:03Z' },
    ]
    const merged = mergeStored(current, [
      stored('a1', 'assistant', 'partial and complete', '2026-11-12T10:00:03Z'),
    ])
    expect(merged).toHaveLength(1)
    expect(merged[0].body).toBe('partial and complete')
  })
})
