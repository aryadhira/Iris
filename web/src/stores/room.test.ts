import { describe, expect, it } from 'vitest'
import { createRoomStore, type RoomStoreDeps } from './room'
import type { StoredMessage } from '../lib/types'

const ROOM = 'room-1'

function stored(
  id: string,
  role: string,
  body: string,
  second: number,
): StoredMessage {
  return {
    id,
    room_id: ROOM,
    role,
    body,
    created_at: `2026-11-12T10:00:${String(second).padStart(2, '0')}Z`,
  }
}

function sse(chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder()
  let index = 0
  return new ReadableStream<Uint8Array>({
    pull(controller) {
      if (index < chunks.length) {
        controller.enqueue(encoder.encode(chunks[index]))
        index += 1
      } else {
        controller.close()
      }
    },
  })
}

function brokenStream(chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder()
  let index = 0
  return new ReadableStream<Uint8Array>({
    pull(controller) {
      if (index < chunks.length) {
        controller.enqueue(encoder.encode(chunks[index]))
        index += 1
      } else {
        controller.error(new Error('network lost'))
      }
    },
  })
}

interface Script {
  list: StoredMessage[][]
  stream: string[]
  failSend?: boolean
  continuation?: {
    id: string
    attempt: number
    status: string
    due_at: string
    deadline: string
  } | null
}

function makeDeps(script: Script) {
  const calls = { list: 0, send: 0, cancel: 0, continuation: 0 }
  const deps: Partial<RoomStoreDeps> = {
    listMessages: async () => {
      const rows = script.list[Math.min(calls.list, script.list.length - 1)]
      calls.list += 1
      return rows
    },
    getContinuation: async () => {
      calls.continuation += 1
      return script.continuation ?? null
    },
    cancelContinuation: async () => {
      calls.cancel += 1
      return { cancelled: 1 }
    },
    sendMessage: async () => {
      calls.send += 1
      if (script.failSend) throw new TypeError('fetch failed')
      return new Response(sse(script.stream), { status: 200 })
    },
    sleep: async () => {},
    reconnectDelays: [0, 0],
    now: () => Date.parse('2026-11-12T11:00:00Z'),
  }
  return { deps, calls }
}

async function tick(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await new Promise((resolve) => setTimeout(resolve, 0))
}

const DELTA_DONE = [
  'event: delta\ndata: {"text":"ja"}\n\n',
  'event: delta\ndata: {"text":"waban"}\n\n',
  'event: done\ndata: {"messageId":"a2"}\n\n',
]

