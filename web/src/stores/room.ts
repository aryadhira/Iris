import { reactive } from 'vue'
import { api } from '../lib/api'
import { humanizeServerError } from '../lib/errors'
import { mergeStored } from '../lib/merge'
import { frameJson, SseParser, type SseFrame } from '../lib/sse'
import type {
  Approval,
  ChatMessage,
  Continuation,
  FileAttachment,
  StoredMessage,
  TurnPhase,
} from '../lib/types'

const PAGE_SIZE = 200

export interface RoomStoreDeps {
  listMessages: (roomId: string, before?: string) => Promise<StoredMessage[]>
  getContinuation: (roomId: string) => Promise<Continuation | null>
  cancelContinuation: (roomId: string) => Promise<{ cancelled: number }>
  listApprovals: (roomId: string) => Promise<Approval[]>
  resolveApproval: (roomId: string, approvalId: string, choice: string) => Promise<Approval>
  stopRoom: (roomId: string) => Promise<{ stopped: number; reason?: string }>
  sendMessage: (
    roomId: string,
    text: string,
    attachmentIds: string[],
    signal: AbortSignal,
  ) => Promise<Response>
  sleep: (ms: number) => Promise<void>
  reconnectDelays: number[]
  now: () => number
}

export const liveDeps: RoomStoreDeps = {
  listMessages: (roomId, before) => api.listMessages(roomId, { before, limit: PAGE_SIZE }),
  getContinuation: (roomId) => api.getContinuation(roomId),
  cancelContinuation: (roomId) => api.cancelContinuation(roomId),
  listApprovals: (roomId) => api.listApprovals(roomId),
  resolveApproval: (roomId, approvalId, choice) =>
    api.resolveApproval(roomId, approvalId, choice),
  stopRoom: (roomId) => api.stopRoom(roomId),
  sendMessage: (roomId, text, attachmentIds, signal) =>
    api.sendMessage(roomId, text, attachmentIds, signal),
  sleep: (ms) => new Promise((resolve) => window.setTimeout(resolve, ms)),
  // Roughly 35 s of patience: long enough for a stored reply, short enough
  // that the screen stops claiming to retry while nothing is happening.
  reconnectDelays: [800, 1200, 1800, 2500, 3500, 5000, 7000, 9000],
  now: () => Date.now(),
}

let localSeq = 0
function localId(): string {
  localSeq += 1
  return `local-${Date.now()}-${localSeq}`
}

/** The API returns newest first; the screen reads oldest first. */
function oldestFirst(rows: StoredMessage[]): StoredMessage[] {
  return rows.slice().reverse()
}

export interface RoomStoreOptions {
  archived?: boolean
}

