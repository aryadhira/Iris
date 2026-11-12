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

/** A row exactly as irisd returns it. */
export interface StoredMessage {
  id: string
  room_id: string
  role: string
  body: string
  run_id?: string | null
  attachments?: FileAttachment[]
  created_at: string
}

/** A row as the room renders it. */
export interface ChatMessage {
  id: string
  role: 'user' | 'assistant' | 'system'
  body: string
  createdAt: string
  attachments?: FileAttachment[]
  /** True for a message that exists only in this browser tab. */
  local?: boolean
  /** True when the connection died before the reply was confirmed complete. */
  incomplete?: boolean
}

export type TurnPhase = 'idle' | 'streaming' | 'reconnecting' | 'error'
