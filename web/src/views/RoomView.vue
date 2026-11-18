<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  PhArrowLeft,
  PhBell,
  PhDotsThreeVertical,
  PhDownloadSimple,
  PhFiles,
  PhList,
  PhSpeakerHigh,
  PhSpeakerSlash,
} from '@phosphor-icons/vue'
import ApprovalCard from '../components/ApprovalCard.vue'
import BrandMark from '../components/BrandMark.vue'
import Composer from '../components/Composer.vue'
import ContinuationLine from '../components/ContinuationLine.vue'
import FileLibrary from '../components/FileLibrary.vue'
import FormattedText from '../components/FormattedText.vue'
import MessageBubble from '../components/MessageBubble.vue'
import RoomInspector from '../components/RoomInspector.vue'
import WorkingLine from '../components/WorkingLine.vue'
import { ApiError, api } from '../lib/api'
import { humanizeServerError } from '../lib/errors'
import { downloadBlob } from '../lib/files'
import { approvalSeconds, needsStopConfirm, roomState } from '../lib/timeline'
import type { Approval, ChatMessage, Room, RoomRun, TurnPhase } from '../lib/types'
import { createAttachmentStore, type AttachmentStore } from '../stores/attachments'
import { createRoomStore, type RoomStore } from '../stores/room'
import { createSpeakerStore, createVoiceStore } from '../stores/voice'
import { markSignedOut } from '../stores/auth'
import { forgetRoom, rememberRoom } from '../stores/rooms'
import { openRoomSheet } from '../stores/ui'

const route = useRoute()
const router = useRouter()

const room = ref<Room | null>(null)
const store = ref<RoomStore | null>(null)
const attachments = ref<AttachmentStore | null>(null)
const libraryOpen = ref(false)
const dragging = ref(false)
const notFound = ref(false)
const openError = ref('')
const menuOpen = ref(false)
const inspectorOpen = ref(false)
const exporting = ref(false)
const exportError = ref('')
const highlightedId = ref('')
let highlightTimer: number | undefined

const speaker = createSpeakerStore()
const voice = createVoiceStore(
  {},
  {
    stopPlayback: () => speaker.stop(),
    onTranscript: (text) => sendTranscript(text),
  },
)
const autoSpeak = ref(false)
const sentNotice = ref('')
let sentToastTimer: number | undefined
let autoSpeakBaseline = new Set<string>()
let readyForAutoSpeak = false

const scroller = ref<HTMLDivElement | null>(null)
// Content grows after paint: images decode and artifact cards mount late. Without this the newest card
// ends up under the composer even though we scrolled on message change. Keep the view pinned only when the
// reader is already at the bottom, so scrolling up is never fought.
let contentObserver: ResizeObserver | null = null
const nearBottom = ref(true)

