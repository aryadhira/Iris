import { describe, expect, it } from 'vitest'
import {
  approvalCountdown,
  approvalSeconds,
  countdownLabel,
  countdownSeconds,
  durationLabel,
  needsStopConfirm,
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

  it('puts a parked approval above work, then returns to work once it expires', () => {
    expect(roomState(true, false, undefined, 2)).toBe('needs-approval')
    expect(roomState(true, false, undefined, 0)).toBe('working')
    expect(roomState(false, true, undefined, 1)).toBe('needs-approval')
  })

  it('asks for a stop confirmation only after a minute', () => {
    const start = Date.parse('2026-11-12T10:00:00Z')
    expect(needsStopConfirm(start, start + 59_000)).toBe(false)
    expect(needsStopConfirm(start, start + 61_000)).toBe(true)
    expect(needsStopConfirm(0, start)).toBe(false)
  })

  it('counts down to the approval deadline, then says expired', () => {
    const now = Date.parse('2026-11-12T10:00:00Z')
    expect(approvalSeconds('2026-11-12T10:00:12Z', now)).toBe(12)
    expect(approvalCountdown('2026-11-12T10:00:12Z', now)).toBe('12 detik lagi')
    expect(approvalCountdown('2026-11-12T10:01:05Z', now)).toBe('1 mnt 05 dtk lagi')
    expect(approvalCountdown('2026-11-12T09:59:59Z', now)).toBe('kedaluwarsa')
  })
})