export function createRoomStore(
  roomId: string,
  deps: Partial<RoomStoreDeps> = {},
  options: RoomStoreOptions = {},
) {
  const d: RoomStoreDeps = { ...liveDeps, ...deps }

  const state = reactive({
    messages: [] as ChatMessage[],
    loaded: false,
    loading: false,
    loadError: '',
    canLoadOlder: false,
    loadingOlder: false,

    phase: 'idle' as TurnPhase,
    streamingText: '',
    currentTool: '',
    turnStartedAt: 0,
    errorText: '',
    connectionLost: false,

    continuation: null as Continuation | null,
    continuationError: '',

    approvals: [] as Approval[],
    approvalError: '',
    resolvingApproval: '',
    resolvingChoice: '',
    stopping: false,
    stopNotice: '',

    draft: '',
    archived: options.archived ?? false,
  })

  let controller: AbortController | null = null
  let disposed = false

  function resetTurn(): void {
    state.streamingText = ''
    state.currentTool = ''
    state.turnStartedAt = 0
    state.connectionLost = false
    if (state.phase !== 'error') state.phase = 'idle'
  }

  function lastUserIndex(): number {
    for (let i = state.messages.length - 1; i >= 0; i -= 1) {
      if (state.messages[i].role === 'user') return i
    }
    return -1
  }

  /** A reply that arrived after the latest user message, i.e. this turn's. */
  function assistantAfterLastUser(): ChatMessage | undefined {
    const index = lastUserIndex()
    return state.messages.slice(index + 1).find((m) => m.role === 'assistant')
  }

  async function load(): Promise<void> {
    if (state.loading) return
    state.loading = true
    state.loadError = ''
    try {
      const rows = await d.listMessages(roomId)
      state.messages = mergeStored([], oldestFirst(rows))
      state.canLoadOlder = rows.length >= PAGE_SIZE
      state.loaded = true
      await refreshContinuation()
      await refreshApprovals()
    } catch {
      state.loadError = 'Iris tidak bisa memuat pesan room ini. Coba lagi.'
    } finally {
      state.loading = false
    }
  }

  async function loadOlder(): Promise<void> {
    if (state.loadingOlder || state.messages.length === 0) return
    state.loadingOlder = true
    try {
      const rows = await d.listMessages(roomId, state.messages[0].createdAt)
      const older = mergeStored([], oldestFirst(rows))
      const known = new Set(state.messages.map((m) => m.id))
      state.messages = [...older.filter((m) => !known.has(m.id)), ...state.messages]
      state.canLoadOlder = rows.length >= PAGE_SIZE
    } catch {
      state.loadError = 'Pesan lama tidak bisa dimuat. Coba lagi.'
    } finally {
      state.loadingOlder = false
    }
  }

  /**
   * Read the pending follow-up. A row that disappears means the pump ran,
   * was cancelled, or hit a bound; re-reading the mirror is what makes that
   * visible without a manual reload.
   */
  async function refreshContinuation(): Promise<void> {
    try {
      const previous = state.continuation
      const next = await d.getContinuation(roomId)
      state.continuation = next
      state.continuationError = ''
      if (previous && !next) await reconcile()
    } catch {
      state.continuationError = 'Status lanjutan tidak bisa dibaca.'
    }
  }

  async function refreshApprovals(): Promise<void> {
    try {
      state.approvals = await d.listApprovals(roomId)
      if (state.approvalError === 'Status izin tidak bisa dibaca.') state.approvalError = ''
    } catch {
      state.approvalError = 'Status izin tidak bisa dibaca.'
    }
  }

  function upsertApproval(row: Approval): void {
    const index = state.approvals.findIndex((existing) => existing.id === row.id)
    if (index === -1) state.approvals.push(row)
    else state.approvals[index] = row
  }

  /** Answer one parked approval. The card is disabled while this is in flight. */
  async function resolveApproval(id: string, choice: string): Promise<void> {
    if (state.resolvingApproval !== '') return
    state.resolvingApproval = id
    state.resolvingChoice = choice
    state.approvalError = ''
    try {
      const updated = await d.resolveApproval(roomId, id, choice)
      upsertApproval(updated)
    } catch (error) {
      state.approvalError =
        error instanceof Error && error.message.trim() !== ''
          ? error.message
          : 'Izin ini belum bisa dijawab. Coba lagi.'
      await refreshApprovals()
    } finally {
      state.resolvingApproval = ''
      state.resolvingChoice = ''
    }
  }

  /** "hentikan": stop the actual Hermes run, not just this tab's view. */
  async function stopRun(): Promise<void> {
    if (state.stopping) return
    state.stopping = true
    state.stopNotice = ''
    try {
      const result = await d.stopRoom(roomId)
      // On success the transcript gets its own system line; do not repeat it
      // here. A zero-stop answer still needs its reason on screen.
      state.stopNotice =
        result.stopped > 0 ? '' : (result.reason ?? 'tidak ada yang berjalan')
    } catch {
      state.stopNotice = 'Room belum bisa dihentikan. Coba lagi.'
    } finally {
      state.stopping = false
      await reconcile()
      await refreshContinuation()
      await refreshApprovals()
    }
  }

  /** "hentikan": stop the chain now, visibly. */
  async function stopContinuation(): Promise<void> {
    state.continuationError = ''
    try {
      await d.cancelContinuation(roomId)
      state.continuation = null
      await reconcile()
    } catch {
      state.continuationError = 'Lanjutan otomatis tidak bisa dihentikan. Coba lagi.'
    }
  }

  async function send(
    text: string,
    attachments: FileAttachment[] = [],
    onAccepted?: () => void,
  ): Promise<void> {
    const body = text.trim()
    if (body === '' && attachments.length === 0) return
    if (state.archived) return
    if (state.phase === 'streaming' || state.phase === 'reconnecting') return

    state.errorText = ''
    state.connectionLost = false
    state.phase = 'streaming'
    state.streamingText = ''
    state.currentTool = ''
    state.turnStartedAt = d.now()

    // The optimistic bubble already carries the text, so the composer can
    // clear at once; a failed request puts the text back for a retry.
    state.draft = ''

    const optimistic: ChatMessage = {
      id: localId(),
      role: 'user',
      body,
      createdAt: new Date(d.now()).toISOString(),
      attachments: attachments.slice(),
      local: true,
    }
    state.messages.push(optimistic)

    controller = new AbortController()
    let response: Response
    try {
      response = await d.sendMessage(
        roomId,
        body,
        attachments.map((file) => file.id),
        controller.signal,
      )
    } catch {
      state.messages = state.messages.filter((m) => m.id !== optimistic.id)
      state.draft = text
      state.phase = 'error'
      state.errorText = 'Pesan belum terkirim. Periksa koneksi, lalu coba lagi.'
      state.turnStartedAt = 0
      return
    }

    if (!response.ok) {
      const message = await readErrorMessage(response)
      state.messages = state.messages.filter((m) => m.id !== optimistic.id)
      state.draft = text
      state.phase = 'error'
      state.errorText = message
      state.turnStartedAt = 0
      return
    }

    // The server accepted the message; the composer may drop the uploaded
    // chips while the reply streams.
    onAccepted?.()
    await consume(response)
    await refreshContinuation()
    await refreshApprovals()
  }

  async function consume(response: Response): Promise<void> {
    const stream = response.body
    if (!stream) {
      await settle('Balasan tidak bisa dibaca dari server.')
      return
    }

    const reader = stream.getReader()
    const decoder = new TextDecoder()
    const parser = new SseParser()
    let sawDone = false

    try {
      while (!disposed) {
        const { value, done } = await reader.read()
        if (done) break
        const chunk = decoder.decode(value, { stream: true })
        for (const frame of parser.feed(chunk)) {
          if (handleFrame(frame)) sawDone = true
        }
      }
      const tail = decoder.decode()
      if (tail !== '') {
        for (const frame of parser.feed(tail)) {
          if (handleFrame(frame)) sawDone = true
        }
      }
      for (const frame of parser.end()) {
        if (handleFrame(frame)) sawDone = true
      }
    } catch {
      if (!disposed && state.phase !== 'idle') await recoverConnection()
      return
    }

    if (disposed) return

    if (!sawDone) {
      const recovered = await settle(
        'Koneksi berakhir sebelum balasan selesai. Balasan mungkin sudah tersimpan.',
      )
      if (!recovered) {
        keepPartialAsIncomplete()
        if (state.errorText === '') {
          state.errorText =
            'Balasan belum selesai. Iris sudah menyimpan apa yang sempat dikirim; muat ulang room untuk memeriksa.'
        }
        resetTurn()
        state.phase = 'error'
      }
    }
  }

  function handleFrame(frame: SseFrame): boolean {
    switch (frame.event) {
      case 'delta': {
        const payload = frameJson<{ text?: string }>(frame)
        if (payload?.text) {
          if (state.phase !== 'streaming') state.phase = 'streaming'
          if (state.turnStartedAt === 0) state.turnStartedAt = d.now()
          state.streamingText += payload.text
        }
        return false
      }
      case 'tool': {
        const payload = frameJson<{ name?: string; status?: string }>(frame)
        if (payload?.name) state.currentTool = payload.name
        return false
      }
      case 'approval': {
        const payload = frameJson<Approval>(frame)
        if (payload?.id) upsertApproval(payload)
        return false
      }
      case 'done': {
        const payload = frameJson<{ messageId?: string }>(frame)
        finishTurn(payload?.messageId ?? '')
        void reconcile()
        void refreshApprovals()
        return true
      }
      case 'error': {
        const payload = frameJson<{ message?: string }>(frame)
        state.errorText = humanizeServerError(payload?.message ?? '')
        state.currentTool = ''
        state.turnStartedAt = 0
        state.phase = 'error'
        return true
      }
      default:
        return false
    }
  }

  function finishTurn(messageId: string): void {
    const body = state.streamingText
    if (body.trim() !== '') {
      state.messages.push({
        id: messageId !== '' ? messageId : localId(),
        role: 'assistant',
        body,
        createdAt: new Date(d.now()).toISOString(),
        local: messageId === '',
      })
    }
    state.streamingText = ''
    state.currentTool = ''
    state.turnStartedAt = 0
    state.connectionLost = false
    if (state.errorText === '') state.phase = 'idle'
    else state.phase = 'error'
  }

  function keepPartialAsIncomplete(): void {
    if (state.streamingText.trim() === '') return
    state.messages.push({
      id: localId(),
      role: 'assistant',
      body: state.streamingText,
      createdAt: new Date(d.now()).toISOString(),
      local: true,
      incomplete: true,
    })
    state.streamingText = ''
  }

  /** Merge the mirror and adopt a reply if this turn already landed there. */
  async function settle(errorIfMissing: string): Promise<boolean> {
    try {
      const rows = await d.listMessages(roomId)
      state.messages = mergeStored(state.messages, oldestFirst(rows))
      if (assistantAfterLastUser()) {
        state.streamingText = ''
        state.currentTool = ''
        state.turnStartedAt = 0
        state.connectionLost = false
        state.errorText = ''
        state.phase = 'idle'
        return true
      }
    } catch {
      // The read failed too; the caller decides how honest to be about it.
    }
    if (state.phase !== 'error') state.errorText = errorIfMissing
    return false
  }

  async function reconcile(): Promise<void> {
    try {
      const rows = await d.listMessages(roomId)
      state.messages = mergeStored(state.messages, oldestFirst(rows))
    } catch {
      // The stream already showed the text; a stale mirror is not an error.
    }
  }

  /**
   * The turn keeps running server-side after a dropped connection. There is no
   * resume endpoint, so the honest move is to re-read the mirror until the
   * stored reply appears. The message is never POSTed a second time.
   */
  async function recoverConnection(): Promise<void> {
    state.phase = 'reconnecting'
    state.connectionLost = true
    state.currentTool = ''

    for (const delay of d.reconnectDelays) {
      await d.sleep(delay)
      if (disposed) return
      try {
        const rows = await d.listMessages(roomId)
        state.messages = mergeStored(state.messages, oldestFirst(rows))
        if (assistantAfterLastUser()) {
          state.streamingText = ''
          state.currentTool = ''
          state.turnStartedAt = 0
          state.connectionLost = false
          state.errorText = ''
          state.phase = 'idle'
          return
        }
      } catch {
        // Keep retrying; the next attempt may land.
      }
    }

    state.connectionLost = false
    state.turnStartedAt = 0
    state.errorText =
      'Koneksi terputus. Giliran ini mungkin masih berjalan di server — muat ulang room untuk memeriksa.'
    state.phase = 'error'
    keepPartialAsIncomplete()
  }

  /** Re-read the mirror after a failed turn, keeping whatever is stored. */
  async function reload(): Promise<void> {
    state.loading = true
    state.loadError = ''
    try {
      const rows = await d.listMessages(roomId)
      state.messages = mergeStored(state.messages, oldestFirst(rows))
      if (assistantAfterLastUser() && state.phase === 'error') {
        state.errorText = ''
        state.phase = 'idle'
        state.streamingText = ''
      }
    } catch {
      state.loadError = 'Iris belum bisa memuat ulang. Coba lagi.'
    } finally {
      state.loading = false
    }
  }

  function dispose(): void {
    disposed = true
    controller?.abort()
  }

  return {
    roomId,
    state,
    load,
    loadOlder,
    refreshContinuation,
    stopContinuation,
    refreshApprovals,
    resolveApproval,
    stopRun,
    send,
    reload,
    dispose,
  }
}

async function readErrorMessage(response: Response): Promise<string> {
  try {
    const body = (await response.json()) as { error?: string }
    return humanizeServerError(body.error ?? '')
  } catch {
    return `Iris menjawab dengan kode ${response.status}. Coba lagi.`
  }
}

export type RoomStore = ReturnType<typeof createRoomStore>
