import { describe, expect, it, vi } from 'vitest'
import { createSpeakerStore, createVoiceStore } from './voice'
import { formatElapsed, micErrorMessage, type AudioHandle, type VoiceRecorder } from '../lib/voice'

function fakeRecorder(blob = new Blob([new Uint8Array(2000)], { type: 'audio/webm' })): VoiceRecorder {
  return {
    start: vi.fn(),
    stop: vi.fn(async () => blob),
    cancel: vi.fn(),
    level: vi.fn(() => 0.5),
  }
}

function baseDeps(recorder = fakeRecorder()) {
  let clock = 0
  let tick: (() => void) | null = null
  return {
    deps: {
      requestMic: vi.fn(async () => ({ getTracks: () => [] })),
      createRecorder: vi.fn(() => recorder),
      transcribe: vi.fn(async () => 'ingatkan saya besok pagi'),
      stopPlayback: vi.fn(),
      now: () => clock,
      schedule: (fn: () => void) => {
        tick = fn
        return 1
      },
      cancelSchedule: vi.fn(),
    },
    advance(ms: number) {
      clock = ms
      tick?.()
    },
  }
}

describe('push-to-talk state machine', () => {
  it('walks idle → recording → transcribing → editing', async () => {
    const recorder = fakeRecorder()
    const { deps, advance } = baseDeps(recorder)
    const onTranscript = vi.fn()
    const store = createVoiceStore(deps, { onTranscript })

    expect(store.state.phase).toBe('idle')

    await store.start(false)
    expect(store.state.phase).toBe('recording')
    expect(recorder.start).toHaveBeenCalledTimes(1)

    advance(1240)
    expect(store.state.elapsedMs).toBe(1240)
    expect(store.state.level).toBe(0.5)

    const stopping = store.stop()
    expect(store.state.phase).toBe('transcribing')
    await stopping

    expect(recorder.stop).toHaveBeenCalledTimes(1)
    expect(deps.transcribe).toHaveBeenCalledTimes(1)
    expect(onTranscript).toHaveBeenCalledWith('ingatkan saya besok pagi')
    expect(store.state.phase).toBe('editing')
  })

  it('a too-short tap is a normal empty state, not a transcript', async () => {
    const recorder = fakeRecorder(new Blob([new Uint8Array(120)], { type: 'audio/webm' }))
    const { deps } = baseDeps(recorder)
    const onTranscript = vi.fn()
    const store = createVoiceStore(deps, { onTranscript })

    await store.start(false)
    await store.stop()

    expect(onTranscript).not.toHaveBeenCalled()
    expect(store.state.phase).toBe('idle')
    expect(store.state.errorText).toContain('terlalu pendek')
  })

  it('cancel returns to idle without transcribing', async () => {
    const recorder = fakeRecorder()
    const { deps } = baseDeps(recorder)
    const store = createVoiceStore(deps)

    await store.start(true)
    expect(store.state.locked).toBe(true)
    store.cancel()

    expect(recorder.cancel).toHaveBeenCalledTimes(1)
    expect(deps.transcribe).not.toHaveBeenCalled()
    expect(store.state.phase).toBe('idle')
    expect(store.state.locked).toBe(false)
  })
})

describe('barge-in', () => {
  it('stops playback synchronously before the microphone is awaited', async () => {
    const audio: AudioHandle = {
      play: vi.fn(async () => {}),
      pause: vi.fn(),
      dispose: vi.fn(),
      onended: null,
    }
    const speaker = createSpeakerStore({
      speak: vi.fn(async () => new Blob([new Uint8Array(2000)], { type: 'audio/mpeg' })),
      createAudio: vi.fn(() => audio),
    })
    await speaker.toggle('message-1', 'Selesai, port delapan ribu kosong.')
    expect(speaker.state.playingId).toBe('message-1')

    const { deps } = baseDeps()
    const voice = createVoiceStore(deps, { stopPlayback: () => speaker.stop() })

    const starting = voice.start(false)
    // The call must not wait for getUserMedia: playback is already gone.
    expect(speaker.state.playingId).toBe('')
    expect(audio.dispose).toHaveBeenCalledTimes(1)
    await starting
    expect(voice.state.phase).toBe('recording')
  })
})

