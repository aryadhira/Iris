import { describe, expect, it } from 'vitest'
import {
  countdownLabel,
  countdownSeconds,
  durationLabel,
  roomState,
  statusKind,
  timelineRows,
} from './timeline'

describe('run timeline', () => {
  it('renders tools in the stored order, newest last', () => {
    const rows = timelineRows({
      tools: [
        { name: 'terminal', durationMs: 750, status: 'completed' },
        { name: 'process_manage', durationMs: 90_000, status: 'failed' },
        { name: 'write_file', durationMs: 12, status: 'completed' },
      ],
    })
    expect(rows.map((row) => row.name)).toEqual(['terminal', 'process_manage', 'write_file'])
    expect(rows[0].label).toBe('Menjalankan perintah')
    expect(rows[1].failed).toBe(true)
    expect(rows[2].durationMs).toBe(12)
  })

  it('renders nothing for a turn that used no tools', () => {
    expect(timelineRows(undefined)).toEqual([])
    expect(timelineRows({})).toEqual([])
    expect(timelineRows({ tools: [] })).toEqual([])
  })

  it('formats a duration honestly', () => {
    expect(durationLabel(0)).toBe('—')
    expect(durationLabel(750)).toBe('750 ms')
    expect(durationLabel(1250)).toBe('1.3 dtk')
    expect(durationLabel(90_000)).toBe('1 mnt 30 dtk')
  })

  it('counts down to the follow-up', () => {
    const now = Date.parse('2026-11-12T10:00:00Z')
    const due = '2026-11-12T10:00:12Z'
    expect(countdownSeconds(due, now)).toBe(12)
    expect(countdownLabel(due, now)).toBe('lanjut otomatis dalam 12 detik')
    expect(countdownSeconds('2026-11-12T09:59:59Z', now)).toBe(-1)
    expect(countdownLabel('2026-11-12T09:59:59Z', now)).toBe('menyiapkan lanjutan…')
  })

  it('never lets a running chain look idle', () => {
    expect(roomState(true, true, undefined)).toBe('working')
    expect(roomState(false, true, 'STATUS: RUNNING — 90 detik')).toBe('waiting')
    expect(roomState(false, false, 'STATUS: BLOCKED — butuh izin')).toBe('needs-you')
    expect(roomState(false, false, 'STATUS: DONE — selesai')).toBe('idle')
    expect(statusKind(undefined)).toBeNull()
  })
})
