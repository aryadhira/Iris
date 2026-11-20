import { describe, expect, it } from 'vitest'
import {
  artifactCard,
  fileDownloadUrl,
  fileKind,
  fileTypeLabel,
  fileUrl,
  filenameFromDisposition,
  formatBytes,
  isAudio,
  isImage,
  isMedia,
  isVideo,
} from './files'

describe('file presentation', () => {
  it('formats Indonesian sizes', () => {
    expect(formatBytes(840)).toBe('840 B')
    expect(formatBytes(12 * 1024)).toBe('12 KB')
    expect(formatBytes(1200 * 1024)).toBe('1,2 MB')
    expect(formatBytes(26 * 1024 * 1024)).toBe('26,0 MB')
  })

  it('classifies types by MIME, not by a lying extension', () => {
    expect(fileKind('image/png', 'x.exe')).toBe('image')
    expect(fileKind('application/pdf')).toBe('pdf')
    expect(fileKind('application/vnd.openxmlformats-officedocument.wordprocessingml.document')).toBe('doc')
    expect(fileKind('application/vnd.openxmlformats-officedocument.spreadsheetml.sheet')).toBe('sheet')
    expect(fileKind('text/markdown')).toBe('text')
    expect(fileKind('', 'laporan.xlsx')).toBe('sheet')
    expect(fileKind('', 'unknown.bin')).toBe('other')
    expect(isImage({ mime: 'image/jpeg' })).toBe(true)
    expect(isImage({ mime: 'application/pdf' })).toBe(false)
  })

  it('classifies sound and video, and keeps sound out of the video rule', () => {
    expect(fileKind('audio/mpeg', 'lagu.bin')).toBe('audio')
    expect(fileKind('audio/mp4', 'lagu.m4a')).toBe('audio')
    expect(fileKind('video/mp4', 'klip.mp4')).toBe('video')
    expect(fileKind('', 'lagu.mp3')).toBe('audio')
    expect(fileKind('', 'suara.wav')).toBe('audio')
    expect(fileKind('', 'klip.mov')).toBe('video')
    expect(fileKind('', 'lagu.webm')).toBe('video')
    expect(fileKind('', 'lagu.weba')).toBe('audio')
    expect(fileKind('video/webm', 'lagu.webm')).toBe('video')
    expect(isAudio({ mime: 'audio/mpeg' })).toBe(true)
    expect(isAudio({ mime: 'video/mp4' })).toBe(false)
    expect(isVideo({ mime: 'video/mp4' })).toBe(true)
    expect(isVideo({ mime: 'audio/mp4' })).toBe(false)
    expect(isMedia({ mime: 'audio/ogg' })).toBe(true)
    expect(isMedia({ mime: 'video/quicktime' })).toBe(true)
    expect(isMedia({ mime: 'image/png' })).toBe(true)
    expect(isMedia({ mime: 'application/pdf' })).toBe(false)
  })

  it('labels sound with the extension the speaker can read', () => {
    expect(fileTypeLabel('audio/mpeg', 'lagu.mp3')).toBe('MP3')
    expect(fileTypeLabel('audio/mp4', 'lagu.m4a')).toBe('M4A')
    expect(fileTypeLabel('audio/wav', 'suara.wav')).toBe('WAV')
    expect(fileTypeLabel('audio/ogg', 'suara.ogg')).toBe('OGG')
    expect(fileTypeLabel('video/mp4', 'klip.mp4')).toBe('MP4')
    expect(fileTypeLabel('audio/mpeg', 'tanpa-nama')).toBe('Audio')
    expect(fileTypeLabel('application/pdf', 'x.pdf')).toBe('PDF')
  })

  it('builds the artifact card model the UI and tests share', () => {
    const card = artifactCard({
      id: 'f 1',
      name: 'catatan akhir.md',
      mime: 'text/markdown',
      size: 1200 * 1024,
      sha256: 'abc',
    })
    expect(card.kind).toBe('text')
    expect(card.title).toBe('catatan akhir.md')
    expect(card.meta).toBe('Teks · 1,2 MB')
    expect(card.downloadUrl).toBe('/api/files/f%201')
    expect(fileUrl('abc')).toBe('/api/files/abc')
    expect(fileDownloadUrl('abc')).toBe('/api/files/abc?download=1')
    expect(fileDownloadUrl('f 1')).toBe('/api/files/f%201?download=1')
    expect(fileTypeLabel('application/pdf', 'x.pdf')).toBe('PDF')
    const song = artifactCard({
      id: 'audio-1',
      name: 'lagu.mp3',
      mime: 'audio/mpeg',
      size: 2 * 1024 * 1024,
      sha256: 'abc',
    })
    expect(song.kind).toBe('audio')
    expect(song.meta).toBe('MP3 · 2,0 MB')
  })

  it('reads the export filename from Content-Disposition', () => {
    expect(filenameFromDisposition('attachment; filename="kerja-2026-11-12.md"')).toBe(
      'kerja-2026-11-12.md',
    )
    expect(filenameFromDisposition("attachment; filename*=UTF-8''rencana%20kerja.md")).toBe(
      'rencana kerja.md',
    )
    expect(filenameFromDisposition('attachment; filename=p5-kerja-2026-11-12.md')).toBe(
      'p5-kerja-2026-11-12.md',
    )
    expect(filenameFromDisposition(null)).toBe('transkrip-iris.md')
  })
})
