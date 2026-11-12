<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { PhCheckCircle, PhProhibit, PhShieldWarning } from '@phosphor-icons/vue'
import { humanToolName } from '../lib/format'
import { approvalCountdown, approvalSeconds } from '../lib/timeline'
import type { Approval } from '../lib/types'

const props = defineProps<{
  approval: Approval
  /** The choice currently in flight, if this card is being answered. */
  resolvingChoice?: string
  disabled?: boolean
}>()

const emit = defineEmits<{ resolve: [choice: string] }>()

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

const pending = computed(() => props.approval.state === 'pending')
const expired = computed(
  () => pending.value && approvalSeconds(props.approval.expires_at, now.value) <= 0,
)
const countdown = computed(() => approvalCountdown(props.approval.expires_at, now.value))
const busy = computed(() => (props.resolvingChoice ?? '') !== '')
const locked = computed(() => !pending.value || expired.value || busy.value || Boolean(props.disabled))

const toolLabel = computed(() =>
  props.approval.tool_name ? humanToolName(props.approval.tool_name) : '',
)

const receipt = computed(() => {
  const decision = props.approval.choice ?? ''
  switch (props.approval.state) {
    case 'allowed':
      if (decision === 'once') return 'Diizinkan sekali oleh Boss'
      if (decision === 'session') return 'Diizinkan untuk sesi ini oleh Boss'
      if (decision === 'always') return 'Selalu diizinkan oleh Boss'
      return 'Diizinkan oleh Boss'
    case 'denied':
      return 'Ditolak oleh Boss'
    case 'expired':
      return 'Kedaluwarsa — Hermes menolak sendiri, tidak pernah dijalankan'
    case 'superseded':
      return 'Tidak berlaku lagi — Hermes sudah menutup izin ini'
    default:
      return ''
  }
})

const receiptTime = computed(() => {
  const raw = props.approval.resolved_at ?? props.approval.created_at
  const date = new Date(raw)
  if (Number.isNaN(date.getTime())) return ''
  return new Intl.DateTimeFormat('id-ID', {
    hour: '2-digit',
    minute: '2-digit',
  }).format(date)
})

function offered(choice: string): boolean {
  return props.approval.choices.includes(choice)
}

function resolve(choice: string): void {
  if (locked.value || !offered(choice)) return
  emit('resolve', choice)
}

/**
 * Enter answers the primary choice and Escape refuses — but only while focus
 * is inside this card. There is deliberately no global shortcut: a stray key
 * must never deny work in another room.
 */
function onKeydown(event: KeyboardEvent): void {
  if (!pending.value || expired.value || busy.value) return
  const target = event.target as HTMLElement | null
  if (event.key === 'Enter') {
    if (target?.tagName === 'BUTTON') return
    event.preventDefault()
    resolve('once')
    return
  }
  if (event.key === 'Escape') {
    if (!offered('deny')) return
    event.preventDefault()
    resolve('deny')
  }
}
</script>

