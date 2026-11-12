import { describe, expect, it } from 'vitest'
import { elapsedLabel, formatUSD, humanToolName } from './format'

describe('humanToolName', () => {
  it('strips the internal underscore before naming the tool', () => {
    expect(humanToolName('_thinking')).toBe('Berpikir')
  })

  it('maps known tools to a human line', () => {
    expect(humanToolName('terminal')).toBe('Menjalankan perintah')
    expect(humanToolName('_read_file')).toBe('Membaca berkas')
  })

  it('humanises an unknown tool instead of showing an id', () => {
    expect(humanToolName('_proc_manage')).toBe('Proc manage')
    expect(humanToolName('')).toBe('Bekerja')
  })
})

describe('elapsedLabel', () => {
  const start = Date.parse('2026-11-12T10:00:00Z')

  it('counts seconds, then minutes', () => {
    expect(elapsedLabel(start, start + 7_000)).toBe('7 detik')
    expect(elapsedLabel(start, start + 65_000)).toBe('1 mnt 05 dtk')
    expect(elapsedLabel(start, start + 3_600_000)).toBe('1 jam 00 mnt')
  })

  it('never goes negative when clocks disagree', () => {
    expect(elapsedLabel(start, start - 5_000)).toBe('0 detik')
  })
})

describe('formatUSD', () => {
  it('keeps a sub-cent turn visible instead of rendering $0.00', () => {
    expect(formatUSD(0.0025)).toBe('$0.0025')
    expect(formatUSD(0)).toBe('$0.00')
    expect(formatUSD(0.42)).toBe('$0.42')
  })
})
