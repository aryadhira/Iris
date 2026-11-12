import { describe, expect, it } from 'vitest'
import { artifactCard, fileKind, fileTypeLabel, fileUrl, formatBytes, isImage } from './files'

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
    expect(fileTypeLabel('application/pdf', 'x.pdf')).toBe('PDF')
  })
})
