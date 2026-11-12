import { describe, expect, it } from 'vitest'
import {
  attachmentBlockReason,
  createAttachmentStore,
  readyAttachments,
  type PendingAttachment,
} from './attachments'
import type { FileAttachment } from '../lib/types'

function file(name = 'foto.png', type = 'image/png'): File {
  return new File([new Uint8Array([1, 2, 3])], name, { type })
}

function uploaded(overrides: Partial<FileAttachment> = {}): FileAttachment {
  return {
    id: 'file-1',
    name: 'foto.png',
    mime: 'image/png',
    size: 3,
    sha256: 'abc',
    ...overrides,
  }
}

describe('attachment chip state machine', () => {
  it('goes uploading → ready, records progress, and offers the id to send', async () => {
    let finish: (value: FileAttachment) => void = () => {}
    let seen = 0
    const store = createAttachmentStore({
      nextKey: () => 'k1',
      upload: (_file, onProgress) => {
        onProgress(42)
        return new Promise<FileAttachment>((resolve) => {
          finish = resolve
        })
      },
    })

    const pending = store.add(file())
    expect(store.state.items).toHaveLength(1)
    expect(store.state.items[0].status).toBe('uploading')
    expect(store.state.items[0].progress).toBe(42)
    expect(store.uploading.value).toBe(true)
    expect(store.blockReason.value).toBe('Menunggu unggahan selesai…')
    expect(store.ready.value).toHaveLength(0)
    seen = store.state.items[0].progress
    expect(seen).toBe(42)

    finish(uploaded())
    await pending

    expect(store.state.items[0].status).toBe('ready')
    expect(store.state.items[0].progress).toBe(100)
    expect(store.uploading.value).toBe(false)
    expect(store.blockReason.value).toBe('')
    expect(store.ready.value.map((item) => item.id)).toEqual(['file-1'])
  })

  it('marks a failed upload and blocks send until it is removed', async () => {
    const store = createAttachmentStore({
      nextKey: () => 'k2',
      upload: async () => {
        throw new Error('tipe file ini belum didukung')
      },
    })
    await store.add(file('evil.exe', 'application/octet-stream'))

    expect(store.state.items[0].status).toBe('error')
    expect(store.state.items[0].error).toContain('belum didukung')
    expect(store.blockReason.value).toContain('gagal diunggah')
    expect(store.ready.value).toHaveLength(0)

    store.remove('k2')
    expect(store.state.items).toHaveLength(0)
    expect(store.blockReason.value).toBe('')
  })

  it('discards an upload that is removed while in flight or after it is ready', async () => {
    const removed: string[] = []
    let finish: (value: FileAttachment) => void = () => {}
    const store = createAttachmentStore({
      nextKey: () => 'k3',
      removeRemote: async (id) => {
        removed.push(id)
      },
      upload: () =>
        new Promise<FileAttachment>((resolve) => {
          finish = resolve
        }),
    })

    const pending = store.add(file())
    store.remove('k3')
    finish(uploaded({ id: 'orphan' }))
    await pending
    expect(removed).toEqual(['orphan'])

    const store2 = createAttachmentStore({
      nextKey: () => 'k4',
      removeRemote: async (id) => {
        removed.push(id)
      },
      upload: async () => uploaded({ id: 'ready-1' }),
    })
    await store2.add(file())
    store2.remove('k4')
    expect(removed).toEqual(['orphan', 'ready-1'])
  })

  it('clears only accepted files, keeping a failed chip visible', async () => {
    const store = createAttachmentStore({
      nextKey: (() => {
        let n = 0
        return () => `k${(n += 1)}`
      })(),
      upload: async (value) => {
        if (value.name === 'bad.pdf') throw new Error('gagal')
        return uploaded({ id: `id-${value.name}`, name: value.name })
      },
    })
    await store.add(file('good.png'))
    await store.add(file('bad.pdf'))
    expect(store.blockReason.value).toContain('gagal')

    store.clearReady()
    expect(store.state.items.map((item) => item.name)).toEqual(['bad.pdf'])
    expect(attachmentBlockReason(store.state.items)).toContain('gagal')
  })
})

describe('attachment helpers', () => {
  it('only sends ready items with an id', () => {
    const items: PendingAttachment[] = [
      { key: 'a', name: 'a.png', size: 1, progress: 100, status: 'ready', id: '1' },
      { key: 'b', name: 'b.png', size: 1, progress: 10, status: 'uploading' },
      { key: 'c', name: 'c.png', size: 1, progress: 0, status: 'error' },
    ]
    expect(readyAttachments(items).map((item) => item.id)).toEqual(['1'])
    expect(attachmentBlockReason(items)).toContain('Menunggu')
  })
})
