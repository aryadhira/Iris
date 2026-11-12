import { reactive } from 'vue'
import { api } from '../lib/api'
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
      options.onTranscript?.(text)
      state.phase = 'editing'
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

export const liveSpeakerDeps: SpeakerDeps = {
  speak: (text) => api.speak(text),
  createAudio: createHtmlAudio,
}

export function createSpeakerStore(deps: Partial<SpeakerDeps> = {}) {
  const d: SpeakerDeps = { ...liveSpeakerDeps, ...deps }

  const state = reactive({
    playingId: '' as string,
    loadingId: '' as string,
    errorText: '',
  })

  let current: AudioHandle | null = null
  let generation = 0

  function stop(): void {
    generation += 1
    if (current) {
      current.dispose()
      current = null
    }
    state.playingId = ''
    state.loadingId = ''
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
