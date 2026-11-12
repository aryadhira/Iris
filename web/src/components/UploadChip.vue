<script setup lang="ts">
import { computed } from 'vue'
import { PhX } from '@phosphor-icons/vue'
import FileIcon from './FileIcon.vue'
import { fileKind, fileTypeLabel, formatBytes } from '../lib/files'
import type { PendingAttachment } from '../stores/attachments'

const props = defineProps<{ item: PendingAttachment }>()
const emit = defineEmits<{ remove: [] }>()

const kind = computed(() => fileKind(props.item.mime ?? '', props.item.name))
const label = computed(() => fileTypeLabel(props.item.mime ?? '', props.item.name))
const failed = computed(() => props.item.status === 'error')
</script>

<template>
  <div
    class="flex min-w-0 max-w-[15rem] items-center gap-2 rounded-xl border px-2 py-1.5"
    :class="failed ? 'border-orchid/60 bg-orchid/10' : 'border-line bg-bg-deep/70'"
    data-testid="upload-chip"
  >
    <FileIcon :kind="kind" :size="17" class="text-muted" />
    <div class="min-w-0 flex-1">
      <p class="truncate text-[11.5px] leading-tight text-ink" :title="item.name">{{ item.name }}</p>
      <p v-if="failed" class="truncate text-[10.5px] leading-tight text-orchid" :title="item.error">
        {{ item.error }}
      </p>
      <p v-else-if="item.status === 'uploading'" class="text-[10.5px] leading-tight text-quiet">
        {{ label }} · {{ formatBytes(item.size) }} · {{ item.progress }}%
      </p>
      <p v-else class="text-[10.5px] leading-tight text-quiet">
        {{ label }} · {{ formatBytes(item.size) }} · siap
      </p>
      <div
        v-if="item.status === 'uploading'"
        class="mt-1 h-0.5 w-full overflow-hidden rounded-full bg-line"
        role="progressbar"
        :aria-valuenow="item.progress"
        aria-valuemin="0"
        aria-valuemax="100"
      >
        <span class="block h-full bg-orchid transition-[width]" :style="{ width: `${item.progress}%` }" />
      </div>
    </div>
    <button
      type="button"
      class="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg text-quiet hover:text-ink"
      :aria-label="`Hapus lampiran ${item.name}`"
      data-testid="remove-upload"
      @click="emit('remove')"
    >
      <PhX :size="14" />
    </button>
  </div>
</template>
