import { reactive } from 'vue'
import { ApiError, api } from '../lib/api'
import {
  micErrorMessage,
  noSpeechMessage,
  pickMimeType,
  playbackErrorMessage,
  tooShortMessage,
  transcribeErrorMessage,
  type AudioHandle,
  type PttPhase,
  type SpeakerDeps,
  type VoiceDeps,
  type VoiceRecorder,
} from '../lib/voice'

/** Real browser recorder: MediaRecorder for bytes, AnalyserNode for the meter. */
function createBrowserRecorder(stream: unknown): VoiceRecorder {
  const media = stream as MediaStream
  const chunks: Blob[] = []
  const mime = pickMimeType()
  const recorder = mime ? new MediaRecorder(media, { mimeType: mime }) : new MediaRecorder(media)

  let resolveStop: ((blob: Blob) => void) | null = null
  let context: AudioContext | null = null
  let analyser: AnalyserNode | null = null
  let samples: Uint8Array<ArrayBuffer> | null = null

  try {
    const AudioCtor =
      window.AudioContext ??
      (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext
    if (AudioCtor) {
      context = new AudioCtor()
      if (context.state === 'suspended') void context.resume()
      const source = context.createMediaStreamSource(media)
      analyser = context.createAnalyser()
      analyser.fftSize = 256
      source.connect(analyser)
      samples = new Uint8Array(analyser.frequencyBinCount)
    }
  } catch {
    context = null
    analyser = null
  }

  const cleanup = (): void => {
    try {
      for (const track of media.getTracks()) track.stop()
    } catch {
      // already stopped
    }
    if (context && context.state !== 'closed') void context.close()
    context = null
    analyser = null
  }

  recorder.ondataavailable = (event: BlobEvent) => {
    if (event.data && event.data.size > 0) chunks.push(event.data)
  }
  recorder.onstop = () => {
    const blob = new Blob(chunks, { type: recorder.mimeType || mime || 'audio/webm' })
    const resolve = resolveStop
    resolveStop = null
    cleanup()
    resolve?.(blob)
  }

  return {
    start() {
      recorder.start(200)
    },
    stop() {
      return new Promise<Blob>((resolve) => {
        resolveStop = resolve
        if (recorder.state === 'inactive') {
          const blob = new Blob(chunks, { type: recorder.mimeType || mime || 'audio/webm' })
          cleanup()
          resolve(blob)
          return
        }
        recorder.stop()
      })
    },
    cancel() {
      chunks.length = 0
      resolveStop = null
      if (recorder.state !== 'inactive') recorder.stop()
      cleanup()
    },
    level() {
      if (!analyser || !samples || analyser.getByteTimeDomainData === undefined) return 0
      analyser.getByteTimeDomainData(samples)
      let sum = 0
      let peak = 0
      for (const value of samples) {
        const centered = (value - 128) / 128
        sum += centered * centered
        peak = Math.max(peak, Math.abs(centered))
      }
      const rms = Math.sqrt(sum / samples.length)
      // A peak term keeps quiet speech visible; RMS alone makes a flat row on
      // a phone held at arm's length.
      return Math.min(1, Math.max(rms * 8, peak * 1.6))
    },
  }
}

export const liveVoiceDeps: VoiceDeps = {
  requestMic: async () => {
    if (!navigator.mediaDevices?.getUserMedia) {
      throw Object.assign(new Error('no getUserMedia'), { name: 'NotSupportedError' })
    }
    return navigator.mediaDevices.getUserMedia({
      audio: {
        echoCancellation: true,
        noiseSuppression: true,
        autoGainControl: true,
      },
    })
  },
  createRecorder: createBrowserRecorder,
  transcribe: async (blob) => {
    const body = await api.transcribe(blob)
    if (body.text.trim() === '' && body.reason === 'no_speech') return ''
    return body.text
  },
  stopPlayback: () => {},
  now: () => Date.now(),
  schedule: (fn, ms) => window.setTimeout(fn, ms),
  cancelSchedule: (id) => window.clearTimeout(id),
}

export interface VoiceStoreOptions {
  onTranscript?: (text: string) => void
  stopPlayback?: () => void
}

export function createVoiceStore(
  deps: Partial<VoiceDeps> = {},
  options: VoiceStoreOptions = {},
) {
  const d: VoiceDeps = {
    ...liveVoiceDeps,
    ...deps,
    stopPlayback: options.stopPlayback ?? deps.stopPlayback ?? (() => {}),
  }

  const state = reactive({
    phase: 'idle' as PttPhase,
    elapsedMs: 0,
    level: 0,
    errorText: '',
    locked: false,
  })

  let recorder: VoiceRecorder | null = null
  let stream: unknown = null
  let timer: number | null = null
  let startedAt = 0
  let generation = 0

  function clearTimer(): void {
    if (timer !== null) {
      d.cancelSchedule(timer)
      timer = null
    }
  }

  function tick(): void {
    timer = null
    if (state.phase !== 'recording') return
    state.elapsedMs = d.now() - startedAt
    state.level = recorder?.level() ?? 0
    timer = d.schedule(tick, 100)
  }

  function stopTracks(): void {
    if (!stream) return
    try {
      const tracks = (stream as MediaStream).getTracks?.() ?? []
      for (const track of tracks) track.stop()
    } catch {
      // already gone
    }
    stream = null
  }

  /** Starting a recording is also the barge-in: playback stops first, synchronously. */
  async function start(locked = false): Promise<void> {
    if (state.phase === 'recording' || state.phase === 'transcribing') return
    state.errorText = ''
    d.stopPlayback()

    const token = ++generation
    let media: unknown
    try {
      media = await d.requestMic()
    } catch (error) {
      if (token === generation) {
        state.phase = 'idle'
        state.errorText = micErrorMessage(error)
      }
      return
    }

    if (token !== generation) {
      try {
        for (const track of (media as MediaStream).getTracks?.() ?? []) track.stop()
      } catch {
        // already gone
      }
      return
    }

    try {
      recorder = d.createRecorder(media)
    } catch (error) {
      state.phase = 'idle'
      state.errorText = micErrorMessage(error)
      return
    }

    stream = media
    state.phase = 'recording'
    state.locked = locked
    state.elapsedMs = 0
    state.level = 0
    startedAt = d.now()
    recorder.start()
    clearTimer()
    timer = d.schedule(tick, 100)
  }

  async function stop(): Promise<void> {
    if (state.phase !== 'recording' || !recorder) return
    clearTimer()
    state.level = 0
    state.phase = 'transcribing'
    const record = recorder
    recorder = null

    let blob: Blob
    try {
      blob = await record.stop()
    } catch {
      stopTracks()
      state.phase = 'idle'
      state.errorText = transcribeErrorMessage()
      return
    }
    stopTracks()

    // A quick tap produces a container header and no speech. That is normal,
    // not an error worth red text on the screen.
    if (!blob || blob.size < 900) {
      state.phase = 'idle'
      state.errorText = tooShortMessage()
      return
    }

    try {
      const text = (await d.transcribe(blob)).trim()
      if (text === '') {
        state.phase = 'idle'
        state.errorText = noSpeechMessage()
        return
      }
      // A transcript is not a draft any more: send it immediately. The room
      // handles a failed send by putting the words back in the composer.
      options.onTranscript?.(text)
      state.phase = 'idle'
      state.elapsedMs = 0
      state.locked = false
    } catch {
      state.phase = 'idle'
      state.errorText = transcribeErrorMessage()
    }
  }

  function cancel(): void {
    generation += 1
    clearTimer()
    if (recorder) {
      recorder.cancel()
      recorder = null
    }
    stopTracks()
    state.phase = 'idle'
    state.elapsedMs = 0
    state.level = 0
    state.locked = false
  }

  async function toggle(locked = true): Promise<void> {
    if (state.phase === 'recording') {
      await stop()
      return
    }
    await start(locked)
  }

  function release(): void {
    if (state.phase === 'recording' && !state.locked) void stop()
  }

  function reset(): void {
    if (state.phase === 'editing') state.phase = 'idle'
  }

  function dispose(): void {
    cancel()
  }

  return { state, start, stop, cancel, toggle, release, reset, dispose }
}

export type VoiceStore = ReturnType<typeof createVoiceStore>

/** Default audio handle; tests inject a fake so no real sound is required. */
function createHtmlAudio(blob: Blob): AudioHandle {
  const url = URL.createObjectURL(blob)
  const audio = new Audio(url)
  audio.preload = 'auto'
  return {
    play: () => audio.play(),
    pause: () => audio.pause(),
    set onended(handler: (() => void) | null) {
      audio.onended = handler
    },
    get onended() {
      return audio.onended as (() => void) | null
    },
    dispose: () => {
      audio.pause()
      audio.onended = null
      URL.revokeObjectURL(url)
    },
  }
}

/** Base64 (no data: prefix) -> bytes. Chunks are whole MP3s, not a byte stream. */
function decodeBase64(value: string): ArrayBuffer {
  const binary = atob(value)
  const bytes = new Uint8Array(new ArrayBuffer(binary.length))
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i)
  return bytes.buffer
}

