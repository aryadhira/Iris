<script setup lang="ts">
import { computed } from 'vue'

// Two crops of the one master: the full mark for large sizes, the reduced
// mark (mic + teardrop) for anything the size of an avatar or a tab.
const props = withDefaults(
  defineProps<{
    size?: number
    variant?: 'full' | 'mark'
    alt?: string
  }>(),
  { size: 32, variant: 'mark', alt: 'Iris' },
)

const src = computed(() => (props.variant === 'full' ? '/icons/icon-192.png' : '/icons/mark-32.png'))
</script>

<template>
  <img
    :src="src"
    :width="size"
    :height="size"
    :alt="alt"
    :style="{ width: `${size}px`, height: `${size}px` }"
    class="shrink-0 select-none"
    draggable="false"
  />
</template>
