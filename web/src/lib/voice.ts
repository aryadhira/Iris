/**
 * Voice helpers that are safe to test without a browser.
 *
 * The microphone needs a secure context. localhost is one; a LAN IP over plain
 * http is not. On a phone the only reliable path is the Tailscale HTTPS name.
 */

export type PttPhase = 'idle' | 'recording' | 'transcribing' | 'editing'

export interface VoiceRecorder {
  start(): void
  stop(): Promise<Blob>
  cancel(): void
  level(): number
}

export interface VoiceDeps {
  requestMic: () => Promise<unknown>
  createRecorder: (stream: unknown) => VoiceRecorder
  transcribe: (blob: Blob) => Promise<string>
  stopPlayback: () => void
  now: () => number
  schedule: (fn: () => void, ms: number) => number
  cancelSchedule: (id: number) => void
}

export interface AudioHandle {
  play(): Promise<void>
  pause(): void
  onended: (() => void) | null
  dispose(): void
}

export interface SpeakerDeps {
  speak: (text: string) => Promise<Blob>
  /**
   * Streaming speech. Each yielded Blob is one sentence's audio, in order.
   * Omitting it falls back to the buffered speak() path.
   */
  speakStream?: (text: string, signal: AbortSignal) => AsyncIterable<Blob>
  createAudio: (blob: Blob) => AudioHandle
}

export const RECORDING_MIME_CANDIDATES = [
  'audio/webm;codecs=opus',
  'audio/webm',
  'audio/ogg;codecs=opus',
  'audio/mp4',
]

export function pickMimeType(): string {
  if (typeof MediaRecorder === 'undefined' || typeof MediaRecorder.isTypeSupported !== 'function') {
    return ''
  }
  return RECORDING_MIME_CANDIDATES.find((type) => MediaRecorder.isTypeSupported(type)) ?? ''
}

export function formatElapsed(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000))
  const minutes = Math.floor(total / 60)
  const seconds = total % 60
  return `${minutes}:${String(seconds).padStart(2, '0')}`
}

/**
 * Indonesian copy, never an English DOMException string. Permission denied and
 * an insecure origin are the two failures Boss will actually meet.
 */
export function micErrorMessage(error: unknown): string {
  const name =
    typeof error === 'object' && error !== null && 'name' in error
      ? String((error as { name?: unknown }).name ?? '')
      : ''

  switch (name) {
    case 'NotAllowedError':
    case 'SecurityError':
      return 'Izin mikrofon ditolak. Mikrofon hanya bisa dipakai lewat HTTPS — buka alamat Tailscale Iris (https://…), bukan IP LAN biasa, lalu izinkan mikrofon di browser.'
    case 'NotFoundError':
    case 'DevicesNotFoundError':
      return 'Tidak ada mikrofon yang terdeteksi di perangkat ini. Sambungkan mikrofon lalu coba lagi.'
    case 'NotReadableError':
    case 'TrackStartError':
      return 'Mikrofon sedang dipakai aplikasi lain. Tutup aplikasi itu lalu coba lagi.'
    case 'OverconstrainedError':
      return 'Mikrofon ini tidak mendukung pengaturan rekaman yang diminta. Coba mikrofon lain.'
    case 'NotSupportedError':
      return 'Browser ini tidak mendukung rekaman suara. Buka Iris lewat HTTPS di Chrome atau Safari terbaru.'
    default:
      return 'Mikrofon tidak bisa dibuka. Pastikan Iris dibuka lewat HTTPS (alamat Tailscale), bukan IP LAN biasa, lalu izinkan mikrofon di browser.'
  }
}

export function transcribeErrorMessage(): string {
  return 'Suara belum bisa diubah jadi teks. Periksa koneksi lalu coba lagi.'
}

export function tooShortMessage(): string {
  return 'Rekamannya terlalu pendek. Tahan tombolnya sebentar sambil bicara.'
}

export function noSpeechMessage(): string {
  return 'Tidak ada suara yang terdengar. Coba lagi dengan suara yang lebih jelas.'
}

export function playbackErrorMessage(): string {
  return 'Suara tidak bisa diputar. Periksa koneksi lalu coba lagi.'
}