<template>
  <div
    class="rounded-2xl border p-3.5 sm:p-4"
    :class="
      pending && !expired
        ? 'border-orchid/45 bg-surface'
        : 'border-line bg-surface/50'
    "
    data-testid="approval-card"
    :data-state="expired ? 'expired' : approval.state"
    :tabindex="pending && !expired ? 0 : undefined"
    :aria-label="pending && !expired ? 'Izin dari Hermes. Tekan Enter untuk mengizinkan sekali, Escape untuk menolak.' : undefined"
    @keydown="onKeydown"
  >
    <div class="flex items-start gap-2.5">
      <PhShieldWarning
        :size="20"
        class="mt-0.5 shrink-0"
        :class="pending && !expired ? 'text-orchid' : 'text-quiet'"
        aria-hidden="true"
      />
      <div class="min-w-0 flex-1">
        <p class="text-[13.5px] font-medium leading-snug text-ink">
          {{ pending && !expired ? 'Hermes minta izin' : receipt }}
        </p>
        <p
          v-if="pending && !expired"
          class="mt-0.5 text-[12px] leading-relaxed text-muted"
        >
          <span v-if="toolLabel">Lewat {{ toolLabel.toLowerCase() }}. </span>
          {{ approval.description || 'Perintah ini ditandai berisiko dan belum dijalankan.' }}
        </p>
        <p v-else-if="receiptTime" class="mt-0.5 text-[11.5px] text-quiet">
          {{ receiptTime }}
        </p>
      </div>
      <span
        v-if="pending && !expired"
        class="shrink-0 rounded-full border border-orchid/40 px-2 py-0.5 font-mono text-[10.5px] tabular-nums text-orchid"
        data-testid="approval-countdown"
      >
        {{ countdown }}
      </span>
    </div>

    <pre
      class="mt-2.5 max-h-40 overflow-x-auto rounded-xl border border-line bg-bg-deep px-3 py-2 font-mono text-[12px] leading-relaxed"
      :class="pending && !expired ? 'text-ink' : 'text-muted'"
      data-testid="approval-command"
    ><code>{{ approval.command }}</code></pre>

    <p
      v-if="expired || approval.state === 'expired'"
      class="mt-2.5 flex items-center gap-1.5 text-[12px] leading-snug text-quiet"
      data-testid="approval-expired"
    >
      <PhProhibit :size="15" class="shrink-0" aria-hidden="true" />
      {{ countdown === 'kedaluwarsa' ? 'Izin ini sudah kedaluwarsa' : countdown }}
    </p>

    <div
      v-if="pending && !expired"
      class="mt-3 flex flex-col gap-2 min-[420px]:flex-row min-[420px]:flex-wrap"
      data-testid="approval-actions"
    >
      <button
        type="button"
        class="tap flex min-h-11 w-full items-center justify-center gap-1.5 rounded-xl bg-violet px-4 text-[13px] font-medium text-ink transition-colors hover:bg-violet/90 disabled:cursor-not-allowed disabled:opacity-60 min-[420px]:w-auto"
        :disabled="locked"
        data-testid="approval-primary"
        @click="resolve('once')"
      >
        <PhCheckCircle :size="16" weight="fill" />
        {{ resolvingChoice === 'once' ? 'Mengizinkan…' : 'Izinkan sekali' }}
      </button>
      <button
        v-if="offered('session')"
        type="button"
        class="tap flex min-h-11 w-full items-center justify-center rounded-xl border border-line px-4 text-[13px] text-ink transition-colors hover:border-orchid/50 disabled:cursor-not-allowed disabled:opacity-60 min-[420px]:w-auto"
        :disabled="locked"
        data-testid="approval-session"
        @click="resolve('session')"
      >
        {{ resolvingChoice === 'session' ? 'Mengizinkan sesi…' : 'Sesi ini' }}
      </button>
      <button
        v-if="offered('always')"
        type="button"
        class="tap flex min-h-11 w-full items-center justify-center rounded-xl border border-line px-4 text-[13px] text-ink transition-colors hover:border-orchid/50 disabled:cursor-not-allowed disabled:opacity-60 min-[420px]:w-auto"
        :disabled="locked"
        data-testid="approval-always"
        @click="resolve('always')"
      >
        {{ resolvingChoice === 'always' ? 'Mengizinkan selalu…' : 'Selalu izinkan' }}
      </button>
      <button
        v-if="offered('deny')"
        type="button"
        class="tap flex min-h-11 w-full items-center justify-center gap-1.5 rounded-xl border border-red-400/40 px-4 text-[13px] text-red-300 transition-colors hover:bg-red-400/10 disabled:cursor-not-allowed disabled:opacity-60 min-[420px]:ml-auto min-[420px]:w-auto"
        :disabled="locked"
        data-testid="approval-deny"
        @click="resolve('deny')"
      >
        <PhProhibit :size="16" />
        {{ resolvingChoice === 'deny' ? 'Menolak…' : 'Tolak' }}
      </button>
    </div>
  </div>
</template>