/**
 * Read POST /api/voice/speak/stream as newline-delimited JSON. Audio chunks are
 * yielded in sentence order, so the first sentence starts playing while the
 * second is still being synthesised. The protocol is documented in
 * voice/README.md.
 */
async function* readSpeechStream(
  text: string,
  signal: AbortSignal,
): AsyncGenerator<Blob, void, void> {
  const response = await api.speakStream(text, signal)
  if (!response.ok) {
    throw new ApiError(`Iris menjawab dengan kode ${response.status}.`, response.status)
  }
  if (!response.body) throw new ApiError('Suara tidak bisa dibaca.', response.status)

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  try {
    while (true) {
      const { value, done } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      let newline = buffer.indexOf('\n')
      while (newline >= 0) {
        const line = buffer.slice(0, newline).trim()
        buffer = buffer.slice(newline + 1)
        newline = buffer.indexOf('\n')
        if (line === '') continue
        let frame: { type?: string; audio?: string }
        try {
          frame = JSON.parse(line) as { type?: string; audio?: string }
        } catch {
          continue
        }
        if (frame.type === 'audio' && typeof frame.audio === 'string' && frame.audio !== '') {
          yield new Blob([decodeBase64(frame.audio)], { type: 'audio/mpeg' })
        }
        // type "meta", "error" or "done" carry no audio. A failed sentence is
        // skipped on purpose; the next sentence still plays.
      }
    }
  } finally {
    try {
      await reader.cancel()
    } catch {
      // already closed
    }
  }
}

