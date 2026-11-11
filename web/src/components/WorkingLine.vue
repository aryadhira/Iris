<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { elapsedLabel, humanToolName } from '../lib/format'

const props = defineProps<{
  startedAt: number
  tool: string
  reconnecting: boolean
}>()

// A ticking second is the difference between "working" and "frozen".
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

const elapsed = computed(() => elapsedLabel(props.startedAt || now.value, now.value))
const toolLabel = computed(() => (props.tool ? humanToolName(props.tool) : ''))
</script>

<template>
  <div class="flex flex-col gap-0.5" role="status" aria-live="polite" data-testid="working-state">
    <p class="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[13px] text-muted">
      <span class="relative flex h-2 w-2" aria-hidden="true">
        <span class="absolute inline-flex h-full w-full animate-ping rounded-full bg-orchid opacity-50" />
        <span class="relative inline-flex h-2 w-2 rounded-full bg-orchid" />
      </span>
      <span>{{ reconnecting ? 'Koneksi terputus — menyambung ulang' : 'Bekerja' }}</span>
      <span class="text-quiet">· {{ elapsed }}</span>
    </p>
    <p v-if="toolLabel && !reconnecting" class="text-[12px] text-quiet">
      Sedang {{ toolLabel.toLowerCase() }}
    </p>
    <p v-else-if="!reconnecting" class="text-[12px] text-quiet">Menunggu Hermes menjawab…</p>
  </div>
</template>
