<script setup lang="ts">
import { computed } from 'vue'

interface TextSegment {
  kind: 'text'
  value: string
}
interface CodeSegment {
  kind: 'code'
  value: string
  lang: string
}
type Segment = TextSegment | CodeSegment

const props = defineProps<{ text: string }>()

/**
 * Split fenced code from prose. Text is rendered as text nodes only — no
 * v-html, no markdown dependency. An unfinished fence (still streaming) is
 * shown as code, because that is what it will become.
 */
const segments = computed<Segment[]>(() => {
  const source = props.text
  const result: Segment[] = []
  let cursor = 0

  while (cursor < source.length) {
    const open = source.indexOf('```', cursor)
    if (open === -1) {
      result.push({ kind: 'text', value: source.slice(cursor) })
      break
    }
    if (open > cursor) result.push({ kind: 'text', value: source.slice(cursor, open) })

    const rest = source.slice(open + 3)
    const close = rest.indexOf('```')
    const block = close === -1 ? rest : rest.slice(0, close)
    const newline = block.indexOf('\n')
    const lang = (newline === -1 ? block : block.slice(0, newline)).trim()
    const code = newline === -1 ? block : block.slice(newline + 1)

    result.push({ kind: 'code', value: close === -1 ? code : block.slice(newline + 1), lang })
    if (close === -1) break
    cursor = open + 3 + close + 3
  }

  return result
})
</script>

<template>
  <div class="min-w-0">
    <template v-for="(segment, index) in segments" :key="index">
      <pre
        v-if="segment.kind === 'code'"
        class="my-2 max-w-full overflow-x-auto rounded-xl border border-line bg-bg-deep p-3 font-mono text-[12.5px] leading-relaxed text-ink"
      ><code>{{ segment.value }}</code></pre>
      <p v-else class="whitespace-pre-wrap break-words">{{ segment.value }}</p>
    </template>
  </div>
</template>
