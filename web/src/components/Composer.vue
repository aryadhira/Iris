<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { PhLockSimple, PhPaperclip, PhPaperPlaneRight, PhStop } from '@phosphor-icons/vue'
import UploadChip from './UploadChip.vue'
import type { PendingAttachment } from '../stores/attachments'

const props = defineProps<{
  modelValue: string
  running: boolean
  archived: boolean
  attachments: PendingAttachment[]
  uploadBlock: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
  send: []
  files: [files: File[]]
  remove: [key: string]
}>()

const textarea = ref<HTMLTextAreaElement | null>(null)
const picker = ref<HTMLInputElement | null>(null)

const hasReady = computed(() =>
  props.attachments.some((item) => item.status === 'ready' && item.id),
)
const canSend = computed(
  () =>
    !props.running &&
    !props.archived &&
    props.uploadBlock === '' &&
    (props.modelValue.trim() !== '' || hasReady.value),
)

const placeholder = computed(() => {
  if (props.archived) return 'Room ini diarsipkan.'
  if (props.running) return 'Tunggu balasan selesai…'
  return 'Tulis pesan untuk Hermes…'
})

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
        class="rounded-2xl border border-line bg-surface transition-colors focus-within:border-orchid/60"
        :class="{ 'opacity-60': archived }"
      >
        <textarea
          ref="textarea"
          :value="modelValue"
          rows="1"
          :disabled="running || archived"
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
            <span class="hidden truncate pl-1 text-[11px] text-quiet sm:block">
              Enter mengirim · Shift+Enter baris baru
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
