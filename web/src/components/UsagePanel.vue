<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { PhChartBar, PhWarningCircle } from '@phosphor-icons/vue'
import { api } from '../lib/api'
import { formatUSD } from '../lib/format'
import type { UsageResponse } from '../lib/types'

const props = defineProps<{ roomId: string; open: boolean }>()

const usage = ref<UsageResponse | null>(null)
const loading = ref(false)
const error = ref('')
const loaded = ref(false)

interface Bar {
  day: string
  label: string
  turns: number
  value: number
  height: number
  unknownCostTurns: number
}

const metric = computed<'cost' | 'turns'>(() => {
  if (!usage.value) return 'cost'
  return usage.value.days.some((day) => day.cost_usd > 0) ? 'cost' : 'turns'
})

/** Always 14 columns, including days with no usage: an empty day is a zero. */
const bars = computed<Bar[]>(() => {
  const byDay = new Map<string, { turns: number; cost: number; unknown: number }>()
  for (const day of usage.value?.days ?? []) {
    byDay.set(day.day, {
      turns: day.turns,
      cost: day.cost_usd,
      unknown: day.unknown_cost_turns,
    })
  }
  const out: Bar[] = []
  const today = new Date()
  for (let offset = 13; offset >= 0; offset -= 1) {
    const date = new Date(today.getFullYear(), today.getMonth(), today.getDate() - offset)
    const key = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
    const found = byDay.get(key)
    out.push({
      day: key,
      label: String(date.getDate()),
      turns: found?.turns ?? 0,
      value: (metric.value === 'cost' ? found?.cost : found?.turns) ?? 0,
      height: 0,
      unknownCostTurns: found?.unknown ?? 0,
    })
  }
  const max = Math.max(...out.map((bar) => bar.value), 0)
  for (const bar of out) {
    bar.height = max > 0 ? Math.max(2, Math.round((bar.value / max) * 100)) : 2
  }
  return out
})

const chartCaption = computed(() => {
  if (!usage.value || usage.value.totals.turns === 0) return ''
  return metric.value === 'cost'
    ? 'Biaya per hari, 14 hari terakhir.'
    : 'Biaya belum diketahui untuk model ini; tinggi batang adalah jumlah turn.'
})

const chip = computed(() => {
  const today = usage.value?.today
  if (!today || today.turns === 0) return 'hari ini belum ada turn'
  const parts = [`hari ini ${formatUSD(today.cost_usd)}`, `${today.turns} turn`]
  if (today.unknown_cost_turns > 0) parts.push(`${today.unknown_cost_turns} tanpa harga`)
  return parts.join(' · ')
})

const topRooms = computed(() => (usage.value?.rooms ?? []).slice(0, 5))

const priceNote = computed(() => {
  const prices = usage.value?.prices
  if (!prices) return ''
  if (prices.error) return `Harga belum bisa dibaca: ${prices.error}`
  if (prices.entries === 0) return 'harga model ini belum ada di prices.json — token tetap dicatat.'
  return ''
})

function barTitle(bar: Bar): string {
  if (bar.turns === 0) return `${bar.day}: tidak ada turn`
  if (metric.value === 'cost') {
    return `${bar.day}: ${formatUSD(usage.value?.days.find((day) => day.day === bar.day)?.cost_usd ?? 0)} · ${bar.turns} turn`
  }
  return `${bar.day}: ${bar.turns} turn`
}

async function load(): Promise<void> {
  if (!props.roomId) return
  loading.value = true
  error.value = ''
  try {
    usage.value = await api.getUsage(props.roomId)
    loaded.value = true
  } catch {
    error.value = 'Catatan biaya belum bisa dimuat. Coba lagi.'
  } finally {
    loading.value = false
  }
}

watch(
  () => [props.open, props.roomId],
  async ([open]) => {
    if (open) await load()
  },
  { immediate: true },
)
</script>

