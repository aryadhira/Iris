<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { countdownLabel } from '../lib/timeline'
import type { Continuation } from '../lib/types'

const props = defineProps<{ continuation: Continuation }>()
const emit = defineEmits<{ cancel: [] }>()

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

const label = computed(() => countdownLabel(props.continuation.due_at, now.value))
const attempt = computed(
  () => `${props.continuation.attempt}/3`,
)
</script>

<template>
  <div
    class="flex items-center gap-3 rounded-xl border border-line bg-surface/60 px-3 py-2.5"
    data-testid="continuation-line"
    role="status"
    aria-live="polite"
  >
    <span class="relative flex h-2 w-2 shrink-0" aria-hidden="true">
      <span class="absolute inline-flex h-full w-full animate-ping rounded-full bg-orchid opacity-50" />
      <span class="relative inline-flex h-2 w-2 rounded-full bg-orchid" />
    </span>
    <div class="min-w-0 flex-1">
      <p class="text-[12.5px] leading-snug text-muted">{{ label }}</p>
      <p class="text-[11px] text-quiet">
        menunggu proses yang dijalankan · percobaan {{ attempt }}
      </p>
    </div>
    <button
      type="button"
      class="shrink-0 rounded-lg border border-line px-2.5 text-[11.5px] text-quiet hover:border-orchid/40 hover:text-ink"
      data-testid="continuation-cancel"
      @click="emit('cancel')"
    >
      hentikan
    </button>
  </div>
</template>
