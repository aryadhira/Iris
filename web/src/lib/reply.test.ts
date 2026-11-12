import { describe, expect, it } from 'vitest'
import { cleanReply, ReplyStream } from './reply'

describe('reply cleaner', () => {
  it('matches the Go/Python table', () => {
    const cases: Array<[string, string]> = [
      ['Sudah selesai.\n\nSTATUS: DONE — laporan ada di outbox.\n', 'Sudah selesai.'],
      ['**STATUS: BLOCKED** — butuh izin\nTeks lain.', 'Teks lain.'],
      ['Langkah pertama -- lalu langkah kedua.', 'Langkah pertama — lalu langkah kedua.'],
      ['-- catatan penting\nSelesai.', '— catatan penting\nSelesai.'],
      ['Jalankan `iris --auto --force` sekarang.', 'Jalankan `iris --auto --force` sekarang.'],
      ['Jalankan --auto dan --force.', 'Jalankan --auto dan --force.'],
      [
        'Contoh:\n```sql\n-- catatan\nSELECT 1;\n```\nSelesai.',
        'Contoh:\n```sql\n-- catatan\nSELECT 1;\n```\nSelesai.',
      ],
      ['Satu.\n\n---\n\nDua.', 'Satu.\n\nDua.'],
      ['Selesai.\n[[artifact: outbox/a.md]]\n', 'Selesai.'],
    ]
    for (const [raw, want] of cases) {
      expect(cleanReply(raw)).toBe(want)
      expect(cleanReply(cleanReply(raw))).toBe(want)
    }
  })

  it('holds a trailing -- until the next chunk decides', () => {
    const stream = new ReplyStream()
    expect(stream.feed('Halo --')).toBe('Halo ')
    expect(stream.feed(' auto')).toBe('— auto')
    expect(stream.visible()).toBe('Halo — auto')
  })

  it('never paints a STATUS line, even split across chunks', () => {
    const stream = new ReplyStream()
    expect(stream.feed('Selesai.\n\nSTATUS: DON')).toBe('Selesai.\n\n')
    expect(stream.feed('E — selesai\n')).toBe('')
    expect(stream.flush()).toBe('')
    expect(stream.visible()).not.toContain('STATUS')
  })
})
