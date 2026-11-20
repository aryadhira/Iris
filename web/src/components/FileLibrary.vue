<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { PhDownloadSimple, PhTrash, PhX } from '@phosphor-icons/vue'
import FileIcon from './FileIcon.vue'
import FileLightbox from './FileLightbox.vue'
import { api } from '../lib/api'
import { fileDownloadUrl, fileKind, fileUrl, formatBytes, isAudio, isImage, isVideo } from '../lib/files'
import type { FileAttachment, RoomFile } from '../lib/types'

const props = defineProps<{ open: boolean; roomId: string }>()
const emit = defineEmits<{ close: []; changed: [] }>()

const files = ref<RoomFile[]>([])
const loading = ref(false)
const loaded = ref(false)
const error = ref('')
const confirmingId = ref<string | null>(null)
const deletingId = ref<string | null>(null)
const preview = ref<FileAttachment | null>(null)

function restoreScroll(): void {
  document.body.style.overflow = ''
}

watch(
  () => props.open,
  async (open) => {
    if (!open) {
      restoreScroll()
      confirmingId.value = null
      return
    }
    document.body.style.overflow = 'hidden'
    await load()
  },
)

watch(
  () => props.roomId,
  () => {
    loaded.value = false
    files.value = []
  },
)

onBeforeUnmount(restoreScroll)

async function load(): Promise<void> {
  if (!props.roomId) return
  loading.value = true
  error.value = ''
  try {
    files.value = await api.listRoomFiles(props.roomId)
    loaded.value = true
  } catch {
    error.value = 'Iris tidak bisa memuat file room ini. Coba lagi.'
  } finally {
    loading.value = false
  }
}

async function remove(file: RoomFile): Promise<void> {
  deletingId.value = file.id
  error.value = ''
  try {
    await api.deleteFile(file.id)
    files.value = files.value.filter((item) => item.id !== file.id)
    confirmingId.value = null
    emit('changed')
  } catch {
    error.value = 'File ini belum bisa dihapus. Coba lagi.'
  } finally {
    deletingId.value = null
  }
}
</script>