const messages = computed(() => store.value?.state.messages ?? [])
const phase = computed<TurnPhase>(() => store.value?.state.phase ?? 'idle')
const streamingText = computed(() => store.value?.state.streamingText ?? '')
const currentTool = computed(() => store.value?.state.currentTool ?? '')
const storeRun = computed<RoomRun | null>(() =>
  store.value ? store.value.state.run : (room.value?.run ?? null),
)
const runStatus = computed(() => storeRun.value?.status ?? '')
const runStartedAt = computed(() => {
  const raw = storeRun.value?.started_at ?? ''
  const parsed = Date.parse(raw)
  return Number.isFinite(parsed) ? parsed : 0
})
const runIsLive = computed(() =>
  ['queued', 'running', 'waiting_for_approval', 'stopping'].includes(runStatus.value),
)
const runInterrupted = computed(() => !runIsLive.value && runStatus.value === 'interrupted')
const turnStartedAt = computed(
  () => store.value?.state.turnStartedAt || runStartedAt.value,
)
const clockNow = ref(Date.now())
let clockTimer: number | undefined
const errorText = computed(() => store.value?.state.errorText ?? '')
const loadingMessages = computed(() => store.value?.state.loading ?? false)
const loaded = computed(() => store.value?.state.loaded ?? false)
const loadError = computed(() => store.value?.state.loadError ?? '')
const canLoadOlder = computed(() => store.value?.state.canLoadOlder ?? false)
const loadingOlder = computed(() => store.value?.state.loadingOlder ?? false)
const archived = computed(() => store.value?.state.archived ?? false)
const isWorking = computed(
  () => phase.value === 'streaming' || phase.value === 'reconnecting' || runIsLive.value,
)
const draft = computed({
  get: () => store.value?.state.draft ?? '',
  set: (value: string) => {
    if (store.value) store.value.state.draft = value
  },
})
const hasDraft = computed(() => draft.value.trim() !== '')
const pendingAttachments = computed(() => attachments.value?.state.items ?? [])
const uploadBlock = computed(() => attachments.value?.blockReason ?? '')
const continuation = computed(() => store.value?.state.continuation ?? null)
const continuationError = computed(() => store.value?.state.continuationError ?? '')
const approvals = computed(() => store.value?.state.approvals ?? [])
const pendingApprovals = computed(
  () =>
    approvals.value.filter(
      (row) => row.state === 'pending' && approvalSeconds(row.expires_at, Date.now()) > 0,
    ).length,
)
const approvalError = computed(() => store.value?.state.approvalError ?? '')
const resolvingApproval = computed(() => store.value?.state.resolvingApproval ?? '')
const resolvingChoice = computed(() => store.value?.state.resolvingChoice ?? '')
const stopping = computed(() => store.value?.state.stopping ?? false)
const stopNotice = computed(() => store.value?.state.stopNotice ?? '')
const reminderNotice = computed(() => store.value?.state.reminderNotice ?? '')
const canStop = computed(
  () => pendingApprovals.value === 0 && (isWorking.value || Boolean(continuation.value)),
)
const stopArmed = ref(false)
const lastAssistant = computed(() => {
  for (let i = messages.value.length - 1; i >= 0; i -= 1) {
    if (messages.value[i].role === 'assistant') return messages.value[i]
  }
  return undefined
})
const lastUserMessage = computed(() => {
  for (let i = messages.value.length - 1; i >= 0; i -= 1) {
    if (messages.value[i].role === 'user') return messages.value[i]
  }
  return undefined
})
const assistantAfterLastUser = computed(() => {
  const user = lastUserMessage.value
  if (!user) return undefined
  const index = messages.value.findIndex((message) => message.id === user.id)
  return messages.value.slice(index + 1).find((message) => message.role === 'assistant')
})
/** A failed turn that left no assistant bubble still needs a visible retry. */
const showEmptyRetry = computed(
  () =>
    phase.value === 'error' &&
    !isWorking.value &&
    Boolean(lastUserMessage.value) &&
    !assistantAfterLastUser.value,
)

interface TimelineItem {
  key: string
  createdAt: string
  message?: ChatMessage
  approval?: Approval
}

