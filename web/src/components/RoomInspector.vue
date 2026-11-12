<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { PhBell, PhBellSlash, PhTrash, PhX } from '@phosphor-icons/vue'
import { api } from '../lib/api'
import { disablePush, enablePush, pushIsOn, pushSupported } from '../lib/push'
import type { Reminder } from '../lib/types'
import UsagePanel from './UsagePanel.vue'

const props = defineProps<{ open: boolean; roomId: string }>()
const emit = defineEmits<{ close: [] }>()

type PushState = 'checking' | 'on' | 'off' | 'unsupported' | 'denied' | 'error'

const reminders = ref<Reminder[]>([])
const loading = ref(false)
const loaded = ref(false)
const error = ref('')
const deletingId = ref('')
const pushState = ref<PushState>('checking')
const pushBusy = ref(false)
const pushError = ref('')

const pushLabel = computed(() => {
  switch (pushState.value) {
    case 'on':
      return 'Notifikasi aktif di perangkat ini'
    case 'off':
      return 'Notifikasi belum aktif di perangkat ini'
    case 'unsupported':
      return 'Peramban ini tidak mendukung notifikasi'
    case 'denied':
      return 'Izin notifikasi ditolak. Ubah di pengaturan peramban.'
    case 'error':
      return 'Notifikasi belum bisa disiapkan. Coba lagi.'
    default:
      return 'Memeriksa notifikasi…'
  }
})

const canTogglePush = computed(
  () =>
    !pushBusy.value &&
    pushState.value !== 'unsupported' &&
    pushState.value !== 'checking' &&
    pushState.value !== 'denied',
)

function restoreScroll(): void {
  document.body.style.overflow = ''
}

watch(
  () => props.open,
  async (open) => {
    if (!open) {
      restoreScroll()
      return
    }
    document.body.style.overflow = 'hidden'
    await load()
    await checkPush()
  },
)

watch(
  () => props.roomId,
  () => {
    loaded.value = false
    reminders.value = []
    pushState.value = 'checking'
  },
)

onBeforeUnmount(restoreScroll)

async function load(): Promise<void> {
  if (!props.roomId) return
  loading.value = true
  error.value = ''
  try {
    reminders.value = await api.listReminders(props.roomId)
    loaded.value = true
  } catch {
    error.value = 'Iris tidak bisa memuat pengingat room ini. Coba lagi.'
  } finally {
    loading.value = false
  }
}

async function checkPush(): Promise<void> {
  if (!pushSupported()) {
    pushState.value = 'unsupported'
    return
  }
  pushState.value = (await pushIsOn()) ? 'on' : 'off'
}

async function togglePush(): Promise<void> {
  if (!canTogglePush.value) return
  pushBusy.value = true
  pushError.value = ''
  try {
    if (pushState.value === 'on') {
      await disablePush()
      pushState.value = 'off'
      return
    }
    const result = await enablePush(props.roomId)
    pushState.value =
      result === 'subscribed' ? 'on' : result === 'unsupported' ? 'unsupported' : result
  } catch {
    pushState.value = 'error'
    pushError.value = 'Notifikasi belum bisa diaktifkan. Coba lagi.'
  } finally {
    pushBusy.value = false
  }
}

async function cancel(reminder: Reminder): Promise<void> {
  if (deletingId.value !== '') return
  deletingId.value = reminder.id
  error.value = ''
  try {
    await api.deleteReminder(props.roomId, reminder.id)
    reminders.value = reminders.value.filter((row) => row.id !== reminder.id)
  } catch {
    error.value = 'Pengingat ini belum bisa dibatalkan. Coba lagi.'
  } finally {
    deletingId.value = ''
  }
}