<template>
  <section aria-labelledby="usage-heading" data-testid="usage-panel">
    <div class="flex items-center justify-between px-2 pb-2">
      <h3 id="usage-heading" class="flex items-center gap-1.5 text-[12px] font-semibold uppercase tracking-wide text-muted">
        <PhChartBar :size="14" />
        Biaya
      </h3>
      <span class="text-[11px] text-quiet" data-testid="usage-chip">{{ chip }}</span>
    </div>

    <div class="mx-1 rounded-xl border border-line bg-surface/60 px-3 py-3">
      <p v-if="loading && !loaded" class="py-4 text-center text-[13px] text-quiet">Memuat biaya…</p>

      <div v-else-if="error && !loaded" class="py-2">
        <p class="text-[13px] text-muted">{{ error }}</p>
        <button type="button" class="tap mt-2 text-[13px] font-medium text-orchid" @click="load">
          Coba lagi
        </button>
      </div>

      <template v-else>
        <div
          v-if="loaded && (usage?.totals.turns ?? 0) === 0"
          class="py-4 text-center"
          data-testid="usage-empty"
        >
          <p class="text-[13px] text-quiet">Belum ada pemakaian tercatat.</p>
          <p class="mt-1 text-[11.5px] leading-relaxed text-quiet">
            Setelah Hermes menjawab, token dan biaya turn itu muncul di sini.
          </p>
        </div>

        <template v-else>
          <div class="flex items-end gap-[3px]" data-testid="usage-chart" :data-metric="metric">
            <div
              v-for="bar in bars"
              :key="bar.day"
              class="flex min-w-0 flex-1 flex-col items-center justify-end"
              :title="barTitle(bar)"
              :data-day="bar.day"
              :data-turns="bar.turns"
            >
              <div
                class="w-full rounded-t-[2px]"
                :class="bar.turns === 0 ? 'bg-line' : 'bg-orchid/80'"
                :style="{ height: `${Math.max(2, Math.round((bar.height / 100) * 72))}px` }"
              />
              <span class="mt-1 text-[8.5px] leading-none text-quiet">{{ bar.label }}</span>
            </div>
          </div>
          <p class="mt-2 text-[10.5px] leading-snug text-quiet">{{ chartCaption }}</p>

          <div class="mt-3 grid grid-cols-2 gap-2 border-t border-line pt-2.5 text-[11.5px]">
            <p class="text-quiet">
              Total
              <span class="block text-[13px] text-ink" data-testid="usage-total">
                {{ formatUSD(usage?.totals.cost_usd ?? 0) }} · {{ usage?.totals.turns ?? 0 }} turn
              </span>
            </p>
            <p class="text-quiet">
              Token
              <span class="block text-[13px] text-ink" data-testid="usage-tokens">
                {{ (usage?.totals.total_tokens ?? 0).toLocaleString('id-ID') }}
              </span>
            </p>
          </div>

          <p v-if="(usage?.totals.unknown_cost_turns ?? 0) > 0" class="mt-2 flex items-start gap-1.5 text-[11px] leading-snug text-muted">
            <PhWarningCircle :size="13" class="mt-0.5 shrink-0" />
            <span>
              harga model ini belum ada untuk {{ usage?.totals.unknown_cost_turns }} turn —
              token tetap dihitung, biayanya tidak.
            </span>
          </p>

          <p v-if="(usage?.budget.enabled ?? false)" class="mt-2 rounded-lg bg-bg-deep/70 px-2 py-1.5 text-[11px] leading-snug text-muted" data-testid="usage-budget">
            Batas harian {{ formatUSD(usage?.budget.usd ?? 0) }} · terpakai
            {{ formatUSD(usage?.budget.spent_usd ?? 0) }}
          </p>

          <p v-if="priceNote" class="mt-2 text-[11px] leading-snug text-quiet">{{ priceNote }}</p>

          <div v-if="topRooms.length > 0" class="mt-3 border-t border-line pt-2.5">
            <p class="mb-1.5 text-[11px] font-medium uppercase tracking-wide text-quiet">Room paling mahal</p>
            <ul class="space-y-1" data-testid="usage-rooms">
              <li v-for="room in topRooms" :key="room.room_id" class="flex items-baseline gap-2 text-[11.5px]">
                <span class="min-w-0 flex-1 truncate text-muted">{{ room.room_name }}</span>
                <span class="shrink-0 text-quiet">{{ room.turns }} turn</span>
                <span class="shrink-0 tabular-nums text-ink">{{ formatUSD(room.cost_usd) }}</span>
                <span v-if="room.unknown_cost_turns > 0" class="shrink-0 text-[10px] text-quiet">
                  +{{ room.unknown_cost_turns }} tanpa harga
                </span>
              </li>
            </ul>
          </div>

          <p class="mt-2.5 text-[10.5px] leading-snug text-quiet">
            Perkiraan dari token yang dilaporkan Hermes × harga di prices.json; bukan tagihan.
          </p>
        </template>
      </template>
    </div>
  </section>
</template>