/** Messages and approval receipts share one chronological transcript. */
const timelineItems = computed<TimelineItem[]>(() => {
  const items: TimelineItem[] = []
  for (const message of messages.value) {
    items.push({ key: `m-${message.id}`, createdAt: message.createdAt, message })
  }
  for (const approval of approvals.value) {
    items.push({ key: `a-${approval.id}`, createdAt: approval.created_at, approval })
  }
  return items.sort((a, b) => {
    const left = Date.parse(a.createdAt)
    const right = Date.parse(b.createdAt)
    if (Number.isFinite(left) && Number.isFinite(right) && left !== right) return left - right
    return a.key < b.key ? -1 : 1
  })
})
const stateChip = computed(() =>
  runInterrupted.value
    ? 'interrupted'
    : roomState(
        isWorking.value,
        Boolean(continuation.value),
        lastAssistant.value?.meta?.statusLine,
        pendingApprovals.value,
      ),
)
const stateChipLabel = computed(() => {
  if (stateChip.value === 'working') {
    return `bekerja · ${runningClock(turnStartedAt.value, clockNow.value)}`
  }
  return {
    idle: 'idle',
    working: 'bekerja',
    waiting: 'menunggu proses',
    'needs-you': 'butuh kamu',
    'needs-approval': `butuh izin · ${pendingApprovals.value}`,
    interrupted: 'terputus',
  }[stateChip.value]
})
const stateChipClass = computed(() => {
  switch (stateChip.value) {
    case 'working':
      return 'border-orchid/40 text-orchid'
    case 'waiting':
      return 'border-line bg-surface text-muted'
    case 'needs-you':
      return 'border-violet/60 bg-violet/20 text-ink'
    case 'needs-approval':
      return 'border-orchid/60 bg-orchid/15 text-orchid'
    case 'interrupted':
      return 'border-line text-muted'
    default:
      return 'border-line text-quiet'
  }
})

let continuationTimer: number | undefined
let approvalTimer: number | undefined
let reminderToastTimer: number | undefined

function autoSpeakStorageKey(id: string): string {
  return `iris.autospeak.${id}`
}

function loadAutoSpeak(id: string): void {
  try {
    autoSpeak.value = window.localStorage.getItem(autoSpeakStorageKey(id)) === '1'
  } catch {
    autoSpeak.value = false
  }
}

function toggleAutoSpeak(): void {
  autoSpeak.value = !autoSpeak.value
  if (room.value) {
    try {
      window.localStorage.setItem(
        autoSpeakStorageKey(room.value.id),
        autoSpeak.value ? '1' : '0',
      )
    } catch {
      // private mode; the switch still works for this tab
    }
  }
  if (autoSpeak.value) {
    const newest = [...messages.value].reverse().find((message) => message.role === 'assistant')
    if (newest) autoSpeakBaseline.add(newest.id)
  } else {
    speaker.stop()
  }
}

/**
 * A transcript goes straight to Hermes: no editable draft, no extra tap. If
 * the room cannot accept it, the words stay in the composer and the existing
 * error path explains why. A misheard name is fixed with "Coba lagi", which
 * reuses the user row instead of posting a second message.
 */
function sendTranscript(text: string): void {
  const current = store.value
  if (!current) return
  const body = text.trim()
  if (body === '') return
  nearBottom.value = true
  if (current.state.archived || isWorking.value) {
    current.state.draft = body
    current.state.errorText = 'Room ini sedang bekerja. Pesan suaramu ada di kotak, kirim lagi setelah selesai.'
    return
  }
  const ready = attachments.value?.ready ?? []
  void current.send(body, ready, () => {
    attachments.value?.clearReady()
    showSentToast(body)
  })
}

function runningClock(startedAt: number, now: number): string {
  if (!startedAt || startedAt <= 0) return '0:00'
  const seconds = Math.max(0, Math.floor((now - startedAt) / 1000))
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  const rest = seconds % 60
  const clock = `${minutes}:${String(rest).padStart(2, '0')}`
  return hours > 0 ? `${hours}:${clock}` : clock
}

function showSentToast(text: string): void {
  if (sentToastTimer !== undefined) window.clearTimeout(sentToastTimer)
  sentNotice.value = `terkirim: ${text}`
  sentToastTimer = window.setTimeout(() => {
    sentNotice.value = ''
    sentToastTimer = undefined
  }, 6000)
}

function speakMessage(message: ChatMessage): void {
  void speaker.toggle(message.id, message.body)
}

function dismissVoiceError(): void {
  voice.state.errorText = ''
}

async function stopContinuation(): Promise<void> {
  await store.value?.stopContinuation()
}

function resolveApproval(id: string, choice: string): void {
  void store.value?.resolveApproval(id, choice)
}