export const liveSpeakerDeps: SpeakerDeps = {
  speak: (text) => api.speak(text),
  speakStream: (text, signal) => readSpeechStream(text, signal),
  createAudio: createHtmlAudio,
}

export function createSpeakerStore(deps: Partial<SpeakerDeps> = {}) {
  const d: SpeakerDeps = { ...liveSpeakerDeps, ...deps }
  // A test (or a caller) that injects only the buffered speak() is asking for
  // the buffered path, not the live fetch.
  if (deps.speak && !deps.speakStream) d.speakStream = undefined

  const state = reactive({
    playingId: '' as string,
    loadingId: '' as string,
    errorText: '',
  })

  let current: AudioHandle | null = null
  let controller: AbortController | null = null
  let generation = 0

  function stop(): void {
    generation += 1
    controller?.abort()
    controller = null
    if (current) {
      current.dispose()
      current = null
    }
    state.playingId = ''
    state.loadingId = ''
  }

  /** Buffered path: wait for the whole MP3, then play it. */
  async function playBuffered(id: string, text: string, token: number): Promise<void> {
    const blob = await d.speak(text)
    if (token !== generation) return
    if (!blob || blob.size === 0) {
      state.errorText = 'Tidak ada yang bisa dibacakan dari pesan ini.'
      return
    }
    const audio = d.createAudio(blob)
    if (token !== generation) {
      audio.dispose()
      return
    }
    current = audio
    audio.onended = () => {
      if (current === audio) {
        current = null
        state.playingId = ''
      }
    }
    await audio.play()
    if (token !== generation) {
      audio.dispose()
      return
    }
    state.playingId = id
  }

  /**
   * Streaming path: play chunk N while chunk N+1 is still arriving. The queue
   * is drained serially; a chunk that fails to play is skipped, never turned
   * into silence for the rest of the reply.
   */
  async function playStream(id: string, text: string, token: number): Promise<void> {
    const stream = d.speakStream
    if (!stream) {
      await playBuffered(id, text, token)
      return
    }
    controller = new AbortController()
    const signal = controller.signal
    const queue: Blob[] = []
    let playing = false
    let ended = false
    let playedAny = false

    const finishIfIdle = (): void => {
      if (token !== generation) return
      if (ended && !playing && queue.length === 0) {
        state.playingId = ''
        state.loadingId = ''
        // Every chunk failed: say so instead of leaving Boss with silence and
        // a state that simply vanished.
        if (!playedAny) state.errorText = playbackErrorMessage()
      }
    }

    const pump = async (): Promise<void> => {
      if (playing) return
      playing = true
      while (token === generation && queue.length > 0) {
        const blob = queue.shift() as Blob
        try {
          const audio = d.createAudio(blob)
          current = audio
          // "membaca…" already appeared on the first chunk. Keep it while the
          // queue drains and only clear when the last sentence ends.
          await new Promise<void>((resolve) => {
            let settled = false
            const done = (): void => {
              if (settled) return
              settled = true
              resolve()
            }
            const guard = setTimeout(done, 60_000)
            const settle = (): void => {
              clearTimeout(guard)
              done()
            }
            audio.onended = settle
            signal.addEventListener('abort', settle, { once: true })
            audio
              .play()
              .then(() => {
                playedAny = true
                // Do NOT resolve here. play() resolving means the sound STARTED, not finished:
                // resolving here disposes the element (pause + revokeObjectURL) while it is still
                // playing, which is exactly the "clicking the speaker plays nothing" bug.
              })
              .catch(() => {
                // A chunk that cannot start (autoplay refusal, decode failure) is skipped so the
                // next one still plays, but we stop waiting for an 'ended' that will never come.
                audio.onended = null
                settle()
              })
          })
          audio.dispose()
        } catch {
          // Decoder or element failure: move to the next sentence.
        }
        if (current) current = null
      }
      playing = false
      finishIfIdle()
    }

    try {
      for await (const blob of stream(text, signal)) {
        if (token !== generation) return
        if (!state.playingId) {
          // First audio chunk: the state flips now, not at completion.
          state.playingId = id
          state.loadingId = ''
        }
        queue.push(blob)
        void pump()
      }
      ended = true
      finishIfIdle()
    } catch {
      if (token === generation) {
        ended = true
        finishIfIdle()
        if (state.playingId === '') {
          state.errorText = playbackErrorMessage()
        }
      }
    } finally {
      if (controller && controller.signal === signal) controller = null
    }
  }

  async function toggle(id: string, text: string): Promise<void> {
    if (state.playingId === id && current) {
      stop()
      return
    }
    stop()
    const token = generation
    state.errorText = ''
    state.loadingId = id
    try {
      await playStream(id, text, token)
    } catch {
      if (token === generation) {
        state.playingId = ''
        state.errorText = playbackErrorMessage()
      }
    } finally {
      if (state.loadingId === id) state.loadingId = ''
    }
  }

  function dispose(): void {
    stop()
  }

  return { state, toggle, stop, dispose }
}

export type SpeakerStore = ReturnType<typeof createSpeakerStore>
