<script setup lang="ts">
import { computed } from 'vue'
import { PhDownloadSimple } from '@phosphor-icons/vue'
import FileIcon from './FileIcon.vue'
import { artifactCard } from '../lib/files'
import type { FileAttachment } from '../lib/types'

const props = defineProps<{ file: FileAttachment }>()
const card = computed(() => artifactCard(props.file))
</script>

<template>
  <a
    :href="card.downloadUrl"
    :download="file.name"
    class="flex min-w-0 items-center gap-3 rounded-xl border border-line bg-surface/80 px-3 py-2.5 transition-colors hover:border-orchid/40"
    data-testid="artifact-card"
  >
    <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-bg-deep text-orchid">
      <FileIcon :kind="card.kind" :size="19" />
    </span>
    <span class="min-w-0 flex-1">
      <span class="block truncate text-[12.5px] font-medium leading-tight text-ink" :title="card.title">
        {{ card.title }}
      </span>
      <span class="mt-0.5 block truncate text-[11px] leading-tight text-quiet">{{ card.meta }}</span>
    </span>
    <span
      class="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border border-line text-muted"
      aria-hidden="true"
    >
      <PhDownloadSimple :size="17" />
    </span>
    <span class="sr-only">Unduh {{ file.name }}</span>
  </a>
</template>
