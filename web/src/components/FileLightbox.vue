<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { PhX } from '@phosphor-icons/vue'
import { fileUrl, isAudio, isImage, isVideo } from '../lib/files'
import type { FileAttachment } from '../lib/types'

const props = defineProps<{ file: FileAttachment | null }>()
const emit = defineEmits<{ close: [] }>()

const videoEl = ref<HTMLVideoElement | null>(null)

function close(): void {
  pauseVideo()
  emit('close')
}

function pauseVideo(): void {
  if (videoEl.value && !videoEl.value.paused) videoEl.value.pause()
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape' && props.file) close()
}

// Closing must stop the sound and the picture, not leave either playing behind the room.
watch(
  () => props.file,
  (file) => {
    if (!file) pauseVideo()
  },
)

onMounted(() => window.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
  pauseVideo()
})
</script>

<template>
  <Teleport to="body">
    <div
      v-if="file"
      class="fixed inset-0 z-[60] flex items-center justify-center bg-bg-deep/90 p-4 backdrop-blur-sm"
      role="dialog"
      aria-modal="true"
      :aria-label="`Pratinjau ${file.name}`"
      :data-media-kind="isVideo(file) ? 'video' : isAudio(file) ? 'audio' : 'image'"
      data-testid="image-lightbox"
      @click.self="close"
    >
      <button
        type="button"
        class="tap absolute right-3 top-3 flex items-center justify-center rounded-xl border border-line bg-bg/80 text-ink"
        aria-label="Tutup pratinjau"
        @click="close"
      >
        <PhX :size="20" />
      </button>
      <figure class="flex max-h-full max-w-full flex-col items-center gap-3">
        <img
          v-if="isImage(file)"
          :src="fileUrl(file.id)"
          :alt="file.name"
          class="max-h-[80dvh] max-w-full rounded-xl object-contain"
          data-testid="lightbox-image"
        />
        <div
          v-else-if="isVideo(file)"
          class="flex aspect-video max-h-[80dvh] w-[min(56rem,calc(100vw-2rem))] items-center justify-center rounded-xl bg-black"
        >
          <video
            ref="videoEl"
            :src="fileUrl(file.id)"
            controls
            playsinline
            preload="metadata"
            class="h-full w-full rounded-xl object-contain"
            data-testid="lightbox-video"
          />
        </div>
        <div v-else-if="isAudio(file)" class="w-[min(28rem,calc(100vw-2rem))]">
          <audio
            :src="fileUrl(file.id)"
            controls
            preload="metadata"
            class="block w-full [color-scheme:dark]"
            data-testid="lightbox-audio"
          />
        </div>
        <figcaption class="max-w-full truncate px-2 text-center text-[12px] text-muted">
          {{ file.name }}
        </figcaption>
      </figure>
    </div>
  </Teleport>
</template>
