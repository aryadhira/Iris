<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { PhArrowLeft, PhList } from '@phosphor-icons/vue'
import BrandMark from '../components/BrandMark.vue'
import Composer from '../components/Composer.vue'
import FormattedText from '../components/FormattedText.vue'
import MessageBubble from '../components/MessageBubble.vue'
import WorkingLine from '../components/WorkingLine.vue'
import { ApiError, api } from '../lib/api'
import { humanizeServerError } from '../lib/errors'
import type { Room, TurnPhase } from '../lib/types'
import { createRoomStore, type RoomStore } from '../stores/room'
import { markSignedOut } from '../stores/auth'
import { forgetRoom, rememberRoom } from '../stores/rooms'
import { openRoomSheet } from '../stores/ui'

const route = useRoute()
const router = useRouter()

const room = ref<Room | null>(null)
const store = ref<RoomStore | null>(null)
const notFound = ref(false)
const openError = ref('')

const scroller = ref<HTMLDivElement | null>(null)
const nearBottom = ref(true)

const messages = computed(() => store.value?.state.messages ?? [])
const phase = computed<TurnPhase>(() => store.value?.state.phase ?? 'idle')
const streamingText = computed(() => store.value?.state.streamingText ?? '')
const currentTool = computed(() => store.value?.state.currentTool ?? '')
const turnStartedAt = computed(() => store.value?.state.turnStartedAt ?? 0)
const errorText = computed(() => store.value?.state.errorText ?? '')
const loadingMessages = computed(() => store.value?.state.loading ?? false)
const loaded = computed(() => store.value?.state.loaded ?? false)
const loadError = computed(() => store.value?.state.loadError ?? '')
const canLoadOlder = computed(() => store.value?.state.canLoadOlder ?? false)
const loadingOlder = computed(() => store.value?.state.loadingOlder ?? false)
const archived = computed(() => store.value?.state.archived ?? false)
const isWorking = computed(() => phase.value === 'streaming' || phase.value === 'reconnecting')
const draft = computed({
  get: () => store.value?.state.draft ?? '',
  set: (value: string) => {
    if (store.value) store.value.state.draft = value
  },
})
const hasDraft = computed(() => draft.value.trim() !== '')

function onScroll(): void {
  const element = scroller.value
  if (!element) return
  nearBottom.value = element.scrollHeight - element.scrollTop - element.clientHeight < 140
}

function scrollToBottom(smooth = false): void {
  const element = scroller.value
  if (!element) return
  element.scrollTo({ top: element.scrollHeight, behavior: smooth ? 'smooth' : 'auto' })
}

async function openRoom(id: string): Promise<void> {
  store.value?.dispose()
  store.value = null
  room.value = null
  notFound.value = false
  openError.value = ''

  try {
    const found = await api.getRoom(id)
    room.value = found
    rememberRoom(found.id)
    const created = createRoomStore(found.id, {}, { archived: Boolean(found.archived_at) })
    store.value = created
    await created.load()
    await nextTick()
    scrollToBottom()
    document.title = `${found.name} — Iris`
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) {
      notFound.value = true
      forgetRoom(id)
      return
    }
    if (error instanceof ApiError && error.status === 401) {
      markSignedOut()
      await router.replace({ name: 'login', query: { next: route.fullPath } })
      return
    }
    openError.value =
      error instanceof ApiError
        ? humanizeServerError(error.message)
        : 'Iris tidak bisa membuka room ini. Periksa koneksi lalu coba lagi.'
  }
}

function send(): void {
  const current = store.value
  if (!current) return
  nearBottom.value = true
  void current.send(current.state.draft)
}

function retry(): void {
  const current = store.value
  if (!current) return
  const text = current.state.draft
  if (text.trim() === '') return
  nearBottom.value = true
  void current.send(text)
}

function reloadRoom(): void {
  void store.value?.reload()
}

function loadOlder(): Promise<void> {
  return store.value?.loadOlder() ?? Promise.resolve()
}

