<script setup lang="ts">
function humanSize(bytes?: number): string {
  if (!bytes || bytes < 0) return ''
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}
import { computed, ref } from 'vue'
import { PhDownloadSimple } from '@phosphor-icons/vue'
import ArtifactCard from './ArtifactCard.vue'
import BrandMark from './BrandMark.vue'
import FileIcon from './FileIcon.vue'
import FileLightbox from './FileLightbox.vue'
import FormattedText from './FormattedText.vue'
import RunTimeline from './RunTimeline.vue'
import SpeakerButton from './SpeakerButton.vue'
import { fileKind, fileTypeLabel, fileUrl, formatBytes, isImage } from '../lib/files'
import type { ChatMessage, FileAttachment } from '../lib/types'

const props = withDefaults(
  defineProps<{
    message: ChatMessage
    streaming?: boolean
    speaking?: boolean
    loadingSpeech?: boolean
  }>(),
  { streaming: false, speaking: false, loadingSpeech: false },
)

const emit = defineEmits<{ speak: [] }>()

const preview = ref<FileAttachment | null>(null)
const attachments = computed(() => props.message.attachments ?? [])
const images = computed(() => attachments.value.filter((file) => isImage(file)))
const files = computed(() => attachments.value.filter((file) => !isImage(file)))
const artifacts = computed(() => (props.message.role === 'assistant' ? attachments.value : []))

const time = computed(() => {
  const date = new Date(props.message.createdAt)
  if (Number.isNaN(date.getTime())) return ''
  return new Intl.DateTimeFormat('id-ID', { hour: '2-digit', minute: '2-digit' }).format(date)
})
</script>

<template>
  <article v-if="message.role === 'user'" class="flex justify-end" data-testid="user-message">
    <div class="min-w-0 max-w-[min(85%,34rem)]">
      <div class="overflow-hidden rounded-2xl rounded-br-md border border-line bg-surface">
        <div
          v-if="images.length > 0"
          class="grid gap-1 p-1.5"
          :class="images.length === 1 ? 'grid-cols-1' : 'grid-cols-2'"
        >
          <button
            v-for="image in images"
            :key="image.id"
            type="button"
            class="group relative overflow-hidden rounded-xl border border-line bg-bg-deep"
            :aria-label="`Buka ${image.name}`"
            data-testid="message-thumbnail"
            @click="preview = image"
          >
            <img
              :src="fileUrl(image.id)"
              :alt="image.name"
              loading="lazy"
              class="max-h-52 w-full object-cover transition-opacity group-hover:opacity-90"
            />
            <span
              class="pointer-events-none absolute inset-x-0 bottom-0 flex items-center justify-between gap-2 bg-gradient-to-t from-black/80 to-transparent px-2 pb-1.5 pt-6 text-[10.5px] text-ink/90"
            >
              <span class="truncate">{{ image.name }}</span>
              <span class="shrink-0 text-ink/60">{{ humanSize(image.size) }}</span>
            </span>
          </button>
        </div>

        <div v-if="files.length > 0" class="flex flex-col gap-1.5 px-2 pt-2">
          <a
            v-for="file in files"
            :key="file.id"
            :href="fileUrl(file.id)"
            :download="file.name"
            class="flex min-w-0 items-center gap-2 rounded-lg border border-line bg-bg-deep/60 px-2 py-1.5 hover:border-orchid/40"
            data-testid="message-file-chip"
          >
            <FileIcon :kind="fileKind(file.mime, file.name)" :size="16" class="text-muted" />
            <span class="min-w-0 flex-1 truncate text-[11.5px] text-ink" :title="file.name">
              {{ file.name }}
            </span>
            <span class="shrink-0 text-[10.5px] text-quiet">
              {{ fileTypeLabel(file.mime, file.name) }} · {{ formatBytes(file.size) }}
            </span>
            <PhDownloadSimple :size="14" class="shrink-0 text-quiet" aria-hidden="true" />
          </a>
        </div>

        <p
          v-if="message.body.trim() !== ''"
          class="whitespace-pre-wrap break-words px-4 py-2.5 text-[14px] leading-relaxed text-ink"
        >
          {{ message.body }}
        </p>
        <div v-else class="h-2" aria-hidden="true" />
      </div>
      <p class="mt-1 pr-1 text-right text-[11px] text-quiet">
        <time v-if="time" :datetime="message.createdAt">Kamu · {{ time }}</time>
        <span v-else>Kamu</span>
      </p>
    </div>
  </article>

  <article
    v-else-if="message.role === 'assistant'"
    class="flex gap-3"
    :data-testid="streaming ? 'streaming-message' : 'assistant-message'"
  >
    <BrandMark :size="26" class="mt-0.5" />
    <div class="min-w-0 flex-1">
      <div class="mb-1 flex min-h-[24px] items-center gap-1.5">
        <p class="text-[11px] text-quiet">
          <time v-if="time" :datetime="message.createdAt">Hermes · {{ time }}</time>
          <span v-else>Hermes</span>
        </p>
        <SpeakerButton
          :speaking="speaking"
          :loading="loadingSpeech"
          @toggle="emit('speak')"
        />
        <span v-if="speaking" class="text-[11px] text-quiet" data-testid="speaking-state">
          membaca…
        </span>
        <span v-else-if="loadingSpeech" class="text-[11px] text-quiet">menyiapkan suara…</span>
      </div>
      <div class="text-[14px] leading-relaxed text-ink">
        <FormattedText :text="message.body" />
        <span
          v-if="streaming"
          class="ml-0.5 inline-block h-[1.05em] w-[2px] translate-y-[0.15em] animate-pulse bg-orchid"
          aria-hidden="true"
        />
      </div>
      <div v-if="artifacts.length > 0" class="mt-2 space-y-1.5" data-testid="artifact-list">
        <ArtifactCard v-for="file in artifacts" :key="file.id" :file="file" />
      </div>
      <RunTimeline :meta="message.meta" :run-id="message.runId" />
      <p v-if="message.incomplete" class="mt-1.5 text-[12px] text-quiet">
        Balasan terputus — mungkin belum lengkap.
      </p>
    </div>
  </article>

  <article v-else class="py-1 text-center text-[12px] leading-relaxed text-quiet">
    {{ message.body }}
  </article>

  <FileLightbox :file="preview" @close="preview = null" />
</template>
