export interface Room {
  id: string
  name: string
  topic: string
  created_at: string
  archived_at?: string | null
}

/** A row exactly as irisd returns it. */
export interface StoredMessage {
  id: string
  room_id: string
  role: string
  body: string
  run_id?: string | null
  created_at: string
}

/** A row as the room renders it. */
export interface ChatMessage {
  id: string
  role: 'user' | 'assistant' | 'system'
  body: string
  createdAt: string
  /** True for a message that exists only in this browser tab. */
  local?: boolean
  /** True when the connection died before the reply was confirmed complete. */
  incomplete?: boolean
}

export type TurnPhase = 'idle' | 'streaming' | 'reconnecting' | 'error'
