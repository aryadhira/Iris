import type { Continuation, FileAttachment, Room, RoomFile, StoredMessage } from './types'

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

  async listRoomFiles(roomId: string): Promise<RoomFile[]> {
    const body = await request<{ files: RoomFile[] }>(
      `/api/rooms/${encodeURIComponent(roomId)}/files`,
    )
    return body.files ?? []
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

  deleteFile(id: string): Promise<{ deleted: boolean }> {
    return request(`/api/files/${encodeURIComponent(id)}`, { method: 'DELETE' })
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