watch(
  () => route.params.id,
  (id) => {
    if (typeof id === 'string' && id !== '') void openRoom(id)
  },
  { immediate: true },
)

watch(
  () => [messages.value.length, streamingText.value.length],
  async () => {
    if (!nearBottom.value) return
    await nextTick()
    scrollToBottom()
  },
)

watch(phase, async (value) => {
  if (value === 'streaming') {
    await nextTick()
    scrollToBottom()
  }
})

onBeforeUnmount(() => {
  store.value?.dispose()
})
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <header
      class="flex items-center gap-2 border-b border-line px-3 pb-2 pt-[max(0.5rem,env(safe-area-inset-top))] lg:px-6 lg:py-3.5"
      data-testid="room-header"
    >
      <button
        type="button"
        class="tap -ml-1 flex items-center justify-center rounded-lg text-muted hover:text-ink lg:hidden"
        aria-label="Buka daftar room"
        data-testid="room-menu"
        @click="openRoomSheet"
      >
        <PhList :size="20" />
      </button>

      <div class="mx-auto flex min-w-0 w-full max-w-3xl items-center gap-2 px-1 lg:px-8">
        <div class="flex min-w-0 items-center gap-2">
          <h1 class="truncate text-[15px] font-semibold tracking-tight text-ink lg:text-[17px]">
            {{ room?.name ?? (notFound ? 'Room tidak ada' : 'Memuat room…') }}
          </h1>
          <span
            v-if="archived"
            class="shrink-0 rounded-full border border-line px-2 py-0.5 text-[10.5px] leading-tight text-quiet"
          >
            Diarsipkan
          </span>
        </div>
        <p class="truncate text-[11.5px] text-quiet">
          {{ room?.topic || (room ? 'Tanpa topik' : '') }}
        </p>
      </div>
    </header>

    <div
      ref="scroller"
      class="min-h-0 flex-1 overflow-y-auto overflow-x-hidden"
      data-testid="message-scroller"
      @scroll.passive="onScroll"
    >
      <div class="mx-auto flex min-h-full w-full max-w-3xl flex-col px-4 pb-6 pt-5 lg:px-8 lg:pt-7">
        <!-- Not found -->
        <div v-if="notFound" class="my-auto flex flex-col items-center py-12 text-center">
          <BrandMark :size="48" />
          <h2 class="mt-4 text-[16px] font-semibold text-ink">Room tidak ditemukan</h2>
          <p class="mt-1.5 max-w-xs text-[13px] leading-relaxed text-quiet">
            Mungkin tautannya salah, atau room ini sudah tidak ada.
          </p>
          <RouterLink
            to="/rooms"
            class="tap mt-5 inline-flex items-center gap-2 rounded-xl border border-line px-3 text-[13px] text-ink hover:border-orchid/50"
          >
            <PhArrowLeft :size="15" />
            Kembali ke rooms
          </RouterLink>
        </div>

        <!-- The room itself failed to open -->
        <div v-else-if="openError" class="my-auto rounded-2xl border border-line bg-surface/60 p-5">
          <p class="text-[14px] text-muted">{{ openError }}</p>
          <div class="mt-3 flex items-center gap-3">
            <button
              type="button"
              class="tap rounded-xl border border-line px-3 text-[13px] text-ink hover:border-orchid/50"
              @click="route.params.id && openRoom(String(route.params.id))"
            >
              Coba lagi
            </button>
            <RouterLink to="/rooms" class="text-[13px] text-quiet hover:text-muted">
              Daftar room
            </RouterLink>
          </div>
        </div>

        <!-- Messages -->
        <div v-else class="flex flex-1 flex-col">
          <p v-if="loadingMessages && !loaded" class="my-auto py-6 text-center text-[13px] text-quiet">
            Memuat pesan…
          </p>

          <div v-else-if="loadError && messages.length === 0" class="my-auto rounded-2xl border border-line bg-surface/60 p-4">
            <p class="text-[13px] text-muted">{{ loadError }}</p>
            <button
              type="button"
              class="tap mt-2 rounded-xl border border-line px-3 text-[13px] text-ink hover:border-orchid/50"
              @click="reloadRoom"
            >
              Coba lagi
            </button>
          </div>

          <div
            v-else-if="loaded && messages.length === 0 && !isWorking"
            class="my-auto flex flex-col items-center py-10 text-center lg:py-16"
            data-testid="empty-room"
          >
            <BrandMark :size="52" variant="full" />
            <h2 class="mt-4 text-[16px] font-semibold tracking-tight text-ink">Room ini masih kosong</h2>
            <p class="mt-1.5 max-w-[17rem] text-[13px] leading-relaxed text-quiet">
              Tulis pesan pertama di bawah. Hermes akan menjawab di room ini dan tetap di sini.
            </p>
          </div>

          <div v-else class="mt-auto space-y-5">
            <button
              v-if="canLoadOlder"
              type="button"
              class="mx-auto block rounded-lg px-3 py-2 text-[12px] text-quiet hover:text-muted disabled:opacity-60"
              :disabled="loadingOlder"
              @click="loadOlder"
            >
              {{ loadingOlder ? 'Memuat pesan lama…' : 'Muat pesan lama' }}
            </button>

            <ol class="space-y-5" data-testid="message-list">
              <li v-for="message in messages" :key="message.id">
                <MessageBubble :message="message" />
              </li>
            </ol>

            <!-- Streaming reply grows in place; the working line never hides. -->
            <div v-if="isWorking" class="flex gap-3" data-testid="streaming-turn">
              <BrandMark :size="26" class="mt-0.5" />
              <div class="min-w-0 flex-1">
                <p v-if="streamingText" class="mb-1 text-[11px] text-quiet">Iris · menulis…</p>
                <div v-if="streamingText" class="text-[14px] leading-relaxed text-ink" data-testid="streaming-text">
                  <FormattedText :text="streamingText" />
                  <span
                    class="ml-0.5 inline-block h-[1.05em] w-[2px] translate-y-[0.15em] animate-pulse bg-orchid"
                    aria-hidden="true"
                  />
                </div>
                <WorkingLine
                  class="mt-2"
                  :started-at="turnStartedAt"
                  :tool="currentTool"
                  :reconnecting="phase === 'reconnecting'"
                />
              </div>
            </div>

            <p v-else-if="loaded && messages.length > 0" class="pt-3 text-center text-[11.5px] text-quiet">
              Hermes menjawab di sini. Riwayat tetap tersimpan meski kamu menutup room.
            </p>
          </div>
        </div>
      </div>
    </div>

    <!-- Error, with the typed message still in the composer for a retry. -->
    <div v-if="store && errorText" class="border-t border-line bg-bg px-3 pt-3 lg:px-8">
      <div
        class="mx-auto w-full max-w-3xl rounded-xl border border-line bg-surface px-3.5 py-2.5"
        role="alert"
        data-testid="turn-error"
      >
        <p class="text-[13px] leading-snug text-muted">{{ errorText }}</p>
        <div class="mt-1.5 flex items-center gap-3">
          <button
            v-if="hasDraft && !isWorking"
            type="button"
            class="text-[12.5px] font-medium text-orchid hover:text-orchid/80"
            @click="retry"
          >
            Coba lagi
          </button>
          <button
            type="button"
            class="text-[12.5px] text-quiet hover:text-muted"
            @click="reloadRoom"
          >
            Muat ulang room
          </button>
        </div>
      </div>
    </div>

    <Composer
      v-if="store"
      v-model="draft"
      :running="isWorking"
      :archived="archived"
      @send="send"
    />

    <div v-else-if="room" class="border-t border-line px-4 py-3">
      <p class="text-center text-[12px] text-quiet">Menyiapkan composer…</p>
    </div>
  </div>
</template>
