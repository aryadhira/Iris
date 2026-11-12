export interface Room {
  id: string
  name: string
  topic: string
  created_at: string
  archived_at?: string | null
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

/** A pending safety-net follow-up. */
export interface Continuation {
  id: string
  attempt: number
  status: string
  due_at: string
  deadline: string
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
  meta?: MessageMeta
  attachments?: FileAttachment[]
  /** True for a message that exists only in this browser tab. */
  local?: boolean
  /** True when the connection died before the reply was confirmed complete. */
  incomplete?: boolean
}

export type TurnPhase = 'idle' | 'streaming' | 'reconnecting' | 'error'
