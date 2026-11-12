import { computed, reactive } from 'vue'
import type { FileAttachment } from '../lib/types'

export type UploadStatus = 'uploading' | 'ready' | 'error'

export interface PendingAttachment {
  key: string
  name: string
  size: number
  progress: number
  status: UploadStatus
  id?: string
  mime?: string
  sha256?: string
  error?: string
}

export interface AttachmentStoreDeps {
  upload: (file: File, onProgress: (percent: number) => void) => Promise<FileAttachment>
  /** Remove a file already uploaded but not yet sent. */
  removeRemote?: (id: string) => Promise<unknown>
  nextKey?: () => string
}

let sequence = 0
function fallbackKey(): string {
  sequence += 1
  return `att-${Date.now()}-${sequence}`
}

/** Send is blocked while anything is still uploading or has failed. */
export function attachmentBlockReason(items: PendingAttachment[]): string {
  if (items.some((item) => item.status === 'uploading')) {
    return 'Menunggu unggahan selesai…'
  }
  if (items.some((item) => item.status === 'error')) {
    return 'Ada lampiran yang gagal diunggah — hapus dulu.'
  }
  return ''
}

export function readyAttachments(items: PendingAttachment[]): FileAttachment[] {
  const out: FileAttachment[] = []
  for (const item of items) {
    if (item.status !== 'ready' || !item.id) continue
    out.push({
      id: item.id,
      name: item.name,
      mime: item.mime ?? 'application/octet-stream',
      size: item.size,
      sha256: item.sha256 ?? '',
    })
  }
  return out
}

export function createAttachmentStore(deps: AttachmentStoreDeps) {
  const nextKey = deps.nextKey ?? fallbackKey
  const state = reactive({ items: [] as PendingAttachment[] })

  const uploading = computed(() => state.items.some((item) => item.status === 'uploading'))
  const blockReason = computed(() => attachmentBlockReason(state.items))
  const ready = computed(() => readyAttachments(state.items))

  async function add(file: File): Promise<void> {
    const item: PendingAttachment = reactive({
      key: nextKey(),
      name: file.name,
      size: file.size,
      progress: 0,
      status: 'uploading' as UploadStatus,
    })
    state.items.push(item)
    try {
      const uploaded = await deps.upload(file, (percent) => {
        item.progress = Math.max(0, Math.min(100, percent))
      })
      // The chip may have been removed while the bytes were in flight. Delete
      // the now-orphaned row instead of leaving an invisible file in the room.
      if (!state.items.includes(item)) {
        await deps.removeRemote?.(uploaded.id)
        return
      }
      item.id = uploaded.id
      item.mime = uploaded.mime
      item.sha256 = uploaded.sha256
      item.size = uploaded.size
      item.progress = 100
      item.status = 'ready'
    } catch (error) {
      item.status = 'error'
      item.progress = 0
      item.error = error instanceof Error ? error.message : 'Unggahan gagal. Coba lagi.'
    }
  }

  function remove(key: string): void {
    const index = state.items.findIndex((item) => item.key === key)
    if (index === -1) return
    const [item] = state.items.splice(index, 1)
    if (item.status === 'ready' && item.id) {
      void deps.removeRemote?.(item.id)
    }
  }

  function clearReady(): void {
    state.items = state.items.filter((item) => item.status !== 'ready')
  }

  function clear(): void {
    state.items = []
  }

  return { state, add, remove, clearReady, clear, uploading, blockReason, ready }
}

export type AttachmentStore = ReturnType<typeof createAttachmentStore>
