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
