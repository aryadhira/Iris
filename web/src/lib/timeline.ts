import type { MessageMeta, ToolActivity } from './types'
import { humanToolName } from './format'

export interface TimelineRow {
  name: string
  label: string
  durationMs: number
  status: string
  failed: boolean
}

/**
 * The timeline is exactly what the server recorded. It keeps the stored order
 * (oldest first, newest last) and never synthesises a row for an empty turn.
 */
export function timelineRows(meta?: MessageMeta): TimelineRow[] {
  const tools = meta?.tools ?? []
  return tools
    .filter((tool: ToolActivity) => tool.name.trim() !== '')
    .map((tool) => ({
      name: tool.name,
      label: humanToolName(tool.name),
      durationMs: Math.max(0, tool.durationMs || 0),
      status: tool.status,
      failed: tool.status === 'failed' || tool.status === 'error',
    }))
}

export function durationLabel(ms: number): string {
  if (!ms || ms <= 0) return '—'
  if (ms < 1000) return `${Math.round(ms)} ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} dtk`
  const minutes = Math.floor(ms / 60_000)
  const seconds = Math.round((ms % 60_000) / 1000)
  return `${minutes} mnt ${String(seconds).padStart(2, '0')} dtk`
}

/** Seconds until the next automatic follow-up. Negative means it is running now. */
export function countdownSeconds(dueAt: string, now: number): number {
  const due = Date.parse(dueAt)
  if (Number.isNaN(due)) return 0
  return Math.ceil((due - now) / 1000)
}

export function countdownLabel(dueAt: string, now: number): string {
  const seconds = countdownSeconds(dueAt, now)
  if (seconds <= 0) return 'menyiapkan lanjutan…'
  return `lanjut otomatis dalam ${seconds} detik`
}

export type RunState = 'idle' | 'working' | 'waiting' | 'needs-you'

/** Header chip: a stalled chain must never look like idle. */
export function roomState(
  working: boolean,
  waiting: boolean,
  lastStatusLine: string | undefined,
): RunState {
  if (working) return 'working'
  if (waiting) return 'waiting'
  if (statusKind(lastStatusLine) === 'blocked') return 'needs-you'
  return 'idle'
}

export function statusKind(line?: string): 'running' | 'done' | 'blocked' | null {
  if (!line) return null
  const match = line.match(/STATUS:\s*(RUNNING|DONE|BLOCKED)\b/i)
  if (!match) return null
  return match[1].toLowerCase() as 'running' | 'done' | 'blocked'
}
