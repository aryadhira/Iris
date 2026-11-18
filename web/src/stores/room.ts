import { reactive } from 'vue'
import { api } from '../lib/api'
import { humanizeServerError } from '../lib/errors'
import { mergeStored } from '../lib/merge'
import { ReplyStream } from '../lib/reply'
import { frameJson, SseParser, type SseFrame } from '../lib/sse'
import type {
  Approval,
  ChatMessage,
  Continuation,
  FileAttachment,
  ProgressStep,
  RoomRun,
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
  openEvents: (roomId: string, signal: AbortSignal) => Promise<Response>
  retryMessage: (roomId: string, messageId: string, signal: AbortSignal) => Promise<Response>
  sleep: (ms: number) => Promise<void>
  /** How often to check the live stream for silence; 0 disables the watchdog (tests do this). */
  watchdogMs?: number
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
  openEvents: (roomId, signal) => api.openRoomEvents(roomId, signal),
  retryMessage: (roomId, messageId, signal) => api.retryMessage(roomId, messageId, signal),
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

/** A streaming row belongs in the live bubble, never in the settled transcript. */
function visibleOldestFirst(rows: StoredMessage[]): StoredMessage[] {
  return oldestFirst(rows).filter((row) => row.state !== 'streaming')
}

const LIVE_RUN_STATUSES = new Set([
  'queued',
  'running',
  'waiting_for_approval',
  'stopping',
])

function isRunLive(run: RoomRun | null | undefined): boolean {
  return Boolean(run && LIVE_RUN_STATUSES.has(run.status))
}

export interface LiveSnapshot {
  run?: RoomRun | null
  partial?: string
  approvals?: Approval[]
  reminders_due?: number
  steps?: ProgressStep[]
  last_event_age_ms?: number
}

export interface RoomStoreOptions {
  archived?: boolean
  /** The cold-load run from GET /api/rooms/{id}; the header uses it before any stream. */
  run?: RoomRun | null
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
    run: (options.run ?? null) as RoomRun | null,
    streamingText: '',
    currentTool: '',
    // The live working log. Persisted on the message at the end; cleared here
    // when the turn settles so the card folds into the stored timeline once.
    steps: [] as ProgressStep[],
    /** Age of the last real event at the moment the last activity frame arrived. */
    activityAgeMs: 0,
    activityAt: 0,
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
    reminderNotice: '',

    draft: '',
    archived: options.archived ?? false,
  })

  let controller: AbortController | null = null
  // One cleaner per reply. It holds a trailing "--" until the next chunk
  // decides whether it is an em dash or part of a command-line flag.
  let cleaner: ReplyStream | null = null
  let disposed = false
  let liveController: AbortController | null = null
  let livePromise: Promise<void> | null = null
  let attempts = 0
  let lastLiveByteAt = 0
  let liveConnected = false

  function applyRun(run: RoomRun | null): void {
    state.run = run
    if (run && isRunLive(run)) {
      state.phase = 'streaming'
      const started = Date.parse(run.started_at)
      if (Number.isFinite(started)) state.turnStartedAt = started
    }
  }

  applyRun(options.run ?? null)

  function resetSteps(): void {
    state.steps = []
    state.activityAgeMs = 0
    state.activityAt = 0
  }

  /** A delta, a tool or a step is real work: the silence clock starts again. */
  function noteActivity(): void {
    state.activityAgeMs = 0
    state.activityAt = d.now()
  }

  /** Steps are addressed by seq; a later frame corrects the same row. */
  function upsertStep(step: ProgressStep): void {
    const index = state.steps.findIndex((row) => row.seq === step.seq)
    if (index === -1) {
      state.steps = [...state.steps, step].sort((a, b) => a.seq - b.seq)
    } else {
      state.steps[index] = step
    }
  }

  function resetTurn(): void {
    cleaner = null
    state.streamingText = ''
    state.currentTool = ''
    resetSteps()
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
      const visible = visibleOldestFirst(rows)
      state.messages = mergeStored([], visible)
      state.canLoadOlder = rows.length >= PAGE_SIZE
      // A reload mid-turn must show what has been written so far. The live
      // snapshot will replace this with the exact hub text a moment later.
      const streamingRow = oldestFirst(rows).find((row) => row.state === 'streaming')
      if (streamingRow) {
        state.streamingText = streamingRow.body
        if (isRunLive(state.run)) state.phase = 'streaming'
      }
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
      const older = mergeStored([], visibleOldestFirst(rows))
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
    state.reminderNotice = ''
    state.phase = 'streaming'
    state.streamingText = ''
    state.currentTool = ''
    resetSteps()
    noteActivity()
    state.turnStartedAt = d.now()
    cleaner = new ReplyStream()

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
      // The guard writes a visible system line in the room. Pull it in so the
      // refusal is readable in the transcript, not only in the composer.
      await reconcile()
      return
    }

    // The server accepted the message; the composer may drop the uploaded
    // chips while the reply streams.
    onAccepted?.()
    await consume(response, !liveConnected)
    await refreshContinuation()
    await refreshApprovals()
  }

  /**
   * Re-run the turn behind an existing user message. The server reuses that
   * row, marks the old reply `superseded` and streams a new answer. Nothing is
   * posted twice: a resend would leave a duplicate user message.
   */
  async function retry(userMessageId: string): Promise<void> {
    if (state.archived) return
    if (state.phase === 'streaming' || state.phase === 'reconnecting') return
    if (userMessageId === '') return

    state.errorText = ''
    state.connectionLost = false
    state.phase = 'streaming'
    state.streamingText = ''
    state.currentTool = ''
    resetSteps()
    noteActivity()
    state.turnStartedAt = d.now()
    cleaner = new ReplyStream()

    // Dim the reply that is about to be replaced right away, so the transcript
    // never shows two current answers while the new one streams.
    let seen = false
    for (const message of state.messages) {
      if (message.id === userMessageId) {
        seen = true
        continue
      }
      if (!seen) continue
      if (message.role === 'user') break
      if (message.role === 'assistant') {
        message.meta = { ...(message.meta ?? {}), superseded: true }
      }
    }

    controller = new AbortController()
    let response: Response
    try {
      response = await d.retryMessage(roomId, userMessageId, controller.signal)
    } catch {
      state.phase = 'error'
      state.errorText = 'Pesan ini belum bisa diulang. Periksa koneksi lalu coba lagi.'
      state.turnStartedAt = 0
      // Undo the optimistic dim: no new reply exists to replace the old one.
      await reconcile()
      return
    }
    if (!response.ok) {
      state.phase = 'error'
      state.errorText = await readErrorMessage(response)
      state.turnStartedAt = 0
      await reconcile()
      return
    }

    await consume(response, !liveConnected)
    await reconcile()
    await refreshContinuation()
    await refreshApprovals()
  }

  /**
   * Re-open the live stream now, skipping the backoff.
   *
   * A phone that is backgrounded does not get an error on its stream: Android freezes the tab, the socket dies,
   * and `reader.read()` simply never resolves. Without this the room sat on a zombie connection and looked idle
   * until a manual reload, which is exactly what Boss reported when he left the app and came back.
   */
  function resync(reason: string): void {
    if (disposed) return
    const controller = liveController
    liveController = null
    liveConnected = false
    attempts = 0
    lastLiveByteAt = d.now()
    try {
      controller?.abort()
    } catch {
      // aborting an already-dead controller is not an error
    }
    if (reason === 'visible') {
      void reconcile()
      void refreshApprovals()
      void refreshContinuation()
    }
  }

  /** The stream is dead if it produced no bytes (not even a heartbeat) for this long. */
  const LIVE_SILENCE_MS = 45_000
  let watchdog: number | undefined

  function startWatchdog(): void {
    const every = d.watchdogMs ?? 0
    if (every <= 0 || watchdog !== undefined || typeof window === 'undefined') return
    watchdog = window.setInterval(() => {
      if (disposed || !liveConnected) return
      if (document.visibilityState !== 'visible') return
      if (d.now() - lastLiveByteAt > LIVE_SILENCE_MS) resync('silent')
    }, every)
  }

  /** Attach to the room hub. Reconnects forever until the room unmounts. */
  function startLive(): void {
    if (disposed || livePromise) return
    livePromise = runLive()
    startWatchdog()
  }

  async function runLive(): Promise<void> {
    while (!disposed) {
      liveController = new AbortController()
      try {
        const response = await d.openEvents(roomId, liveController.signal)
        if (!response.ok || !response.body) {
          throw new Error(`events answered ${response.status}`)
        }
        liveConnected = true
        lastLiveByteAt = d.now()
        if (state.connectionLost) {
          state.connectionLost = false
          if (state.phase === 'reconnecting') state.phase = 'streaming'
        }
        await consumeLive(response)
      } catch {
        // A dropped phone or a restarting irisd is not an error in the room:
        // reconnect and let the snapshot tell the truth.
      }
      liveConnected = false
      liveController = null
      if (disposed) return
      attempts += 1
      if (isRunLive(state.run)) {
        state.phase = 'reconnecting'
        state.connectionLost = true
      }
      const delay =
        d.reconnectDelays[Math.min(attempts, d.reconnectDelays.length - 1)] ?? 5000
      await d.sleep(delay)
    }
  }

  async function consumeLive(response: Response): Promise<void> {
    const stream = response.body
    if (!stream) return
    const reader = stream.getReader()
    const decoder = new TextDecoder()
    const parser = new SseParser()
    try {
      while (!disposed) {
        const { value, done } = await reader.read()
        // Any bytes — including the 15 s heartbeat comment — prove the stream is alive. A frozen
        // phone tab resolves neither value nor error, so this timestamp is the only liveness signal.
        lastLiveByteAt = d.now()
        if (done) break
        for (const frame of parser.feed(decoder.decode(value, { stream: true }))) {
          handleLiveFrame(frame)
        }
      }
      const tail = decoder.decode()
      if (tail !== '') {
        for (const frame of parser.feed(tail)) handleLiveFrame(frame)
      }
      for (const frame of parser.end()) handleLiveFrame(frame)
    } finally {
      try {
        reader.releaseLock()
      } catch {
        // the stream is already gone
      }
    }
  }

  function handleLiveFrame(frame: SseFrame): void {
    switch (frame.event) {
      case 'snapshot': {
        const payload = frameJson<LiveSnapshot>(frame)
        if (!payload) return
        applyRun(payload.run ?? null)
        for (const approval of payload.approvals ?? []) upsertApproval(approval)
        if (isRunLive(payload.run)) {
          state.streamingText = payload.partial ?? ''
          state.steps = (payload.steps ?? [])
            .slice()
            .sort((a, b) => a.seq - b.seq)
          state.activityAgeMs = Math.max(0, payload.last_event_age_ms ?? 0)
          state.activityAt = d.now()
          state.phase = 'streaming'
          state.connectionLost = false
          const started = payload.run ? Date.parse(payload.run.started_at) : NaN
          if (Number.isFinite(started)) state.turnStartedAt = started
        } else {
          state.streamingText = ''
          if (state.phase === 'streaming' || state.phase === 'reconnecting') {
            state.phase = state.errorText === '' ? 'idle' : 'error'
          }
          state.currentTool = ''
          resetSteps()
          state.turnStartedAt = 0
          state.connectionLost = false
          // A reconnect can land after the one `done` frame was published. The
          // mirror is the fallback: adopt the final row instead of leaving the
          // room without the answer until a manual reload.
          void reconcile()
        }
        return
      }
      case 'run': {
        // Announced when a turn starts (a retry, another device, or a continuation). Adopt it so the room
        // says "bekerja" immediately rather than waiting for a reconnect to fetch a snapshot.
        const payload = frameJson<{ run?: RoomRun; messageId?: string }>(frame)
        if (payload?.run) {
          state.run = payload.run
          state.phase = 'streaming'
          if (state.turnStartedAt === 0) state.turnStartedAt = d.now()
          noteActivity()
        }
        return
      }
      case 'step': {
        const payload = frameJson<ProgressStep>(frame)
        if (payload?.seq) {
          upsertStep(payload)
          noteActivity()
        }
        return
      }
      case 'activity': {
        const payload = frameJson<{ ageMs?: number }>(frame)
        state.activityAgeMs = Math.max(0, payload?.ageMs ?? 0)
        state.activityAt = d.now()
        return
      }
      case 'delta': {
        const payload = frameJson<{ text?: string }>(frame)
        if (payload?.text) {
          // The server already ran the reply cleaner; appending here must not
          // clean a second time or it would swallow a legitimate em dash.
          state.streamingText += payload.text
          state.phase = 'streaming'
          if (state.turnStartedAt === 0) state.turnStartedAt = d.now()
          noteActivity()
        }
        return
      }
      case 'tool': {
        const payload = frameJson<{ name?: string; status?: string }>(frame)
        if (payload?.name) state.currentTool = payload.name
        noteActivity()
        return
      }
      case 'approval':
      case 'approval.resolved': {
        const payload = frameJson<Approval>(frame)
        if (payload?.id) upsertApproval(payload)
        return
      }
      case 'note': {
        const payload = frameJson<{ body?: string }>(frame)
        if (payload?.body) {
          state.messages.push({
            id: localId(),
            role: 'system',
            body: payload.body,
            createdAt: new Date(d.now()).toISOString(),
            local: true,
          })
          void reconcile()
        }
        return
      }
      case 'reminder': {
        const payload = frameJson<{ due_at?: string }>(frame)
        if (payload?.due_at) state.reminderNotice = reminderNotice(payload.due_at)
        return
      }
      case 'done': {
        const payload = frameJson<{ messageId?: string }>(frame)
        void finishLive(payload?.messageId ?? '')
        return
      }
      case 'error': {
        const payload = frameJson<{ message?: string }>(frame)
        state.errorText = humanizeServerError(payload?.message ?? '')
        state.currentTool = ''
        resetSteps()
        state.turnStartedAt = 0
        state.phase = 'error'
        return
      }
      default:
        return
    }
  }

  async function finishLive(messageId: string): Promise<void> {
    const body = state.streamingText
    cleaner = null
    state.streamingText = ''
    state.currentTool = ''
    resetSteps()
    state.turnStartedAt = 0
    state.connectionLost = false
    state.run = null
    if (state.errorText === '') state.phase = 'idle'
    await reconcile()
    if (
      messageId !== '' &&
      body.trim() !== '' &&
      !state.messages.some((message) => message.id === messageId)
    ) {
      state.messages.push({
        id: messageId,
        role: 'assistant',
        body,
        state: 'done',
        createdAt: new Date(d.now()).toISOString(),
      })
    }
    await refreshContinuation()
    await refreshApprovals()
  }

  async function consume(response: Response, render = true): Promise<void> {
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
          if (render && handleFrame(frame)) sawDone = true
        }
      }
      const tail = decoder.decode()
      if (tail !== '') {
        for (const frame of parser.feed(tail)) {
          if (render && handleFrame(frame)) sawDone = true
        }
      }
      for (const frame of parser.end()) {
        if (render && handleFrame(frame)) sawDone = true
      }
    } catch {
      if (render && !disposed && state.phase !== 'idle') await recoverConnection()
      return
    }

    if (disposed || !render) return

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
      case 'step': {
        const payload = frameJson<ProgressStep>(frame)
        if (payload?.seq) {
          upsertStep(payload)
          noteActivity()
        }
        return false
      }
      case 'activity': {
        const payload = frameJson<{ ageMs?: number }>(frame)
        state.activityAgeMs = Math.max(0, payload?.ageMs ?? 0)
        state.activityAt = d.now()
        return false
      }
      case 'delta': {
        const payload = frameJson<{ text?: string }>(frame)
        if (payload?.text) {
          if (state.phase !== 'streaming') state.phase = 'streaming'
          if (state.turnStartedAt === 0) state.turnStartedAt = d.now()
          noteActivity()
          if (!cleaner) cleaner = new ReplyStream()
          const visible = cleaner.feed(payload.text)
          if (visible !== '') state.streamingText += visible
        }
        return false
      }
      case 'tool': {
        const payload = frameJson<{ name?: string; status?: string }>(frame)
        if (payload?.name) state.currentTool = payload.name
        noteActivity()
        return false
      }
      case 'approval': {
        const payload = frameJson<Approval>(frame)
        if (payload?.id) upsertApproval(payload)
        return false
      }
      case 'reminder': {
        const payload = frameJson<{ due_at?: string }>(frame)
        if (payload?.due_at) state.reminderNotice = reminderNotice(payload.due_at)
        return false
      }
      case 'done': {
        const payload = frameJson<{ messageId?: string }>(frame)
        if (cleaner) {
          const rest = cleaner.flush()
          if (rest !== '') state.streamingText += rest
          cleaner = null
        }
        finishTurn(payload?.messageId ?? '')
        void reconcile()
        void refreshApprovals()
        return true
      }
      case 'error': {
        const payload = frameJson<{ message?: string }>(frame)
        state.errorText = humanizeServerError(payload?.message ?? '')
        state.currentTool = ''
        resetSteps()
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
    cleaner = null
    state.streamingText = ''
    state.currentTool = ''
    resetSteps()
    state.turnStartedAt = 0
    state.connectionLost = false
    state.run = null
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
      state.messages = mergeStored(state.messages, visibleOldestFirst(rows))
      if (assistantAfterLastUser()) {
        cleaner = null
        state.streamingText = ''
        state.currentTool = ''
        resetSteps()
        state.turnStartedAt = 0
        state.connectionLost = false
        state.run = null
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
      state.messages = mergeStored(state.messages, visibleOldestFirst(rows))
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
        state.messages = mergeStored(state.messages, visibleOldestFirst(rows))
        if (assistantAfterLastUser()) {
          cleaner = null
          state.streamingText = ''
          state.currentTool = ''
          resetSteps()
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
    resetSteps()
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
      state.messages = mergeStored(state.messages, visibleOldestFirst(rows))
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

  function clearReminderNotice(): void {
    state.reminderNotice = ''
  }

  function dispose(): void {
    disposed = true
    controller?.abort()
    liveController?.abort()
  }

  return {
    roomId,
    state,
    load,
    loadOlder,
    startLive,
    refreshContinuation,
    stopContinuation,
    refreshApprovals,
    resolveApproval,
    stopRun,
    resync,
    send,
    retry,
    reload,
    clearReminderNotice,
    dispose,
  }
}

function reminderNotice(dueAt: string): string {
  const due = new Date(dueAt)
  if (Number.isNaN(due.getTime())) return 'Pengingat dibuat.'
  const time = new Intl.DateTimeFormat('id-ID', {
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
  }).format(due)
  return `Pengingat dibuat untuk ${time}`
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
