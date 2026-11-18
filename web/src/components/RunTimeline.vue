<script setup lang="ts">
import { computed } from 'vue'
import { PhCaretRight } from '@phosphor-icons/vue'
import { compactDuration, durationLabel, timelineRows } from '../lib/timeline'
import type { MessageMeta } from '../lib/types'

const props = defineProps<{ meta?: MessageMeta; runId?: string }>()
const rows = computed(() => timelineRows(props.meta))
const usesSteps = computed(() => (props.meta?.steps?.length ?? 0) > 0)
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
        v-for="row in rows"
        :key="row.key"
        class="flex min-w-0 items-baseline gap-x-2 text-[11.5px] leading-snug"
        :class="row.failed ? 'text-red-300' : ''"
        :data-status="row.status"
        :data-kind="row.kind"
      >
        <span class="min-w-0 flex-1 truncate" :class="row.failed ? '' : 'text-muted'">
          {{ row.label }}
          <span
            v-if="row.kind === 'thinking' && row.detail"
            class="text-quiet"
          >
            · {{ row.detail.length > 80 ? `${row.detail.slice(0, 80)}…` : row.detail }}
          </span>
          <span v-else-if="row.failed && row.detail" class="text-red-300/80"> · {{ row.detail }}</span>
        </span>
        <span class="shrink-0 tabular-nums text-quiet">
          {{ usesSteps ? compactDuration(row.durationMs) : durationLabel(row.durationMs) }}
        </span>
        <span v-if="row.failed" class="shrink-0 text-red-300">gagal</span>
        <span v-else-if="row.status === 'running'" class="shrink-0 text-quiet">berjalan</span>
      </li>
    </ol>
  </details>
</template>
