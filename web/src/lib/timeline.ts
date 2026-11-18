import type { MessageMeta, ProgressStep, ToolActivity } from './types'
import { humanToolName } from './format'

export interface TimelineRow {
  key: string
  kind: 'thinking' | 'tool' | 'plan' | 'status'
  name: string
  label: string
  detail: string
  durationMs: number
  status: string
  failed: boolean
}

/**
 * The timeline is exactly what the server recorded. It keeps the stored order
 * (oldest first, newest last) and never synthesises a row for an empty turn.
 * New rows carry steps; an older message without steps still reads its tools.
 */
export function timelineRows(meta?: MessageMeta): TimelineRow[] {
  const steps = meta?.steps ?? []
  if (steps.length > 0) {
    return steps
      .slice()
      .sort((a, b) => a.seq - b.seq)
      .map((step) => stepRow(step))
  }
  const tools = meta?.tools ?? []
  return tools
    .filter((tool: ToolActivity) => tool.name.trim() !== '')
    .map((tool, index) => ({
      key: `tool-${index}`,
      kind: 'tool' as const,
      name: tool.name,
      label: humanToolName(tool.name),
      detail: '',
      durationMs: Math.max(0, tool.durationMs || 0),
      status: tool.status,
      failed: tool.status === 'failed' || tool.status === 'error',
    }))
}

export function stepRow(step: ProgressStep): TimelineRow {
  return {
    key: `step-${step.seq}`,
    kind: step.kind,
    name: step.name ?? '',
    label: stepLabel(step),
    detail: step.detail ?? '',
    durationMs: Math.max(0, step.durationMs || 0),
    status: step.status,
    failed: step.status === 'failed',
  }
}

export function stepLabel(step: ProgressStep): string {
  if (step.kind === 'thinking') return 'berpikir'
  if (step.kind === 'plan') return step.name?.trim() || 'rencana'
  if (step.kind === 'status') return step.name?.trim() || 'menunggu'
  return humanToolName(step.name ?? '')
}

/** Panel heading: work, waiting, or a reconnecting socket. */
export function progressHeading(idleMs: number, elapsed: string, reconnecting: boolean): string {
  if (reconnecting) return `menyambung ulang… ${elapsed}`
  if (idleMs >= 20_000) return `menunggu Hermes… ${Math.floor(idleMs / 1000)}s`
  return `Hermes bekerja · ${elapsed}`
}

/** After two silent minutes the owner is offered the stop button. */
export function isStalled(idleMs: number): boolean {
  return idleMs >= 120_000
}

/** The panel's compact unit: 12.4s, not a sentence. */
export function compactDuration(ms: number): string {
  if (!ms || ms <= 0) return ''
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  const minutes = Math.floor(ms / 60_000)
  const seconds = Math.round((ms % 60_000) / 1000)
  return `${minutes}m ${String(seconds).padStart(2, '0')}s`
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

export type RunState = 'idle' | 'working' | 'waiting' | 'needs-you' | 'needs-approval'

/** Header chip: a stalled chain must never look like idle. */
export function roomState(
  working: boolean,
  waiting: boolean,
  lastStatusLine: string | undefined,
  pendingApprovals = 0,
): RunState {
  // A parked command needs Boss more than a timer needs a label.
  if (pendingApprovals > 0) return 'needs-approval'
  if (working) return 'working'
  if (waiting) return 'waiting'
  if (statusKind(lastStatusLine) === 'blocked') return 'needs-you'
  return 'idle'
}

/** A stop is confirmed only after a minute of real work. */
export function needsStopConfirm(startedAt: number, now: number): boolean {
  if (!startedAt || startedAt <= 0) return false
  return now - startedAt > 60_000
}

/** Seconds left before Iris stops accepting an answer. */
export function approvalSeconds(expiresAt: string, now: number): number {
  const expires = Date.parse(expiresAt)
  if (Number.isNaN(expires)) return 0
  return Math.ceil((expires - now) / 1000)
}

export function approvalCountdown(expiresAt: string, now: number): string {
  const seconds = approvalSeconds(expiresAt, now)
  if (seconds <= 0) return 'kedaluwarsa'
  if (seconds < 60) return `${seconds} detik lagi`
  const minutes = Math.floor(seconds / 60)
  const rest = seconds % 60
  return `${minutes} mnt ${String(rest).padStart(2, '0')} dtk lagi`
}

export function statusKind(line?: string): 'running' | 'done' | 'blocked' | null {
  if (!line) return null
  const match = line.match(/STATUS:\s*(RUNNING|DONE|BLOCKED)\b/i)
  if (!match) return null
  return match[1].toLowerCase() as 'running' | 'done' | 'blocked'
}
