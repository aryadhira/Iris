<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import {
  PhLockSimple,
  PhMicrophone,
  PhPaperclip,
  PhPaperPlaneRight,
  PhStop,
  PhX,
} from '@phosphor-icons/vue'
import UploadChip from './UploadChip.vue'
import type { PendingAttachment } from '../stores/attachments'
import { formatElapsed, type PttPhase } from '../lib/voice'

const props = defineProps<{
  modelValue: string
  running: boolean
  archived: boolean
  attachments: PendingAttachment[]
  uploadBlock: string
  voicePhase: PttPhase
  voiceElapsed: number
  voiceLevel: number
  voiceError: string
  voiceLocked: boolean
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
  send: []
  files: [files: File[]]
  remove: [key: string]
  'voice-start': []
  'voice-stop': []
  'voice-cancel': []
  'voice-toggle': []
  'voice-dismiss': []
}>()

const textarea = ref<HTMLTextAreaElement | null>(null)
const picker = ref<HTMLInputElement | null>(null)
const pointerHandled = ref(false)

const recording = computed(() => props.voicePhase === 'recording')
const transcribing = computed(() => props.voicePhase === 'transcribing')
const voiceBusy = computed(() => recording.value || transcribing.value)
const editing = computed(() => props.voicePhase === 'editing')

const hasReady = computed(() =>
  props.attachments.some((item) => item.status === 'ready' && item.id),
)
const canSend = computed(
  () =>
    !props.running &&
    !props.archived &&
    !voiceBusy.value &&
    props.uploadBlock === '' &&
    (props.modelValue.trim() !== '' || hasReady.value),
)

const placeholder = computed(() => {
  if (props.archived) return 'Room ini diarsipkan.'
  if (recording.value) return 'Sedang merekam…'
  if (transcribing.value) return 'Mengubah suara jadi teks…'
  if (props.running) return 'Tunggu balasan selesai…'
  return 'Tulis pesan untuk Hermes…'
})

const BARS = [0.32, 0.58, 0.86, 0.48, 1, 0.72, 0.42, 0.78, 0.55]

function barHeight(index: number): string {
  const factor = BARS[index] ?? 0.5
  // A square-root scale is what an audio meter looks like to the eye; a linear
  // scale makes ordinary speech look like a flat row of dots.
  const level = Math.sqrt(Math.max(0, Math.min(1, props.voiceLevel)))
  return `${4 + Math.round(level * 26 * factor)}px`
}

function autosize(): void {
  const element = textarea.value
  if (!element) return
  element.style.height = 'auto'
  element.style.height = `${Math.min(element.scrollHeight, 168)}px`
}

function submit(): void {
  if (!canSend.value) return
  emit('send')
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key !== 'Enter' || event.shiftKey) return
  if (event.isComposing) return
  event.preventDefault()
  submit()
}

function onInput(event: Event): void {
  emit('update:modelValue', (event.target as HTMLTextAreaElement).value)
}

function openPicker(): void {
  if (props.archived) return
  picker.value?.click()
}

function onPicked(event: Event): void {
  const input = event.target as HTMLInputElement
  const files = input.files ? Array.from(input.files) : []
  if (files.length > 0) emit('files', files)
  input.value = ''
}

function onPaste(event: ClipboardEvent): void {
  if (props.archived) return
  const clipboard = event.clipboardData
  if (!clipboard) return
  const fromItems = Array.from(clipboard.items)
    .filter((item) => item.kind === 'file')
    .map((item) => item.getAsFile())
    .filter((file): file is File => file !== null)
  const files = fromItems.length > 0 ? fromItems : Array.from(clipboard.files)
  if (files.length === 0) return
  event.preventDefault()
  emit('files', files)
}

function onMicPointerDown(event: PointerEvent): void {
  if (props.archived || transcribing.value) return
  pointerHandled.value = true
  if (event.pointerType === 'touch') {
    // A tap locks the recording; a second tap stops it.
    emit('voice-toggle')
    return
  }
  if (event.button !== 0) return
  emit('voice-start')
}

function onMicPointerUp(event: PointerEvent): void {
  if (event.pointerType === 'touch') return
  if (recording.value) emit('voice-stop')
}

