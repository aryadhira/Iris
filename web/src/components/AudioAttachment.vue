<script setup lang="ts">
import { PhDownloadSimple, PhMusicNotes } from '@phosphor-icons/vue'
import { fileDownloadUrl, fileTypeLabel, fileUrl, formatBytes } from '../lib/files'
import type { FileAttachment } from '../lib/types'

defineProps<{ file: FileAttachment }>()
</script>

<template>
  <div
    class="min-w-0 overflow-hidden rounded-xl border border-line bg-surface/80"
    data-testid="audio-attachment"
  >
    <div class="flex min-w-0 items-center gap-2.5 px-2.5 py-2">
      <span class="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-bg-deep text-orchid">
        <PhMusicNotes :size="16" aria-hidden="true" />
      </span>
      <span class="min-w-0 flex-1">
        <span class="block truncate text-[12px] leading-tight text-ink" :title="file.name">
          {{ file.name }}
        </span>
        <span class="mt-0.5 block truncate text-[10.5px] leading-tight text-quiet">
          {{ fileTypeLabel(file.mime, file.name) }} · {{ formatBytes(file.size) }}
        </span>
      </span>
      <a
        :href="fileDownloadUrl(file.id)"
        :download="file.name"
        class="tap flex shrink-0 items-center justify-center rounded-lg text-quiet hover:text-ink"
        :aria-label="`Unduh ${file.name}`"
        data-testid="audio-download"
      >
        <PhDownloadSimple :size="16" aria-hidden="true" />
      </a>
    </div>
    <div class="px-2 pb-2">
      <!-- The native control is the player; a dark color-scheme keeps it in the room's palette.
           No autoplay and no loop: Boss decides when sound starts. -->
      <audio
        :src="fileUrl(file.id)"
        controls
        preload="metadata"
        class="block w-full [color-scheme:dark]"
        data-testid="audio-element"
      />
    </div>
  </div>
</template>