describe('room store', () => {
  it('paints stored messages oldest first', async () => {
    const { deps } = makeDeps({
      // The API returns newest first.
      list: [[stored('a1', 'assistant', 'hai', 5), stored('u1', 'user', 'halo', 1)]],
      stream: DELTA_DONE,
    })
    const store = createRoomStore(ROOM, deps)
    await store.load()
    expect(store.state.messages.map((m) => m.id)).toEqual(['u1', 'a1'])
    expect(store.state.loaded).toBe(true)
  })

  it('does not duplicate a stored message the stream repeats', async () => {
    const first = [stored('u1', 'user', 'halo', 1)]
    const afterTurn = [
      stored('a2', 'assistant', 'jawaban', 9),
      stored('u2', 'user', 'tanya', 6),
      stored('u1', 'user', 'halo', 1),
    ]
    const { deps, calls } = makeDeps({ list: [first, afterTurn], stream: DELTA_DONE })

    const store = createRoomStore(ROOM, deps)
    await store.load()
    await store.send('tanya')
    await tick()

    const ids = store.state.messages.map((m) => m.id)
    expect(ids).toEqual(['u1', 'u2', 'a2'])
    expect(new Set(ids).size).toBe(ids.length)
    expect(store.state.messages.filter((m) => m.role === 'user')).toHaveLength(2)
    expect(store.state.messages.filter((m) => m.role === 'assistant')).toHaveLength(1)
    expect(store.state.phase).toBe('idle')
    expect(calls.send).toBe(1)
  })

  it('adopts a stored reply without a done messageId instead of doubling it', async () => {
    const first = [stored('u1', 'user', 'halo', 1)]
    const afterTurn = [
      stored('a2', 'assistant', 'jawaban', 9),
      stored('u2', 'user', 'tanya', 6),
      stored('u1', 'user', 'halo', 1),
    ]
    const { deps } = makeDeps({
      list: [first, afterTurn],
      stream: [
        'event: delta\ndata: {"text":"jawaban"}\n\n',
        'event: done\ndata: {"messageId":""}\n\n',
      ],
    })

    const store = createRoomStore(ROOM, deps)
    await store.load()
    await store.send('tanya')
    await tick()

    expect(store.state.messages.map((m) => m.id)).toEqual(['u1', 'u2', 'a2'])
    expect(store.state.messages.filter((m) => m.role === 'assistant')).toHaveLength(1)
  })

  it('shows a reconnecting state, then adopts the reply the server finished', async () => {
    const first = [stored('u1', 'user', 'halo', 1)]
    const userOnly = [stored('u2', 'user', 'tanya', 6), ...first]
    const withReply = [stored('a2', 'assistant', 'jawaban', 9), ...userOnly]

    const observed: string[] = []
    const { deps } = makeDeps({
      list: [first, userOnly, withReply],
      stream: DELTA_DONE,
    })
    const originalList = deps.listMessages
    let listed = 0
    deps.listMessages = async (roomId, before) => {
      listed += 1
      const rows = await originalList!(roomId, before)
      return rows
    }
    deps.sendMessage = async () =>
      new Response(
        brokenStream(['event: delta\ndata: {"text":"ja"}\n\n', 'event: tool\ndata: {"name":"_thinking"}\n\n']),
        { status: 200 },
      )
    deps.sleep = async () => {
      observed.push('sleep')
    }

    const store = createRoomStore(ROOM, deps)
    await store.load()
    await store.send('tanya')

    expect(observed.length).toBeGreaterThan(0)
    expect(listed).toBeGreaterThanOrEqual(3)
    expect(store.state.phase).toBe('idle')
    expect(store.state.connectionLost).toBe(false)
    expect(store.state.streamingText).toBe('')
    expect(store.state.messages.map((m) => m.id)).toEqual(['u1', 'u2', 'a2'])
    expect(store.state.messages.filter((m) => m.id === 'a2')).toHaveLength(1)
  })

  it('keeps the typed text and adds no message when the send never lands', async () => {
    const { deps } = makeDeps({
      list: [[stored('u1', 'user', 'halo', 1)]],
      stream: DELTA_DONE,
      failSend: true,
    })
    const store = createRoomStore(ROOM, deps)
    await store.load()
    await store.send('pesan penting')

    expect(store.state.phase).toBe('error')
    expect(store.state.draft).toBe('pesan penting')
    expect(store.state.errorText).not.toBe('')
    expect(store.state.messages.map((m) => m.id)).toEqual(['u1'])
  })

  it('keeps a partial reply honest when the connection never recovers', async () => {
    const first = [stored('u1', 'user', 'halo', 1)]
    const { deps } = makeDeps({
      list: [first, first, first],
      stream: DELTA_DONE,
    })
    deps.sendMessage = async () =>
      new Response(brokenStream(['event: delta\ndata: {"text":"sebagian"}\n\n']), { status: 200 })

    const store = createRoomStore(ROOM, deps)
    await store.load()
    await store.send('tanya')

    expect(store.state.phase).toBe('error')
    expect(store.state.streamingText).toBe('')
    const partial = store.state.messages.find((m) => m.incomplete)
    expect(partial?.body).toBe('sebagian')
  })

  it('shows the pending follow-up and cancels it through the API', async () => {
    const first = [stored('u1', 'user', 'halo', 1)]
    const { deps, calls } = makeDeps({
      list: [first, first],
      stream: DELTA_DONE,
      continuation: {
        id: 'c1',
        attempt: 1,
        status: 'pending',
        due_at: '2026-11-12T11:00:20Z',
        deadline: '2026-11-12T11:30:00Z',
      },
    })
    const store = createRoomStore(ROOM, deps)
    await store.load()
    expect(store.state.continuation?.attempt).toBe(1)
    expect(calls.continuation).toBeGreaterThan(0)

    await store.stopContinuation()
    expect(calls.cancel).toBe(1)
    expect(store.state.continuation).toBe(null)
  })
})
