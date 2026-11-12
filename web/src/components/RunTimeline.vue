<script setup lang="ts">
import { computed } from 'vue'
import { PhCaretRight } from '@phosphor-icons/vue'
import { durationLabel, timelineRows } from '../lib/timeline'
import type { MessageMeta } from '../lib/types'

const props = defineProps<{ meta?: MessageMeta; runId?: string }>()
const rows = computed(() => timelineRows(props.meta))
</script>

<template>
  <details v-if="rows.length > 0" class="group mt-2" data-testid="run-timeline">
    <summary
      class="inline-flex cursor-pointer list-none items-center gap-1 rounded text-[11.5px] text-quiet hover:text-muted"
    >
      <PhCaretRight
        :size="11"
        class="transition-transform group-open:rotate-90"
        aria-hidden="true"
      />
      apa yang Hermes lakukan
    </summary>
    <p
      v-if="runId"
      class="mt-1 font-mono text-[10.5px] leading-snug text-quiet"
      data-testid="run-id"
    >
      run {{ runId }}
    </p>
    <ol class="mt-1.5 space-y-1 border-l border-line pl-3" data-testid="run-timeline-list">
      <li
        v-for="(row, index) in rows"
        :key="`${row.name}-${index}`"
        class="flex flex-wrap items-baseline gap-x-2 text-[11.5px] leading-snug"
        :data-status="row.status"
      >
        <span class="text-muted">{{ row.label }}</span>
        <span class="text-quiet">{{ durationLabel(row.durationMs) }}</span>
        <span v-if="row.failed" class="text-orchid">gagal</span>
        <span v-else-if="row.status === 'running'" class="text-quiet">berjalan</span>
      </li>
    </ol>
  </details>
</template>
