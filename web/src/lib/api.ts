import type {
  Approval,
  Continuation,
  FileAttachment,
  Reminder,
  Room,
  RoomFile,
  SearchResult,
  StoredMessage,
  UsageResponse,
} from './types'
import { filenameFromDisposition } from './files'

export class ApiError extends Error {
  readonly status: number

  constructor(message: string, status: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

interface ErrorBody {
  error?: string
}

async function readError(response: Response): Promise<string> {
  try {
    const body = (await response.json()) as ErrorBody
    if (typeof body.error === 'string' && body.error.trim() !== '') return body.error
  } catch {
    // Not JSON; fall through to a plain line.
  }
  return `Iris menjawab dengan kode ${response.status}.`
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    credentials: 'same-origin',
    headers: init.body ? { 'Content-Type': 'application/json' } : undefined,
    ...init,
  })
  if (!response.ok) throw new ApiError(await readError(response), response.status)
  return (await response.json()) as T
}

export interface MessagePage {
  messages: StoredMessage[]
}

export interface VoiceTranscription {
  text: string
  reason?: string
  ms?: number
}

export const api = {
  me(): Promise<{ authenticated: boolean }> {
    return request('/api/auth/me')
  },

  login(passphrase: string): Promise<{ authenticated: boolean }> {
    return request('/api/auth/login', {
      method: 'POST',
      body: JSON.stringify({ passphrase }),
    })
  },

  logout(): Promise<{ authenticated: boolean }> {
    return request('/api/auth/logout', { method: 'POST' })
  },

  async listRooms(): Promise<Room[]> {
    const body = await request<{ rooms: Room[] }>('/api/rooms')
    return body.rooms ?? []
  },

  async createRoom(name: string, topic: string): Promise<Room> {
    const body = await request<{ room: Room }>('/api/rooms', {
      method: 'POST',
      body: JSON.stringify({ name, topic }),
    })
    return body.room
  },

  async getRoom(id: string): Promise<Room> {
    const body = await request<{ room: Room }>(`/api/rooms/${encodeURIComponent(id)}`)
    return body.room
  },

  async archiveRoom(id: string): Promise<Room> {
    const body = await request<{ room: Room }>(`/api/rooms/${encodeURIComponent(id)}/archive`, {
      method: 'POST',
    })
    return body.room
  },

  async listMessages(
    roomId: string,
    options: { before?: string; limit?: number } = {},
  ): Promise<StoredMessage[]> {
    const params = new URLSearchParams()
    if (options.before) params.set('before', options.before)
    params.set('limit', String(options.limit ?? 200))
    const body = await request<MessagePage>(
      `/api/rooms/${encodeURIComponent(roomId)}/messages?${params.toString()}`,
    )
    return body.messages ?? []
  },

  /**
   * Open the streaming turn. The caller owns the response body; a non-2xx here
   * means the user message was not accepted and nothing was stored.
   */
  sendMessage(
    roomId: string,
    text: string,
    attachmentIds: string[] = [],
    signal?: AbortSignal,
  ): Promise<Response> {
    return fetch(`/api/rooms/${encodeURIComponent(roomId)}/messages`, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
      body: JSON.stringify({ text, attachmentIds }),
      signal,
    })
  },

  /**
   * Re-run the turn behind an existing user message. The server reuses that
   * row and streams a fresh reply; it never creates a second user message.
   */
  retryMessage(roomId: string, messageId: string, signal?: AbortSignal): Promise<Response> {
    return fetch(
      `/api/rooms/${encodeURIComponent(roomId)}/messages/${encodeURIComponent(messageId)}/retry`,
      {
        method: 'POST',
        credentials: 'same-origin',
        headers: { Accept: 'text/event-stream' },
        signal,
      },
    )
  },

  getUsage(roomId?: string): Promise<UsageResponse> {
    const params = new URLSearchParams()
    if (roomId) params.set('room', roomId)
    const query = params.toString()
    return request(`/api/usage${query ? `?${query}` : ''}`)
  },

  async listRoomFiles(roomId: string): Promise<RoomFile[]> {
    const body = await request<{ files: RoomFile[] }>(
      `/api/rooms/${encodeURIComponent(roomId)}/files`,
    )
    return body.files ?? []
  },

  async search(query: string, limit = 20): Promise<SearchResult[]> {
    const params = new URLSearchParams({ q: query, limit: String(limit) })
    const body = await request<{ results: SearchResult[] }>(`/api/search?${params.toString()}`)
    return body.results ?? []
  },

  /** The transcript as a markdown file. The caller owns the download. */
  async exportRoom(roomId: string): Promise<{ blob: Blob; filename: string }> {
    const response = await fetch(`/api/rooms/${encodeURIComponent(roomId)}/export.md`, {
      credentials: 'same-origin',
    })
    if (!response.ok) throw new ApiError(await readError(response), response.status)
    return {
      blob: await response.blob(),
      filename: filenameFromDisposition(response.headers.get('content-disposition')),
    }
  },

  async getContinuation(roomId: string): Promise<Continuation | null> {
    const body = await request<{ continuation: Continuation | null }>(
      `/api/rooms/${encodeURIComponent(roomId)}/continuation`,
    )
    return body.continuation ?? null
  },

  cancelContinuation(roomId: string): Promise<{ cancelled: number }> {
    return request(`/api/rooms/${encodeURIComponent(roomId)}/continuation/cancel`, {
      method: 'POST',
    })
  },

  async listApprovals(roomId: string): Promise<Approval[]> {
    const body = await request<ApprovalPage>(
      `/api/rooms/${encodeURIComponent(roomId)}/approvals`,
    )
    return body.approvals ?? []
  },

  async resolveApproval(
    roomId: string,
    approvalId: string,
    choice: string,
  ): Promise<Approval> {
    const body = await request<{ approval: Approval }>(
      `/api/rooms/${encodeURIComponent(roomId)}/approvals/${encodeURIComponent(approvalId)}`,
      { method: 'POST', body: JSON.stringify({ choice }) },
    )
    return body.approval
  },

  stopRoom(roomId: string): Promise<StopResult> {
    return request(`/api/rooms/${encodeURIComponent(roomId)}/stop`, { method: 'POST' })
  },

  async listReminders(roomId: string): Promise<Reminder[]> {
    const body = await request<{ reminders: Reminder[] }>(
      `/api/rooms/${encodeURIComponent(roomId)}/reminders`,
    )
    return body.reminders ?? []
  },

  async createReminder(roomId: string, text: string, dueAt: string): Promise<Reminder> {
    const body = await request<{ reminder: Reminder }>(
      `/api/rooms/${encodeURIComponent(roomId)}/reminders`,
      { method: 'POST', body: JSON.stringify({ text, due_at: dueAt }) },
    )
    return body.reminder
  },

  deleteReminder(roomId: string, reminderId: string): Promise<{ reminder: Reminder }> {
    return request(
      `/api/rooms/${encodeURIComponent(roomId)}/reminders/${encodeURIComponent(reminderId)}`,
      { method: 'DELETE' },
    )
  },

  async getPushKey(): Promise<string> {
    const body = await request<{ public_key: string }>('/api/push/key')
    return body.public_key ?? ''
  },

  subscribePush(
    roomId: string,
    subscription: PushSubscriptionJSON,
  ): Promise<{ subscription: { id: string; room_id: string } }> {
    return request('/api/push/subscribe', {
      method: 'POST',
      body: JSON.stringify({
        room_id: roomId,
        endpoint: subscription.endpoint,
        keys: subscription.keys,
      }),
    })
  },

  unsubscribePush(endpoint: string): Promise<{ deleted: number }> {
    return request('/api/push/subscribe', {
      method: 'DELETE',
      body: JSON.stringify({ endpoint }),
    })
  },

  deleteFile(id: string): Promise<{ deleted: boolean }> {
    return request(`/api/files/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },

  /**
   * Push-to-talk upload. The server sniffs the container; the blob type is a
   * hint only. An empty recording comes back as {text: "", reason: "no_speech"}.
   */
  async transcribe(blob: Blob): Promise<VoiceTranscription> {
    const form = new FormData()
    form.append('audio', blob, 'rekaman.webm')
    const response = await fetch('/api/voice/transcribe', {
      method: 'POST',
      credentials: 'same-origin',
      body: form,
    })
    if (!response.ok) throw new ApiError(await readError(response), response.status)
    return (await response.json()) as VoiceTranscription
  },

  /** Summary speech. The server returns audio/mpeg, never raw markdown. */
  async speak(text: string): Promise<Blob> {
    const response = await fetch('/api/voice/speak', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text }),
    })
    if (!response.ok) throw new ApiError(await readError(response), response.status)
    return response.blob()
  },

  /** The streaming speech response; the caller parses NDJSON line by line. */
  speakStream(text: string, signal?: AbortSignal): Promise<Response> {
    return fetch('/api/voice/speak/stream', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', Accept: 'application/x-ndjson' },
      body: JSON.stringify({ text }),
      signal,
    })
  },

  /**
   * Upload one file. XMLHttpRequest is used only for its upload progress
   * events; the endpoint streams and sniffs the bytes server-side.
   */
  uploadFile(
    roomId: string,
    file: File,
    onProgress: (percent: number) => void = () => {},
  ): Promise<FileAttachment> {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest()
      xhr.open('POST', `/api/rooms/${encodeURIComponent(roomId)}/uploads`)
      xhr.withCredentials = true
      xhr.responseType = 'text'
      xhr.upload.onprogress = (event) => {
        if (event.lengthComputable && event.total > 0) {
          onProgress(Math.min(100, Math.round((event.loaded / event.total) * 100)))
        }
      }
      xhr.onload = () => {
        let body: { file?: FileAttachment; error?: string } = {}
        try {
          body = JSON.parse(xhr.responseText) as typeof body
        } catch {
          // not JSON; fall through to the status line
        }
        if (xhr.status >= 200 && xhr.status < 300 && body.file) {
          onProgress(100)
          resolve(body.file)
          return
        }
        reject(new ApiError(body.error ?? `Iris menjawab dengan kode ${xhr.status}.`, xhr.status))
      }
      xhr.onerror = () =>
        reject(new ApiError('Unggahan terputus. Periksa koneksi lalu coba lagi.', 0))
      const form = new FormData()
      form.append('file', file, file.name)
      xhr.send(form)
    })
  },
}

export interface ApprovalPage {
  approvals: Approval[]
}

export interface StopResult {
  stopped: number
  reason?: string
}
