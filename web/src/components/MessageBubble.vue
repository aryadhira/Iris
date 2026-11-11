<script setup lang="ts">
import { computed } from 'vue'
import BrandMark from './BrandMark.vue'
import FormattedText from './FormattedText.vue'
import type { ChatMessage } from '../lib/types'

const props = withDefaults(
  defineProps<{
    message: ChatMessage
    streaming?: boolean
  }>(),
  { streaming: false },
)

const time = computed(() => {
  const date = new Date(props.message.createdAt)
  if (Number.isNaN(date.getTime())) return ''
  return new Intl.DateTimeFormat('id-ID', { hour: '2-digit', minute: '2-digit' }).format(date)
})
</script>

<template>
  <article v-if="message.role === 'user'" class="flex justify-end" data-testid="user-message">
    <div class="min-w-0 max-w-[min(85%,34rem)]">
      <div class="rounded-2xl rounded-br-md border border-line bg-surface px-4 py-2.5">
        <p class="whitespace-pre-wrap break-words text-[14px] leading-relaxed text-ink">
          {{ message.body }}
        </p>
      </div>
      <p class="mt-1 pr-1 text-right text-[11px] text-quiet">
        <time v-if="time" :datetime="message.createdAt">Kamu · {{ time }}</time>
        <span v-else>Kamu</span>
      </p>
    </div>
  </article>

  <article
    v-else-if="message.role === 'assistant'"
    class="flex gap-3"
    :data-testid="streaming ? 'streaming-message' : 'assistant-message'"
  >
    <BrandMark :size="26" class="mt-0.5" />
    <div class="min-w-0 flex-1">
      <p class="mb-1 text-[11px] text-quiet">
        <time v-if="time" :datetime="message.createdAt">Hermes · {{ time }}</time>
        <span v-else>Hermes</span>
      </p>
      <div class="text-[14px] leading-relaxed text-ink">
        <FormattedText :text="message.body" />
        <span
          v-if="streaming"
          class="ml-0.5 inline-block h-[1.05em] w-[2px] translate-y-[0.15em] animate-pulse bg-orchid"
          aria-hidden="true"
        />
      </div>
      <p v-if="message.incomplete" class="mt-1.5 text-[12px] text-quiet">
        Balasan terputus — mungkin belum lengkap.
      </p>
    </div>
  </article>

  <article v-else class="py-1 text-center text-[12px] leading-relaxed text-quiet">
    {{ message.body }}
  </article>
</template>
