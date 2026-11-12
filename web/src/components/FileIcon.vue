<script setup lang="ts">
import { computed } from 'vue'
import {
  PhFile,
  PhFileDoc,
  PhFileImage,
  PhFilePdf,
  PhFileText,
  PhFileXls,
  PhFileZip,
} from '@phosphor-icons/vue'
import type { FileKind } from '../lib/files'

const props = withDefaults(defineProps<{ kind: FileKind; size?: number }>(), { size: 20 })

const icons = {
  image: PhFileImage,
  pdf: PhFilePdf,
  doc: PhFileDoc,
  sheet: PhFileXls,
  archive: PhFileZip,
  text: PhFileText,
  other: PhFile,
}

const icon = computed(() => icons[props.kind] ?? PhFile)
</script>

<template>
  <component :is="icon" :size="size" class="shrink-0" aria-hidden="true" />
</template>
