export interface RoomRun {
  id: string
  status:
    | 'queued'
    | 'running'
    | 'waiting_for_approval'
    | 'stopping'
    | 'completed'
    | 'failed'
    | 'cancelled'
    | 'interrupted'
  started_at: string
  continuation: number
  chain?: string
}

export interface Room {
  id: string
  name: string
  topic: string
  pending_approvals?: number
  run?: RoomRun | null
  created_at: string
  archived_at?: string | null
}

/** One dangerous-command approval, exactly as irisd stored it. */
export interface Approval {
  id: string
  room_id: string
  tool_name?: string
  command: string
  description?: string
  choices: string[]
  state: 'pending' | 'allowed' | 'denied' | 'expired' | 'superseded'
  choice?: string | null
  created_at: string
  expires_at: string
  resolved_at?: string | null
}

/** A file that belongs to a room, as irisd returns it. */
export interface FileAttachment {
  id: string
  name: string
  mime: string
  size: number
  sha256: string
}

export interface RoomFile extends FileAttachment {
  room_id: string
  message_id?: string | null
  source: 'user' | 'agent'
  created_at: string
}

/** A tool call as irisd recorded it for one assistant message. */
export interface ToolActivity {
  name: string
  durationMs: number
  status: string
}

/** What a turn left behind, read from the stream, never invented. */
export interface MessageMeta {
  procs?: string[]
  tools?: ToolActivity[]
  statusLine?: string
  /** True when a retry replaced this reply. It stays visible, dimmed. */
  superseded?: boolean
}

/** One reminder owned by a room. */
export interface Reminder {
  id: string
  room_id: string
  text: string
  due_at: string
  state: 'pending' | 'fired' | 'cancelled'
  created_at: string
  fired_at?: string | null
}

/** A pending safety-net follow-up. */
export interface Continuation {
  id: string
  attempt: number
  status: string
  due_at: string
  deadline: string
}

export interface SearchResult {
  roomId: string
  roomName: string
  messageId: string
  role: string
  createdAt: string
  snippet: string
}

/** A row exactly as irisd returns it. */
export interface StoredMessage {
  id: string
  room_id: string
  role: string
  body: string
  state?: 'streaming' | 'done' | 'interrupted'
  run_id?: string | null
  meta?: MessageMeta
  attachments?: FileAttachment[]
  created_at: string
}

/** A row as the room renders it. */
export interface ChatMessage {
  id: string
  role: 'user' | 'assistant' | 'system'
  body: string
  createdAt: string
  /** Hermes run id; shown only inside the timeline inspector. */
  runId?: string
  meta?: MessageMeta
  attachments?: FileAttachment[]
  /** True for a message that exists only in this browser tab. */
  local?: boolean
  /** True when the connection died before the reply was confirmed complete. */
  incomplete?: boolean
  /** Server truth for a row: a partial answer, a final one, or a cut-off one. */
  state?: 'streaming' | 'done' | 'interrupted'
}

export type TurnPhase = 'idle' | 'streaming' | 'reconnecting' | 'error'

/** Cost meter shapes, exactly as irisd returns them. Cost is never invented. */
export interface UsageTotals {
  turns: number
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  cost_usd: number
  unknown_cost_turns: number
  unreported_turns: number
  unreported_cache_turns: number
}

export interface UsageDay {
  day: string
  turns: number
  tokens: number
  cost_usd: number
  unknown_cost_turns: number
}

export interface UsageRoom {
  room_id: string
  room_name: string
  turns: number
  tokens: number
  cost_usd: number
  unknown_cost_turns: number
}

export interface UsageBudget {
  enabled: boolean
  usd?: number
  spent_usd?: number
  unknown_cost_turns?: number
}

export interface UsageResponse {
  from: string
  to: string
  totals: UsageTotals
  today: UsageTotals
  days: UsageDay[]
  rooms: UsageRoom[]
  budget: UsageBudget
  prices: { file: string; entries: number; error?: string }
}