describe('streaming speech', () => {
  function deferred() {
    let resolve!: () => void
    const promise = new Promise<void>((res) => {
      resolve = res
    })
    return { promise, resolve }
  }

  async function waitFor(condition: () => boolean, timeoutMs = 2000): Promise<void> {
    const deadline = Date.now() + timeoutMs
    while (Date.now() < deadline) {
      if (condition()) return
      await new Promise((resolve) => setTimeout(resolve, 5))
    }
    throw new Error('condition never became true')
  }

  it('flips to "membaca…" on the first chunk, before the stream finishes', async () => {
    const firstArrived = deferred()
    const release = deferred()
    const played: string[] = []
    const blobs = [new Blob(['one']), new Blob(['two'])]
    let finished = false

    async function* stream() {
      yield blobs[0]
      firstArrived.resolve()
      await release.promise
      yield blobs[1]
      finished = true
    }

    let created = 0
    const speaker = createSpeakerStore({
      speakStream: stream,
      speak: vi.fn(async () => new Blob()),
      createAudio: () => {
        const id = String(created++)
        let ended: (() => void) | null = null
        return {
          play: async () => {
            played.push(id)
            await Promise.resolve()
            ended?.()
          },
          pause: vi.fn(),
          dispose: vi.fn(),
          get onended() {
            return ended
          },
          set onended(handler: (() => void) | null) {
            ended = handler
          },
        }
      },
    })

    const running = speaker.toggle('message-1', 'Satu. Dua.')
    await firstArrived.promise
    await waitFor(() => speaker.state.playingId === 'message-1')

    // The state appeared while the second sentence was still being
    // synthesised, which is the whole point of the stream.
    expect(finished).toBe(false)
    expect(speaker.state.playingId).toBe('message-1')

    release.resolve()
    await running
    await waitFor(() => played.length === 2)
    expect(played).toEqual(['0', '1'])
  })

  it('barge-in aborts the whole queue', async () => {
    let aborted = false
    async function* stream(_text: string, signal: AbortSignal) {
      yield new Blob(['one'])
      await new Promise<void>((resolve) => {
        signal.addEventListener(
          'abort',
          () => {
            aborted = true
            resolve()
          },
          { once: true },
        )
      })
      // A well-behaved sidecar would stop here; anything yielded after abort
      // must not play.
      yield new Blob(['two'])
    }

    const disposed = vi.fn()
    const speaker = createSpeakerStore({
      speakStream: stream,
      speak: vi.fn(async () => new Blob()),
      createAudio: () => ({
        play: vi.fn(async () => {}),
        pause: vi.fn(),
        dispose: disposed,
        onended: null,
      }),
    })

    const running = speaker.toggle('message-1', 'Satu. Dua.')
    await waitFor(() => speaker.state.playingId === 'message-1')
    speaker.stop()
    await running
    await waitFor(() => aborted)
    expect(speaker.state.playingId).toBe('')
    expect(speaker.state.loadingId).toBe('')
    expect(disposed).toHaveBeenCalled()
  })

  it('skips a chunk that fails to play and keeps going', async () => {
    async function* stream() {
      yield new Blob(['bad'])
      yield new Blob(['good'])
    }
    const played: string[] = []
    let created = 0
    const speaker = createSpeakerStore({
      speakStream: stream,
      speak: vi.fn(async () => new Blob()),
      createAudio: () => {
        const id = String(created++)
        return {
          play: async () => {
            if (id === '0') throw new Error('decode failed')
            played.push(id)
          },
          pause: vi.fn(),
          dispose: vi.fn(),
          onended: null,
        }
      },
    })

    await speaker.toggle('message-1', 'Satu. Dua.')
    await waitFor(() => played.length === 1)
    expect(played).toEqual(['1'])
    expect(speaker.state.errorText).toBe('')
  })
})

describe('permission copy', () => {
  it('renders an Indonesian HTTPS/Tailscale message, never an English DOM error', async () => {
    const denied = Object.assign(new Error('Permission denied'), { name: 'NotAllowedError' })
    const { deps } = baseDeps()
    deps.requestMic.mockRejectedValueOnce(denied)
    const store = createVoiceStore(deps)

    await store.start(false)

    expect(store.state.phase).toBe('idle')
    expect(store.state.errorText).toContain('Izin mikrofon ditolak')
    expect(store.state.errorText).toContain('HTTPS')
    expect(store.state.errorText).toContain('Tailscale')
    expect(store.state.errorText.toLowerCase()).not.toContain('permission denied')
  })

  it('explains a missing microphone in Indonesian', () => {
    const message = micErrorMessage(Object.assign(new Error('x'), { name: 'NotFoundError' }))
    expect(message).toContain('Tidak ada mikrofon')
  })
})

describe('formatElapsed', () => {
  it('shows elapsed seconds as m:ss', () => {
    expect(formatElapsed(0)).toBe('0:00')
    expect(formatElapsed(3200)).toBe('0:03')
    expect(formatElapsed(65000)).toBe('1:05')
  })
})

describe('speaker keeps the audio element alive while it plays', () => {
  // Regression: playStream used to resolve its wait on play() starting, then call dispose()
  // (pause + revokeObjectURL) while the sound was still playing. The speaker looked broken because
  // it went silent microseconds after starting. The fake here never fires 'ended' on its own, which
  // is what a real 20-second clip does and what the old test double failed to model.
  it('does not dispose the element before playback ends', async () => {
    const dispose = vi.fn()
    const heard: { end: (() => void) | null } = { end: null }
    let paused = false

    async function* stream() {
      yield new Blob(['chunk'], { type: 'audio/mpeg' })
    }

    const speaker = createSpeakerStore({
      speakStream: stream,
      speak: vi.fn(async () => new Blob()),
      createAudio: () => ({
        play: async () => {}, // started, still playing
        pause: () => {
          paused = true
        },
        dispose,
        get onended() {
          return heard.end
        },
        set onended(handler: (() => void) | null) {
          heard.end = handler
        },
      }),
    })

    const playback = speaker.toggle('m1', 'halo')
    const deadline = Date.now() + 2000
    while (Date.now() < deadline && speaker.state.playingId !== 'm1') {
      await new Promise((resolve) => setTimeout(resolve, 5))
    }
    expect(speaker.state.playingId).toBe('m1')

    // give the old behaviour time to bite
    await new Promise((resolve) => setTimeout(resolve, 30))
    expect(paused).toBe(false)
    expect(dispose).not.toHaveBeenCalled()

    heard.end?.()
    await playback
    expect(dispose).toHaveBeenCalled()
  })
})
