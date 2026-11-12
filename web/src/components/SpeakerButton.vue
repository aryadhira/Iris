<script setup lang="ts">
import { PhSpeakerHigh, PhStop } from '@phosphor-icons/vue'

withDefaults(
  defineProps<{
    speaking?: boolean
    loading?: boolean
  }>(),
  { speaking: false, loading: false },
)

const emit = defineEmits<{ toggle: [] }>()
</script>

<template>
  <button
    type="button"
    class="tap -my-1 flex shrink-0 items-center justify-center rounded-lg transition-colors"
    :class="speaking || loading ? 'text-orchid' : 'text-quiet hover:text-ink'"
    :aria-label="speaking ? 'Hentikan bacaan' : 'Bacakan pesan ini'"
    :aria-pressed="speaking"
    :data-state="loading ? 'loading' : speaking ? 'playing' : 'idle'"
    data-testid="speaker-button"
    @click="emit('toggle')"
  >
    <PhStop v-if="speaking" :size="14" weight="fill" />
    <PhSpeakerHigh v-else :size="16" :class="{ 'animate-pulse': loading }" />
  </button>
</template>
