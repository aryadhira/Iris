<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { PhMagnifyingGlass, PhX } from '@phosphor-icons/vue'
import { api } from '../lib/api'
import { resultTime, roleLabel, snippetParts } from '../lib/search'
import type { SearchResult } from '../lib/types'
import { closeRoomSheet } from '../stores/ui'

const router = useRouter()
const emit = defineEmits<{ searching: [boolean] }>()

const query = ref('')
const results = ref<SearchResult[]>([])
const status = ref<'idle' | 'loading' | 'done' | 'error'>('idle')
const errorText = ref('')

let debounce: number | undefined
let sequence = 0

function reset(): void {
  sequence += 1
  if (debounce !== undefined) window.clearTimeout(debounce)
  debounce = undefined
  status.value = 'idle'
  results.value = []
  errorText.value = ''
}

async function run(term: string): Promise<void> {
  const mine = ++sequence
  status.value = 'loading'
  errorText.value = ''
  try {
    const found = await api.search(term, 20)
    if (mine !== sequence) return
    results.value = found
    status.value = 'done'
  } catch {
    if (mine !== sequence) return
    results.value = []
    errorText.value = 'Pencarian gagal. Periksa koneksi lalu coba lagi.'
    status.value = 'error'
  }
}

watch(query, (value) => {
  const term = value.trim()
  emit('searching', term !== '')
  if (debounce !== undefined) window.clearTimeout(debounce)
  if (term === '') {
    reset()
    return
  }
  status.value = 'loading'
  debounce = window.setTimeout(() => void run(term), 260)
})

function clear(): void {
  query.value = ''
  reset()
}

function open(result: SearchResult): void {
  closeRoomSheet()
  query.value = ''
  reset()
  void router.push({ path: `/r/${result.roomId}`, query: { m: result.messageId } })
}

onBeforeUnmount(() => {
  if (debounce !== undefined) window.clearTimeout(debounce)
})
</script>

<template>
  <div class="px-3 pb-2">
    <div class="relative">
      <PhMagnifyingGlass
        :size="15"
        class="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-quiet"
      />
      <input
        v-model="query"
        type="search"
        aria-label="Cari pesan di semua room"
        inputmode="search"
        autocomplete="off"
        placeholder="Cari pesan…"
        class="h-9 w-full rounded-xl border border-line bg-surface/60 pl-8 pr-8 text-[13px] text-ink placeholder:text-quiet focus:border-orchid/60 focus:outline-none"
        data-testid="room-search"
      />
      <button
        v-if="query !== ''"
        type="button"
        class="tap absolute right-0 top-0 flex items-center justify-center rounded-lg text-quiet hover:text-ink"
        aria-label="Hapus pencarian"
        data-testid="room-search-clear"
        @click="clear"
      >
        <PhX :size="15" />
      </button>
    </div>

    <div v-if="query.trim() !== ''" class="mt-2" data-testid="search-panel">
      <p v-if="status === 'loading'" class="px-1 py-1.5 text-[12px] text-quiet">Mencari…</p>

      <div v-else-if="status === 'error'" class="rounded-xl border border-line px-2.5 py-2">
        <p class="text-[12px] leading-relaxed text-muted">{{ errorText }}</p>
        <button
          type="button"
          class="mt-1 text-[12px] font-medium text-orchid hover:text-orchid/80"
          @click="run(query.trim())"
        >
          Coba lagi
        </button>
      </div>

      <p
        v-else-if="status === 'done' && results.length === 0"
        class="rounded-xl border border-dashed border-line px-2.5 py-2.5 text-[12px] leading-relaxed text-quiet"
        data-testid="search-empty"
      >
        Tidak ada pesan yang cocok dengan
        <span class="text-muted">“{{ query.trim() }}”</span>.
      </p>

      <ul v-else-if="results.length > 0" class="space-y-0.5">
        <li v-for="result in results" :key="result.messageId">
          <button
            type="button"
            class="tap w-full rounded-xl px-2.5 py-2 text-left transition-colors hover:bg-surface"
            data-testid="search-result"
            @click="open(result)"
          >
            <span class="flex items-baseline justify-between gap-2">
              <span class="truncate text-[12.5px] font-medium text-ink">{{ result.roomName }}</span>
              <span class="shrink-0 text-[10.5px] text-quiet">{{ resultTime(result) }}</span>
            </span>
            <span class="mt-0.5 block text-[11px] text-quiet">
              {{ roleLabel(result.role) }}
            </span>
            <span class="mt-1 block line-clamp-2 text-[12px] leading-snug text-muted">
              <template v-for="(part, index) in snippetParts(result.snippet)" :key="index">
                <mark v-if="part.hit" class="rounded-sm bg-violet/40 px-0.5 text-ink">{{ part.text }}</mark>
                <template v-else>{{ part.text }}</template>
              </template>
            </span>
          </button>
        </li>
      </ul>
    </div>
  </div>
</template>