function onMicPointerCancel(): void {
  if (recording.value) emit('voice-cancel')
}

function onMicPointerLeave(event: PointerEvent): void {
  if (event.pointerType === 'touch') return
  if (recording.value && !props.voiceLocked) emit('voice-stop')
}

function onMicClick(): void {
  // Pointer events already handled this gesture. A keyboard activation has
  // detail === 0 and must still work.
  if (pointerHandled.value) {
    pointerHandled.value = false
    return
  }
  if (recording.value) {
    emit('voice-stop')
    return
  }
  emit('voice-start')
}

watch(
  () => props.modelValue,
  async () => {
    await nextTick()
    autosize()
  },
)

onMounted(autosize)
</script>

<template>
  <div class="border-t border-line bg-bg">
    <div class="safe-bottom mx-auto w-full max-w-3xl px-3 pt-3 lg:px-8">
      <p
        v-if="archived"
        class="mb-2 flex items-start gap-2 rounded-xl border border-line bg-surface/60 px-3 py-2 text-[12.5px] leading-snug text-muted"
        data-testid="archived-note"
      >
        <PhLockSimple :size="15" class="mt-0.5 shrink-0 text-quiet" />
        Room ini diarsipkan. Kamu bisa membaca semua pesan, tapi tidak bisa mengirim pesan baru.
      </p>

      <div
        v-if="attachments.length > 0"
        class="mb-2 flex gap-2 overflow-x-auto pb-0.5"
        data-testid="pending-attachments"
      >
        <UploadChip
          v-for="item in attachments"
          :key="item.key"
          :item="item"
          class="shrink-0"
          @remove="emit('remove', item.key)"
        />
      </div>

      <p
        v-if="uploadBlock"
        class="mb-2 text-[11.5px] leading-snug text-quiet"
        data-testid="upload-block"
      >
        {{ uploadBlock }}
      </p>

      <div
        v-if="recording"
        class="mb-2 flex items-center gap-2 rounded-xl border border-orchid/40 bg-surface/80 px-3 py-2"
        data-testid="recording-state"
      >
        <span class="h-2.5 w-2.5 shrink-0 animate-pulse rounded-full bg-red-400" aria-hidden="true" />
        <span class="w-[42px] shrink-0 font-mono text-[13px] tabular-nums text-ink" data-testid="recording-timer">
          {{ formatElapsed(voiceElapsed) }}
        </span>
        <div class="flex h-7 min-w-0 flex-1 items-end gap-[3px]" data-testid="level-meter" aria-hidden="true">
          <span
            v-for="(_, index) in BARS"
            :key="index"
            class="w-full rounded-full bg-orchid/80 transition-[height] duration-75"
            :style="{ height: barHeight(index) }"
          />
        </div>
        <span class="hidden shrink-0 text-[11.5px] text-quiet sm:inline">
          {{ voiceLocked ? 'ketuk mic untuk berhenti' : 'lepas untuk berhenti' }}
        </span>
        <button
          type="button"
          class="tap flex shrink-0 items-center gap-1 rounded-lg px-2 text-[12.5px] text-muted hover:text-ink"
          data-testid="recording-cancel"
          aria-label="Batalkan rekaman"
          @click="emit('voice-cancel')"
        >
          <PhX :size="14" />
          batal
        </button>
      </div>

      <div
        v-else-if="transcribing"
        class="mb-2 flex items-center gap-2 rounded-xl border border-line bg-surface/60 px-3 py-2 text-[12.5px] text-muted"
        data-testid="transcribing-state"
        role="status"
      >
        <span class="h-2 w-2 animate-pulse rounded-full bg-orchid" aria-hidden="true" />
        Mengubah suara jadi teks…
      </div>

      <p
        v-else-if="editing"
        class="mb-2 text-[11.5px] leading-snug text-quiet"
        data-testid="transcript-ready"
      >
        Transkrip sudah masuk ke kotak. Periksa nama dan angkanya sebelum kirim.
      </p>

      <div
        v-if="voiceError"
        class="mb-2 flex items-start gap-2 rounded-xl border border-line bg-surface px-3 py-2"
        role="alert"
        data-testid="voice-error"
      >
        <p class="min-w-0 flex-1 text-[12px] leading-snug text-muted">{{ voiceError }}</p>
        <button
          type="button"
          class="tap shrink-0 rounded-lg px-2 text-[11.5px] text-quiet hover:text-ink"
          aria-label="Tutup pesan kesalahan suara"
          @click="emit('voice-dismiss')"
        >
          tutup
        </button>
      </div>

      <div
        class="rounded-2xl border border-line bg-surface transition-colors focus-within:border-orchid/60"
        :class="{ 'opacity-60': archived }"
      >
        <textarea
          ref="textarea"
          :value="modelValue"
          rows="1"
          :disabled="running || archived || voiceBusy"
          data-testid="composer-input"
          :placeholder="placeholder"
          :aria-label="placeholder"
          class="block max-h-[168px] w-full resize-none bg-transparent px-4 pb-1 pt-3 text-[16px] leading-relaxed text-ink outline-none placeholder:text-quiet/80 disabled:cursor-not-allowed sm:text-[14px]"
          @input="onInput"
          @keydown="onKeydown"
          @paste="onPaste"
        />

        <input
          ref="picker"
          type="file"
          multiple
          class="sr-only"
          data-testid="file-input"
          accept=".png,.jpg,.jpeg,.webp,.gif,.pdf,.docx,.xlsx,.txt,.md,.markdown,.csv,.json,.zip,image/*,application/pdf,text/plain,text/markdown,text/csv,application/json"
          @change="onPicked"
        />

        <div class="flex items-center justify-between gap-2 px-2 pb-2 pt-1">
          <div class="flex min-w-0 items-center gap-1">
            <button
              type="button"
              class="tap flex items-center justify-center rounded-xl text-muted hover:text-ink disabled:cursor-not-allowed disabled:opacity-40"
              :disabled="archived"
              aria-label="Lampirkan file"
              title="Lampirkan file"
              data-testid="attach-button"
              @click="openPicker"
            >
              <PhPaperclip :size="18" />
            </button>
            <button
              type="button"
              class="tap flex items-center justify-center rounded-xl transition-colors disabled:cursor-not-allowed disabled:opacity-40"
              :class="
                recording
                  ? 'bg-violet/25 text-orchid ring-1 ring-orchid/60'
                  : 'text-muted hover:text-ink'
              "
              :disabled="archived || transcribing"
              :aria-label="recording ? 'Berhenti merekam' : 'Rekam suara'"
              :aria-pressed="recording"
              :data-state="voicePhase"
              data-testid="mic-button"
              title="Tahan untuk bicara (ketuk untuk kunci di HP)"
              @pointerdown.prevent="onMicPointerDown"
              @pointerup.prevent="onMicPointerUp"
              @pointercancel="onMicPointerCancel"
              @pointerleave="onMicPointerLeave"
              @click="onMicClick"
            >
              <PhMicrophone v-if="!recording" :size="19" weight="bold" />
              <PhStop v-else :size="17" weight="fill" />
            </button>
            <span class="hidden truncate pl-1 text-[11px] text-quiet sm:block">
              Enter mengirim · tahan mic untuk bicara
            </span>
          </div>

          <div class="ml-auto flex shrink-0 items-center gap-1.5">
            <span
              v-if="running"
              class="inline-flex"
              title="Stop belum tersambung ke server. Tutup room ini untuk berhenti menonton balasan."
            >
              <button
                type="button"
                class="tap flex items-center gap-1.5 rounded-xl border border-line px-3 text-[12.5px] text-quiet"
                disabled
                data-testid="stop-button"
                aria-describedby="stop-note"
              >
                <PhStop :size="14" weight="fill" />
                Stop
              </button>
            </span>
            <span id="stop-note" class="sr-only">
              Stop belum tersambung ke server; tombol ini sengaja dinonaktifkan.
            </span>

            <button
              type="button"
              class="tap flex items-center gap-1.5 rounded-xl bg-violet px-3.5 text-[13px] font-medium text-ink transition-colors hover:bg-violet/90 disabled:cursor-not-allowed disabled:opacity-50"
              :disabled="!canSend"
              data-testid="send-button"
              @click="submit"
            >
              <PhPaperPlaneRight :size="15" weight="fill" />
              Kirim
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