function requestStop(): void {
  if (!canStop.value || stopping.value) return
  if (needsStopConfirm(turnStartedAt.value, Date.now()) && !stopArmed.value) {
    stopArmed.value = true
    return
  }
  void stopNow()
}

async function stopNow(): Promise<void> {
  stopArmed.value = false
  await store.value?.stopRun()
}

function cancelStop(): void {
  stopArmed.value = false
}

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
  voice.cancel()
  speaker.stop()
  readyForAutoSpeak = false
  store.value?.dispose()
  store.value = null
  attachments.value?.clear()
  attachments.value = null
  libraryOpen.value = false
  inspectorOpen.value = false
  room.value = null
  notFound.value = false
  openError.value = ''

  try {
    const found = await api.getRoom(id)
    room.value = found
    rememberRoom(found.id)
    attachments.value = createAttachmentStore({
      upload: (file, onProgress) => api.uploadFile(found.id, file, onProgress),
      removeRemote: (id) => api.deleteFile(id),
    })
    const created = createRoomStore(found.id, {}, { archived: Boolean(found.archived_at), run: found.run ?? null })
    store.value = created
    await created.load()
    created.startLive()
    loadAutoSpeak(found.id)
    autoSpeakBaseline = new Set(
      created.state.messages
        .filter((message) => message.role === 'assistant')
        .map((message) => message.id),
    )
    readyForAutoSpeak = true
    const deepLink = typeof route.query.m === 'string' ? route.query.m : ''
    if (deepLink !== '') {
      // A search result opens the room at that message, not at the bottom.
      nearBottom.value = false
      await scrollToMessage(deepLink)
    } else {
      await nextTick()
      scrollToBottom()
    }
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
        : `Iris tidak bisa menghubungi ${location.host}. Periksa koneksi, atau buka alamat terbaru aplikasi ini.`
  }
}

async function scrollToMessage(id: string): Promise<void> {
  await nextTick()
  if (!/^[0-9a-fA-F-]{36}$/.test(id)) return
  const element = scroller.value?.querySelector(`[data-message-id="${id}"]`)
  if (!(element instanceof HTMLElement)) return
  element.scrollIntoView({ block: 'center' })
  highlightedId.value = id
  if (highlightTimer !== undefined) window.clearTimeout(highlightTimer)
  highlightTimer = window.setTimeout(() => {
    highlightedId.value = ''
  }, 6000)
}

async function exportTranscript(): Promise<void> {
  if (!room.value || exporting.value) return
  exporting.value = true
  exportError.value = ''
  try {
    const { blob, filename } = await api.exportRoom(room.value.id)
    downloadBlob(blob, filename)
    menuOpen.value = false
  } catch (error) {
    exportError.value =
      error instanceof ApiError
        ? humanizeServerError(error.message)
        : 'Transkrip belum bisa diunduh. Periksa koneksi lalu coba lagi.'
  } finally {
    exporting.value = false
  }
}

function send(): void {
  const current = store.value
  if (!current || uploadBlock.value !== '') return
  const ready = attachments.value?.ready ?? []
  nearBottom.value = true
  void current.send(current.state.draft, ready, () => attachments.value?.clearReady())
}

function retry(): void {
  const current = store.value
  if (!current || uploadBlock.value !== '') return
  const text = current.state.draft
  const ready = attachments.value?.ready ?? []
  if (text.trim() === '' && ready.length === 0) {
    // Nothing typed: this is a retry of the failed turn, not a resend.
    const user = lastUserMessage.value
    if (user) retryFrom(user)
    return
  }
  nearBottom.value = true
  void current.send(text, ready, () => attachments.value?.clearReady())
}

/** Retry reuses the user message; it never posts a second one. */
function retryFrom(message: ChatMessage): void {
  const current = store.value
  if (!current || isWorking.value || archived.value) return
  if (message.role !== 'user') return
  nearBottom.value = true
  void current.retry(message.id)
}

