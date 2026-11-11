<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { PhLockSimple, PhPaperPlaneRight, PhStop } from '@phosphor-icons/vue'

const props = defineProps<{
  modelValue: string
  running: boolean
  archived: boolean
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
  send: []
}>()

const textarea = ref<HTMLTextAreaElement | null>(null)

const canSend = computed(
  () => !props.running && !props.archived && props.modelValue.trim() !== '',
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
        />

        <div class="flex items-center justify-between gap-2 px-2 pb-2 pt-1">
          <span class="hidden pl-2 text-[11px] text-quiet sm:block">
            Enter mengirim · Shift+Enter baris baru
          </span>

          <div class="ml-auto flex items-center gap-1.5">
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
