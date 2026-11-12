<script setup lang="ts">
import { PhX } from '@phosphor-icons/vue'
import { fileUrl } from '../lib/files'
import type { FileAttachment } from '../lib/types'

defineProps<{ file: FileAttachment | null }>()
const emit = defineEmits<{ close: [] }>()
</script>

<template>
  <Teleport to="body">
    <div
      v-if="file"
      class="fixed inset-0 z-[60] flex items-center justify-center bg-bg-deep/90 p-4 backdrop-blur-sm"
      role="dialog"
      aria-modal="true"
      :aria-label="`Pratinjau ${file.name}`"
      data-testid="image-lightbox"
      @click.self="emit('close')"
    >
      <button
        type="button"
        class="tap absolute right-3 top-3 flex items-center justify-center rounded-xl border border-line bg-bg/80 text-ink"
        aria-label="Tutup pratinjau"
        @click="emit('close')"
      >
        <PhX :size="20" />
      </button>
      <figure class="flex max-h-full max-w-full flex-col items-center gap-3">
        <img
          :src="fileUrl(file.id)"
          :alt="file.name"
          class="max-h-[80dvh] max-w-full rounded-xl object-contain"
        />
        <figcaption class="max-w-full truncate px-2 text-center text-[12px] text-muted">
          {{ file.name }}
        </figcaption>
      </figure>
    </div>
  </Teleport>
</template>