function addFiles(files: File[]): void {
  if (!attachments.value || archived.value) return
  for (const file of files) void attachments.value.add(file)
}

function onDrop(event: DragEvent): void {
  dragging.value = false
  const files = event.dataTransfer?.files
  if (!files || files.length === 0) return
  addFiles(Array.from(files))
}

function onDragLeave(event: DragEvent): void {
  const next = event.relatedTarget as Node | null
  if (!next || !(event.currentTarget as Node).contains(next)) dragging.value = false
}

function removeAttachment(key: string): void {
  attachments.value?.remove(key)
}

function onFilesChanged(): void {
  void store.value?.reload()
}

function reloadRoom(): void {
  void store.value?.reload()
}

function loadOlder(): Promise<void> {
  return store.value?.loadOlder() ?? Promise.resolve()
}

watch(
  () => route.query.m,
  (value) => {
    if (typeof value === 'string' && value !== '' && room.value) void scrollToMessage(value)
  },
)

watch(
  () => route.params.id,
  (id) => {
    if (typeof id === 'string' && id !== '') void openRoom(id)
  },
  { immediate: true },
)

watch(
  () => [messages.value.length, approvals.value.length, streamingText.value.length],
  async () => {
    if (!nearBottom.value) return
    await nextTick()
    scrollToBottom()
  },
)

// Auto-speak only new replies, never the history a room opens with.
watch(
  () => messages.value.length,
  () => {
    if (!readyForAutoSpeak || !autoSpeak.value) return
    for (const message of messages.value) {
      if (message.role !== 'assistant' || autoSpeakBaseline.has(message.id)) continue
      autoSpeakBaseline.add(message.id)
      if (message.incomplete || (message.state && message.state !== 'done')) continue
      void speaker.toggle(message.id, message.body)
    }
  },
)

watch(phase, async (value) => {
  if (value === 'streaming' && nearBottom.value) {
    await nextTick()
    scrollToBottom()
  }
})

watch(reminderNotice, (value) => {
  if (reminderToastTimer !== undefined) window.clearTimeout(reminderToastTimer)
  if (value === '') return
  reminderToastTimer = window.setTimeout(() => {
    store.value?.clearReminderNotice()
  }, 6000)
})

onMounted(async () => {
  clockTimer = window.setInterval(() => {
    clockNow.value = Date.now()
  }, 1000)
  await nextTick()
  const target = scroller.value?.firstElementChild
  if (target && typeof ResizeObserver !== 'undefined') {
    contentObserver = new ResizeObserver(() => {
      if (nearBottom.value) scrollToBottom()
    })
    contentObserver.observe(target)
  }
  // The pump runs server-side; this only keeps the live line and the room
  // honest when another tab (or the pump itself) changes the state.
  continuationTimer = window.setInterval(() => {
    void store.value?.refreshContinuation()
  }, 4000)
  approvalTimer = window.setInterval(() => {
    void store.value?.refreshApprovals()
  }, 4000)
})

// Coming back from the background (or from bfcache) must re-open the live stream: Android freezes the
// tab, the socket dies silently, and the room would otherwise sit on a zombie connection showing "idle"
// while the agent is still working.
let hiddenAt = 0
function onVisibilityChange(): void {
  if (document.visibilityState === 'hidden') {
    hiddenAt = Date.now()
    return
  }
  const away = hiddenAt === 0 ? 0 : Date.now() - hiddenAt
  hiddenAt = 0
  if (away > 2000) store.value?.resync('visible')
}
function onPageShow(): void {
  if (document.visibilityState === 'visible') store.value?.resync('visible')
}
function onOnline(): void {
  store.value?.resync('visible')
}

onMounted(() => {
  document.addEventListener('visibilitychange', onVisibilityChange)
  window.addEventListener('pageshow', onPageShow)
  window.addEventListener('online', onOnline)
})

onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', onVisibilityChange)
  window.removeEventListener('pageshow', onPageShow)
  window.removeEventListener('online', onOnline)
  contentObserver?.disconnect()
  contentObserver = null
  if (clockTimer !== undefined) window.clearInterval(clockTimer)
  if (continuationTimer !== undefined) window.clearInterval(continuationTimer)
  if (approvalTimer !== undefined) window.clearInterval(approvalTimer)
  if (highlightTimer !== undefined) window.clearTimeout(highlightTimer)
  if (reminderToastTimer !== undefined) window.clearTimeout(reminderToastTimer)
  if (sentToastTimer !== undefined) window.clearTimeout(sentToastTimer)
  voice.dispose()
  speaker.dispose()
  store.value?.dispose()
})
</script>

<template>
  <div
    class="relative flex h-full min-h-0 flex-col"
    @dragenter.prevent="dragging = true"
    @dragover.prevent
    @dragleave="onDragLeave"
    @drop.prevent="onDrop"
  >
    <div
      v-if="dragging"
      class="pointer-events-none absolute inset-0 z-30 flex items-center justify-center bg-bg-deep/80 p-6 backdrop-blur-sm"
      data-testid="drop-overlay"
    >
      <p class="rounded-2xl border border-dashed border-orchid/60 px-6 py-8 text-center text-[13px] text-ink">
        Lepaskan file di sini untuk mengirim ke Hermes.
      </p>
    </div>

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
        <div class="flex min-w-0 shrink-0 items-center gap-2">
          <h1 class="max-w-[42vw] truncate text-[15px] font-semibold tracking-tight text-ink lg:max-w-none lg:text-[17px]">
            {{ room?.name ?? (notFound ? 'Room tidak ada' : 'Memuat room…') }}
          </h1>
          <span
            v-if="archived"
            class="shrink-0 rounded-full border border-line px-2 py-0.5 text-[10.5px] leading-tight text-quiet"
          >
            Diarsipkan
          </span>
          <span
            class="shrink-0 rounded-full border px-2 py-0.5 text-[10.5px] leading-tight"
            :class="stateChipClass"
            data-testid="room-state-chip"
            :data-state="stateChip"
          >
            {{ stateChipLabel }}
          </span>
        </div>
        <p class="hidden min-w-0 flex-1 truncate text-[11.5px] text-quiet sm:block">
          {{ room?.topic || (room ? 'Tanpa topik' : '') }}
        </p>
        <button
          type="button"
          class="tap flex shrink-0 items-center justify-center gap-1.5 rounded-xl border px-2 text-[12px] sm:px-3"
          :class="
            autoSpeak
              ? 'border-orchid/50 bg-violet/15 text-orchid'
              : 'border-line text-muted hover:border-orchid/40 hover:text-ink'
          "
          :aria-label="autoSpeak ? 'Matikan suara otomatis' : 'Bacakan balasan baru otomatis'"
          :aria-pressed="autoSpeak"
          data-testid="autospeak-toggle"
          :data-state="autoSpeak ? 'on' : 'off'"
          @click="toggleAutoSpeak"
        >
          <PhSpeakerHigh v-if="autoSpeak" :size="17" />
          <PhSpeakerSlash v-else :size="17" />
          <span class="hidden sm:inline">{{ autoSpeak ? 'Suara aktif' : 'Suara mati' }}</span>
        </button>
        <button
          type="button"
          class="tap flex shrink-0 items-center justify-center gap-1.5 rounded-xl border border-line px-2 text-[12px] text-muted hover:border-orchid/40 hover:text-ink sm:px-3"
          aria-label="Buka semua file room ini"
          data-testid="open-files"
          @click="libraryOpen = true"
        >
          <PhFiles :size="17" />
          <span class="hidden sm:inline">File</span>
        </button>

        <div class="relative shrink-0">
          <button
            type="button"
            class="tap flex items-center justify-center rounded-xl border border-line px-2 text-muted hover:border-orchid/40 hover:text-ink"
            aria-label="Menu room"
            aria-haspopup="menu"
            :aria-expanded="menuOpen"
            data-testid="room-menu-more"
            @click="menuOpen = !menuOpen"
          >
            <PhDotsThreeVertical :size="17" />
          </button>

          <div v-if="menuOpen" class="fixed inset-0 z-20" @click="menuOpen = false" />

          <div
            v-if="menuOpen"
            class="absolute right-0 top-full z-30 mt-1.5 w-60 rounded-xl border border-line bg-bg-deep p-1.5 shadow-2xl"
            role="menu"
          >
            <button
              type="button"
              class="tap flex w-full items-start gap-2 rounded-lg px-2.5 py-2 text-left hover:bg-surface disabled:opacity-60"
              role="menuitem"
              :disabled="exporting"
              data-testid="export-room"
              @click="exportTranscript"
            >
              <PhDownloadSimple :size="16" class="mt-0.5 shrink-0 text-muted" />
              <span class="min-w-0">
                <span class="block text-[13px] text-ink">
                  {{ exporting ? 'Menyiapkan transkrip…' : 'Ekspor transkrip' }}
                </span>
                <span class="mt-0.5 block text-[11.5px] text-quiet">Markdown (.md)</span>
              </span>
            </button>
            <button
              type="button"
              class="tap flex w-full items-start gap-2 rounded-lg px-2.5 py-2 text-left hover:bg-surface"
              role="menuitem"
              data-testid="open-inspector"
              @click="menuOpen = false; inspectorOpen = true"
            >
              <PhBell :size="16" class="mt-0.5 shrink-0 text-muted" />
              <span class="min-w-0">
                <span class="block text-[13px] text-ink">Pengingat &amp; notifikasi</span>
                <span class="mt-0.5 block text-[11.5px] text-quiet">
                  Daftar pengingat dan notifikasi perangkat
                </span>
              </span>
            </button>
            <p v-if="exportError" class="px-2.5 pb-1 pt-1.5 text-[11.5px] leading-snug text-muted" role="alert">
              {{ exportError }}
            </p>
          </div>
        </div>
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
              <template v-for="item in timelineItems" :key="item.key">
                <li
                  v-if="item.message"
                  :data-message-id="item.message.id"
                  class="rounded-2xl transition-shadow"
                  :class="
                    highlightedId === item.message.id
                      ? 'ring-2 ring-orchid/60 ring-offset-2 ring-offset-bg'
                      : ''
                  "
                >
                  <MessageBubble
                    :message="item.message"
                    :speaking="speaker.state.playingId === item.message.id"
                    :loading-speech="speaker.state.loadingId === item.message.id"
                    :can-retry="item.message.role === 'user'"
                    :retry-disabled="isWorking || archived"
                    @speak="speakMessage(item.message)"
                    @retry="retryFrom(item.message)"
                  />
                </li>
                <li v-else-if="item.approval">
                  <ApprovalCard
                    :approval="item.approval"
                    :resolving-choice="
                      resolvingApproval === item.approval.id ? resolvingChoice : ''
                    "
                    @resolve="resolveApproval(item.approval.id, $event)"
                  />
                </li>
              </template>
            </ol>
            <p v-if="approvalError" class="text-[12px] text-muted" role="alert" data-testid="approval-error">
              {{ approvalError }}
            </p>

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

            <!-- A failed turn that produced no text is still a real failure:
                 it gets a visible retry, not a silent empty gap. -->
            <div v-if="showEmptyRetry" class="flex gap-3" data-testid="empty-reply">
              <BrandMark :size="26" class="mt-0.5" />
              <div class="min-w-0 flex-1">
                <p class="text-[13px] leading-relaxed text-muted">
                  Hermes tidak mengirim balasan untuk pesan ini.
                </p>
                <button
                  type="button"
                  class="tap mt-2 inline-flex h-11 items-center rounded-xl border border-line px-3 text-[13px] text-ink hover:border-orchid/50 disabled:opacity-60"
                  :disabled="isWorking || archived"
                  data-testid="retry-empty"
                  @click="lastUserMessage && retryFrom(lastUserMessage)"
                >
                  Coba lagi
                </button>
              </div>
            </div>

            <!-- A pending safety-net follow-up is live, not a black box. -->
            <ContinuationLine
              v-if="!isWorking && continuation"
              :continuation="continuation"
              @cancel="stopContinuation"
            />
            <p v-if="continuationError" class="text-[12px] text-quiet" role="alert">
              {{ continuationError }}
            </p>
            <p v-if="stopNotice" class="text-[12px] text-quiet" role="status" data-testid="stop-notice">
              {{ stopNotice }}
            </p>

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
            v-if="(hasDraft || lastUserMessage) && !isWorking"
            type="button"
            class="inline-flex h-11 items-center text-[12.5px] font-medium text-orchid hover:text-orchid/80"
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

    <Transition
      enter-active-class="transition duration-150 ease-out"
      enter-from-class="translate-y-2 opacity-0"
      leave-active-class="transition duration-100 ease-in"
      leave-to-class="translate-y-2 opacity-0"
    >
      <div
        v-if="sentNotice"
        class="pointer-events-none fixed inset-x-0 bottom-36 z-30 flex justify-center px-4"
        role="status"
        aria-live="polite"
      >
        <p
          class="max-w-full truncate rounded-full border border-orchid/40 bg-bg-deep/95 px-4 py-2 text-[12.5px] text-ink shadow-xl backdrop-blur"
          data-testid="sent-toast"
        >
          {{ sentNotice }}
        </p>
      </div>
    </Transition>

    <Transition
      enter-active-class="transition duration-150 ease-out"
      enter-from-class="translate-y-2 opacity-0"
      leave-active-class="transition duration-100 ease-in"
      leave-to-class="translate-y-2 opacity-0"
    >
      <div
        v-if="reminderNotice"
        class="pointer-events-none fixed inset-x-0 bottom-24 z-30 flex justify-center px-4"
        role="status"
        aria-live="polite"
      >
        <p
          class="rounded-full border border-orchid/40 bg-bg-deep/95 px-4 py-2 text-[12.5px] text-ink shadow-xl backdrop-blur"
          data-testid="reminder-toast"
        >
          {{ reminderNotice }}
        </p>
      </div>
    </Transition>

    <Composer
      v-if="store"
      v-model="draft"
      :running="isWorking"
      :archived="archived"
      :attachments="pendingAttachments"
      :upload-block="uploadBlock"
      :voice-phase="voice.state.phase"
      :voice-elapsed="voice.state.elapsedMs"
      :voice-level="voice.state.level"
      :voice-error="voice.state.errorText"
      :voice-locked="voice.state.locked"
      :can-stop="canStop"
      :stopping="stopping"
      :stop-armed="stopArmed"
      @send="send"
      @stop="requestStop"
      @stop-confirm="stopNow"
      @stop-cancel="cancelStop"
      @files="addFiles"
      @remove="removeAttachment"
      @voice-start="voice.start(false)"
      @voice-stop="voice.stop()"
      @voice-cancel="voice.cancel()"
      @voice-toggle="voice.toggle(true)"
      @voice-dismiss="dismissVoiceError"
    />

    <div v-else-if="room" class="border-t border-line px-4 py-3">
      <p class="text-center text-[12px] text-quiet">Menyiapkan composer…</p>
    </div>

    <FileLibrary
      :open="libraryOpen"
      :room-id="room?.id ?? ''"
      @close="libraryOpen = false"
      @changed="onFilesChanged"
    />

    <RoomInspector
      :open="inspectorOpen"
      :room-id="room?.id ?? ''"
      @close="inspectorOpen = false"
    />
  </div>
</template>
