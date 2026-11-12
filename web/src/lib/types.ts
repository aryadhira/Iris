export interface Room {
  id: string
  name: string
  topic: string
  pending_approvals?: number
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
}

export type TurnPhase = 'idle' | 'streaming' | 'reconnecting' | 'error'