function dueLabel(iso: string): string {
  const due = new Date(iso)
  if (Number.isNaN(due.getTime())) return iso
  return new Intl.DateTimeFormat('id-ID', {
    weekday: 'short',
    day: 'numeric',
    month: 'short',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(due)
}
</script>

<template>
  <Teleport to="body">
    <Transition
      enter-active-class="transition duration-150 ease-out"
      enter-from-class="opacity-0"
      leave-active-class="transition duration-100 ease-in"
      leave-to-class="opacity-0"
    >
      <div v-if="open" class="fixed inset-0 z-50 lg:bg-transparent">
        <div class="absolute inset-0 bg-bg-deep/75 backdrop-blur-sm" @click="emit('close')" />
        <section
          class="absolute inset-x-0 bottom-0 flex max-h-[86dvh] flex-col rounded-t-2xl border-t border-line bg-bg shadow-2xl lg:inset-y-0 lg:left-auto lg:right-0 lg:max-h-none lg:w-[400px] lg:rounded-none lg:border-l lg:border-t-0"
          role="dialog"
          aria-modal="true"
          aria-labelledby="room-inspector-title"
          data-testid="room-inspector"
        >
          <div class="flex items-center justify-between gap-3 border-b border-line px-4 pb-3 pt-3">
            <div class="min-w-0">
              <h2 id="room-inspector-title" class="text-[15px] font-semibold tracking-tight text-ink">
                Biaya, pengingat &amp; notifikasi
              </h2>
              <p class="mt-0.5 text-[11.5px] text-quiet">
                Biaya dari token Hermes; pengingat dijalankan Iris.
              </p>
            </div>
            <button
              type="button"
              class="tap flex items-center justify-center rounded-lg text-quiet hover:text-ink"
              aria-label="Tutup biaya, pengingat dan notifikasi"
              data-testid="close-inspector"
              @click="emit('close')"
            >
              <PhX :size="18" />
            </button>
          </div>

          <div class="min-h-0 flex-1 overflow-y-auto overflow-x-hidden px-3 py-3">
            <UsagePanel :room-id="props.roomId" :open="props.open" />

            <hr class="my-4 border-line" />

            <section aria-labelledby="reminder-heading">
              <div class="flex items-center justify-between px-2 pb-2">
                <h3 id="reminder-heading" class="text-[12px] font-semibold uppercase tracking-wide text-muted">
                  Pengingat
                </h3>
                <span class="text-[11px] text-quiet">{{ reminders.length }} menunggu</span>
              </div>

              <p v-if="loading && !loaded" class="px-2 py-5 text-center text-[13px] text-quiet">
                Memuat pengingat…
              </p>

              <div v-else-if="error && reminders.length === 0" class="mx-1 rounded-xl border border-line px-3 py-3">
                <p class="text-[13px] text-muted">{{ error }}</p>
                <button type="button" class="mt-2 text-[13px] font-medium text-orchid" @click="load">
                  Coba lagi
                </button>
              </div>

              <div
                v-else-if="loaded && reminders.length === 0"
                class="mx-1 flex flex-col items-center rounded-xl border border-dashed border-line px-4 py-8 text-center"
                data-testid="reminder-empty"
              >
                <p class="text-[13.5px] font-medium text-ink">Belum ada pengingat</p>
                <p class="mt-1.5 max-w-[17rem] text-[12.5px] leading-relaxed text-quiet">
                  Ketik di composer, misalnya
                  <span class="text-ink">“ingatkan saya jam 3 untuk rapat”</span>. Pengingat muncul di
                  sini dan bisa dibatalkan.
                </p>
              </div>

              <ul v-else class="space-y-1.5" data-testid="reminder-list">
                <li
                  v-for="reminder in reminders"
                  :key="reminder.id"
                  class="rounded-xl border border-line bg-surface/60 px-3 py-2.5"
                  data-testid="reminder-row"
                >
                  <div class="flex min-w-0 items-start gap-3">
                    <span class="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-bg-deep text-orchid">
                      <PhBell :size="16" />
                    </span>
                    <div class="min-w-0 flex-1">
                      <p class="text-[13px] leading-snug text-ink">{{ reminder.text }}</p>
                      <p class="mt-1 text-[11.5px] leading-tight text-quiet">
                        {{ dueLabel(reminder.due_at) }}
                      </p>
                    </div>
                    <button
                      type="button"
                      class="tap flex shrink-0 items-center justify-center rounded-lg text-quiet hover:text-ink disabled:opacity-60"
                      :aria-label="`Batalkan pengingat ${reminder.text}`"
                      :disabled="deletingId === reminder.id"
                      data-testid="reminder-cancel"
                      @click="cancel(reminder)"
                    >
                      <PhTrash :size="16" />
                    </button>
                  </div>
                </li>
              </ul>

              <p v-if="error && reminders.length > 0" class="mt-2 px-2 text-[12.5px] text-muted" role="alert">
                {{ error }}
              </p>
            </section>

            <hr class="my-4 border-line" />

            <section aria-labelledby="push-heading">
              <h3 id="push-heading" class="px-2 pb-2 text-[12px] font-semibold uppercase tracking-wide text-muted">
                Notifikasi
              </h3>
              <div class="mx-1 rounded-xl border border-line bg-surface/60 px-3 py-3">
                <div class="flex items-start gap-3">
                  <span class="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-bg-deep text-muted">
                    <PhBell v-if="pushState === 'on'" :size="16" />
                    <PhBellSlash v-else :size="16" />
                  </span>
                  <div class="min-w-0 flex-1">
                    <p class="text-[13px] leading-snug text-ink" data-testid="push-state">
                      {{ pushLabel }}
                    </p>
                    <p class="mt-1.5 text-[11.5px] leading-relaxed text-quiet">
                      Iris memberi tahu saat balasan selesai dan kamu tidak sedang membuka room,
                      saat ada izin yang menunggu, dan saat pengingat berbunyi.
                    </p>
                    <button
                      type="button"
                      class="tap mt-2.5 inline-flex items-center gap-1.5 rounded-lg border border-line px-3 text-[12.5px] text-ink hover:border-orchid/50 disabled:opacity-60"
                      :disabled="!canTogglePush"
                      data-testid="push-toggle"
                      @click="togglePush"
                    >
                      {{
                        pushBusy
                          ? 'Menyiapkan…'
                          : pushState === 'on'
                            ? 'Matikan notifikasi'
                            : 'Aktifkan notifikasi'
                      }}
                    </button>
                    <p v-if="pushError" class="mt-2 text-[11.5px] text-muted" role="alert">
                      {{ pushError }}
                    </p>
                  </div>
                </div>
                <p class="mt-3 border-t border-line pt-2.5 text-[11px] leading-relaxed text-quiet">
                  Di iPhone, pasang Iris ke Layar Utama dulu — tab Safari biasa tidak bisa menerima
                  notifikasi. Di Android, pasang Iris sebagai aplikasi, bukan tab biasa.
                </p>
              </div>
            </section>
          </div>
        </section>
      </div>
    </Transition>
  </Teleport>
</template>
