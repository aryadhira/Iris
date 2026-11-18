<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  PhBrain,
  PhCheck,
  PhCircleNotch,
  PhInfo,
  PhListChecks,
  PhWrench,
  PhX,
} from '@phosphor-icons/vue'
import { compactDuration, progressHeading, stepLabel } from '../lib/timeline'
import type { ProgressStep } from '../lib/types'

const props = defineProps<{
  steps: ProgressStep[]
  startedAt: number
  /** How long the server has seen no real event. */
  idleMs: number
  reconnecting: boolean
}>()

const now = ref(Date.now())
let timer: number | undefined

onMounted(() => {
  timer = window.setInterval(() => {
    now.value = Date.now()
  }, 1000)
})

onBeforeUnmount(() => {
  if (timer !== undefined) window.clearInterval(timer)
})

const elapsed = computed(() => turnClock(props.startedAt, now.value))
const waiting = computed(() => props.idleMs >= 20_000)
const current = computed<ProgressStep | undefined>(() => {
  for (let i = props.steps.length - 1; i >= 0; i -= 1) {
    if (props.steps[i].status === 'running') return props.steps[i]
  }
  return props.steps[props.steps.length - 1]
})
const currentLabel = computed(() => (current.value ? stepLabel(current.value) : ''))
const heading = computed(() =>
  progressHeading(props.idleMs, elapsed.value, props.reconnecting),
)

function turnClock(startedAt: number, at: number): string {
  if (!startedAt || startedAt <= 0) return '0:00'
  const seconds = Math.max(0, Math.floor((at - startedAt) / 1000))
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  const rest = seconds % 60
  const clock = `${minutes}:${String(rest).padStart(2, '0')}`
  return hours > 0 ? `${hours}:${clock}` : clock
}

function thinkingExcerpt(step: ProgressStep): string {
  const text = (step.detail ?? '').replace(/\s+/g, ' ').trim()
  return text.length > 80 ? `${text.slice(0, 80)}…` : text
}

function duration(step: ProgressStep): string {
  return compactDuration(step.durationMs)
}
</script>

<template>
  <div class="border-t border-line bg-bg px-3 pt-2.5 lg:px-8" data-testid="turn-progress">
    <div class="mx-auto w-full max-w-3xl overflow-hidden rounded-xl border border-line bg-surface/70">
      <div class="flex min-w-0 items-center gap-2 px-3 pt-2">
        <span class="relative flex h-2 w-2 shrink-0" aria-hidden="true">
          <span class="absolute inline-flex h-full w-full animate-ping rounded-full bg-orchid opacity-50" />
          <span class="relative inline-flex h-2 w-2 rounded-full bg-orchid" />
        </span>
        <p
          class="min-w-0 flex-1 truncate text-[12px] font-medium text-muted"
          role="status"
          aria-live="polite"
          data-testid="turn-progress-header"
        >
          {{ heading }}
        </p>
        <p
          v-if="!waiting && !reconnecting && currentLabel"
          class="min-w-0 max-w-[45%] truncate text-[11.5px] text-quiet"
          data-testid="turn-progress-current"
        >
          {{ currentLabel }}
        </p>
      </div>

      <ol
        v-if="steps.length > 0"
        class="max-h-[30vh] overflow-y-auto overflow-x-hidden px-3 pb-2 pt-1.5"
        data-testid="turn-progress-steps"
      >
        <li
          v-for="step in steps"
          :key="step.seq"
          class="flex min-w-0 items-center gap-1.5 py-[3px] text-[11.5px] leading-tight"
          :class="step.status === 'failed' ? 'text-red-300' : 'text-muted'"
          :data-kind="step.kind"
          :data-status="step.status"
          data-testid="progress-step"
        >
          <PhCircleNotch
            v-if="step.status === 'running'"
            :size="11"
            class="shrink-0 animate-spin text-orchid"
            aria-hidden="true"
          />
          <PhX v-else-if="step.status === 'failed'" :size="11" class="shrink-0" aria-hidden="true" />
          <PhCheck v-else :size="11" class="shrink-0 text-quiet" aria-hidden="true" />

          <PhBrain v-if="step.kind === 'thinking'" :size="12" class="shrink-0 text-quiet" aria-hidden="true" />
          <PhListChecks v-else-if="step.kind === 'plan'" :size="12" class="shrink-0 text-quiet" aria-hidden="true" />
          <PhInfo v-else-if="step.kind === 'status'" :size="12" class="shrink-0 text-quiet" aria-hidden="true" />
          <PhWrench v-else :size="12" class="shrink-0 text-quiet" aria-hidden="true" />

          <span class="min-w-0 flex-1 truncate" data-testid="progress-step-label">
            <template v-if="step.kind === 'thinking'">
              <span class="text-quiet">berpikir…</span>
              <span v-if="thinkingExcerpt(step)" class="text-quiet/80"> {{ thinkingExcerpt(step) }}</span>
            </template>
            <template v-else>
              {{ stepLabel(step) }}
              <span v-if="step.status === 'failed' && step.detail" class="text-red-300/80">
                · {{ step.detail }}
              </span>
            </template>
          </span>

          <span
            class="shrink-0 tabular-nums text-quiet"
            data-testid="progress-step-duration"
          >
            {{ duration(step) }}
          </span>
        </li>
      </ol>
      <p v-else class="px-3 pb-2.5 pt-1 text-[11.5px] text-quiet" data-testid="turn-progress-empty">
        belum ada langkah — menunggu kabar dari Hermes
      </p>
    </div>
  </div>
</template>