<template>
  <Teleport to="body">
    <Transition
      enter-active-class="transition duration-150 ease-out"
      enter-from-class="opacity-0"
      leave-active-class="transition duration-100 ease-in"
      leave-to-class="opacity-0"
    >
      <div v-if="open" class="fixed inset-0 z-50 lg:bg-transparent">
        <div class="absolute inset-0 bg-bg-deep/75 backdrop-blur-sm" @click="emit('close')" />
        <section
          class="absolute inset-x-0 bottom-0 flex max-h-[82dvh] flex-col rounded-t-2xl border-t border-line bg-bg shadow-2xl lg:inset-y-0 lg:left-auto lg:right-0 lg:max-h-none lg:w-[380px] lg:rounded-none lg:border-l lg:border-t-0"
          role="dialog"
          aria-modal="true"
          aria-labelledby="file-library-title"
          data-testid="file-library"
        >
          <div class="flex items-center justify-between gap-3 border-b border-line px-4 pb-3 pt-3">
            <div class="min-w-0">
              <h2 id="file-library-title" class="text-[15px] font-semibold tracking-tight text-ink">
                File room ini
              </h2>
              <p class="mt-0.5 text-[11.5px] text-quiet">
                Yang kamu kirim dan yang Hermes hasilkan.
              </p>
            </div>
            <button
              type="button"
              class="tap flex items-center justify-center rounded-lg text-quiet hover:text-ink"
              aria-label="Tutup daftar file"
              @click="emit('close')"
            >
              <PhX :size="18" />
            </button>
          </div>

          <div class="min-h-0 flex-1 overflow-y-auto overflow-x-hidden px-3 py-3">
            <p v-if="loading && !loaded" class="px-2 py-6 text-center text-[13px] text-quiet">
              Memuat file…
            </p>

            <div v-else-if="error && files.length === 0" class="mx-1 rounded-xl border border-line px-3 py-3">
              <p class="text-[13px] text-muted">{{ error }}</p>
              <button
                type="button"
                class="mt-2 text-[13px] font-medium text-orchid"
                @click="load"
              >
                Coba lagi
              </button>
            </div>

            <div
              v-else-if="loaded && files.length === 0"
              class="mx-1 flex flex-col items-center rounded-xl border border-dashed border-line px-4 py-10 text-center"
              data-testid="file-library-empty"
            >
              <p class="text-[13.5px] font-medium text-ink">Belum ada file di room ini</p>
              <p class="mt-1.5 max-w-[15rem] text-[12.5px] leading-relaxed text-quiet">
                File yang kamu lampirkan dan file yang Hermes serahkan akan muncul di sini.
              </p>
            </div>

            <ul v-else class="space-y-1.5" data-testid="file-library-list">
              <li
                v-for="file in files"
                :key="file.id"
                class="rounded-xl border border-line bg-surface/60 px-3 py-2.5"
              >
                <div class="flex min-w-0 items-center gap-3">
                  <button
                    v-if="isImage(file)"
                    type="button"
                    class="h-11 w-11 shrink-0 overflow-hidden rounded-lg border border-line bg-bg-deep"
                    :aria-label="`Buka ${file.name}`"
                    data-testid="library-thumbnail"
                    @click="preview = file"
                  >
                    <img
                      :src="fileUrl(file.id)"
                      :alt="file.name"
                      loading="lazy"
                      class="h-full w-full object-cover"
                    />
                  </button>
                  <span
                    v-else
                    class="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-bg-deep text-muted"
                  >
                    <FileIcon :kind="fileKind(file.mime, file.name)" :size="18" />
                  </span>
                  <div class="min-w-0 flex-1">
                    <p class="truncate text-[12.5px] leading-tight text-ink" :title="file.name">
                      {{ file.name }}
                    </p>
                    <p class="mt-0.5 truncate text-[11px] leading-tight text-quiet">
                      {{ file.source === 'agent' ? 'Hermes kirim' : 'Kamu kirim' }} ·
                      {{ formatBytes(file.size) }}
                    </p>
                  </div>
                  <a
                    :href="fileDownloadUrl(file.id)"
                    :download="file.name"
                    class="tap flex items-center justify-center rounded-lg text-muted hover:text-ink"
                    :aria-label="`Unduh ${file.name}`"
                    data-testid="library-download"
                  >
                    <PhDownloadSimple :size="17" />
                  </a>
                  <button
                    type="button"
                    class="tap flex items-center justify-center rounded-lg text-quiet hover:text-ink"
                    :aria-label="`Hapus ${file.name}`"
                    data-testid="library-delete"
                    @click="confirmingId = file.id"
                  >
                    <PhTrash :size="16" />
                  </button>
                </div>

                <div
                  v-if="isVideo(file)"
                  class="mt-2 overflow-hidden rounded-lg border border-line bg-black"
                >
                  <video
                    :src="fileUrl(file.id)"
                    controls
                    playsinline
                    preload="metadata"
                    class="aspect-video max-h-56 w-full object-contain"
                    data-testid="library-video"
                  />
                </div>
                <div
                  v-else-if="isAudio(file)"
                  class="mt-2 rounded-lg border border-line bg-bg-deep/60 px-2 py-1.5"
                >
                  <audio
                    :src="fileUrl(file.id)"
                    controls
                    preload="metadata"
                    class="block w-full [color-scheme:dark]"
                    data-testid="library-audio"
                  />
                </div>

                <div
                  v-if="confirmingId === file.id"
                  class="mt-2 rounded-lg border border-line bg-bg px-3 py-2"
                  data-testid="delete-confirm"
                >
                  <p class="text-[12px] leading-snug text-muted">
                    Hapus <span class="text-ink">{{ file.name }}</span
                    >? File hilang dari room ini dan tidak bisa diunduh lagi.
                  </p>
                  <div class="mt-2 flex items-center gap-2">
                    <button
                      type="button"
                      class="tap rounded-lg bg-violet px-3 text-[12.5px] font-medium text-ink disabled:opacity-60"
                      :disabled="deletingId === file.id"
                      data-testid="delete-confirm-yes"
                      @click="remove(file)"
                    >
                      {{ deletingId === file.id ? 'Menghapus…' : 'Ya, hapus' }}
                    </button>
                    <button
                      type="button"
                      class="tap rounded-lg px-3 text-[12.5px] text-muted hover:text-ink"
                      :disabled="deletingId === file.id"
                      data-testid="delete-cancel"
                      @click="confirmingId = null"
                    >
                      Batal
                    </button>
                  </div>
                </div>
              </li>
            </ul>

            <p v-if="error && files.length > 0" class="mt-2 px-2 text-[12.5px] text-muted" role="alert">
              {{ error }}
            </p>
          </div>
        </section>
      </div>
    </Transition>
  </Teleport>

  <FileLightbox :file="preview" @close="preview = null" />
</template>
