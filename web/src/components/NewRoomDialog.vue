<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { PhX } from '@phosphor-icons/vue'
import { ApiError } from '../lib/api'
import { humanizeServerError } from '../lib/errors'
import { createRoom } from '../stores/rooms'
import { markSignedOut } from '../stores/auth'
import { closeNewRoom, closeRoomSheet, newRoomOpen } from '../stores/ui'

const router = useRouter()
const name = ref('')
const topic = ref('')
const error = ref('')
const busy = ref(false)
const nameInput = ref<HTMLInputElement | null>(null)

function reset(): void {
  name.value = ''
  topic.value = ''
  error.value = ''
  busy.value = false
}

watch(newRoomOpen, async (open) => {
  if (!open) return
  reset()
  await nextTick()
  nameInput.value?.focus()
})

function close(): void {
  if (busy.value) return
  closeNewRoom()
}

async function submit(): Promise<void> {
  if (busy.value) return
  const cleanName = name.value.trim()
  if (cleanName === '') {
    error.value = 'Beri room ini nama dulu.'
    nameInput.value?.focus()
    return
  }

  busy.value = true
  error.value = ''
  try {
    const room = await createRoom(cleanName, topic.value.trim())
    closeNewRoom()
    closeRoomSheet()
    await router.push(`/r/${room.id}`)
  } catch (cause) {
    if (cause instanceof ApiError && cause.status === 401) {
      markSignedOut()
      closeNewRoom()
      await router.replace({ name: 'login' })
      return
    }
    error.value =
      cause instanceof ApiError
        ? humanizeServerError(cause.message)
        : 'Iris tidak bisa dihubungi. Room belum dibuat — coba lagi.'
    await nextTick()
    nameInput.value?.focus()
  } finally {
    busy.value = false
  }
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape' && newRoomOpen.value) close()
}

onMounted(() => window.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <Teleport to="body">
    <Transition
      enter-active-class="transition duration-150 ease-out"
      enter-from-class="opacity-0"
      leave-active-class="transition duration-100 ease-in"
      leave-to-class="opacity-0"
    >
      <div
        v-if="newRoomOpen"
        class="fixed inset-0 z-50 flex items-end justify-center bg-bg-deep/80 p-3 backdrop-blur-sm sm:items-center sm:p-4"
        @click.self="close"
      >
        <div
          class="max-h-[calc(100dvh-1.5rem)] w-full max-w-md overflow-y-auto rounded-2xl border border-line bg-surface p-5 shadow-2xl"
          role="dialog"
          aria-modal="true"
          aria-labelledby="new-room-title"
        >
          <div class="flex items-start justify-between gap-4">
            <div>
              <h2 id="new-room-title" class="text-[17px] font-semibold tracking-tight text-ink">
                Room baru
              </h2>
              <p class="mt-1 text-[13px] text-quiet">
                Satu room, satu percakapan dengan Hermes.
              </p>
            </div>
            <button
              type="button"
              class="tap -mr-2 -mt-2 flex items-center justify-center rounded-lg text-quiet hover:text-ink"
              aria-label="Tutup"
              @click="close"
            >
              <PhX :size="18" />
            </button>
          </div>

          <form class="mt-5 space-y-4" @submit.prevent="submit">
            <div>
              <label for="room-name" class="mb-1.5 block text-[12.5px] font-medium text-muted">
                Nama room
              </label>
              <input
                id="room-name"
                ref="nameInput"
                v-model="name"
                type="text"
                maxlength="200"
                autocomplete="off"
                placeholder="Misalnya: Riset harga"
                class="w-full rounded-xl border border-line bg-bg px-3.5 py-3 text-[16px] text-ink outline-none placeholder:text-quiet/80 focus:border-orchid/70 sm:text-[14px]"
              />
            </div>

            <div>
              <label for="room-topic" class="mb-1.5 block text-[12.5px] font-medium text-muted">
                Topik <span class="font-normal text-quiet">(opsional)</span>
              </label>
              <textarea
                id="room-topic"
                v-model="topic"
                rows="2"
                maxlength="4000"
                placeholder="Konteks singkat yang berguna untuk Hermes"
                class="w-full resize-none rounded-xl border border-line bg-bg px-3.5 py-3 text-[16px] text-ink outline-none placeholder:text-quiet/80 focus:border-orchid/70 sm:text-[14px]"
              />
            </div>

            <p v-if="error" class="text-[13px] leading-snug text-orchid" role="alert">
              {{ error }}
            </p>

            <div class="flex items-center justify-end gap-2 pt-1">
              <button
                type="button"
                class="tap rounded-xl px-3 text-[13.5px] text-muted hover:text-ink"
                :disabled="busy"
                @click="close"
              >
                Batal
              </button>
              <button
                type="submit"
                class="tap rounded-xl bg-violet px-4 text-[13.5px] font-medium text-ink transition-colors hover:bg-violet/90 disabled:opacity-60"
                :disabled="busy || name.trim() === ''"
                data-testid="create-room"
              >
                {{ busy ? 'Membuat…' : 'Buat room' }}
              </button>
            </div>
          </form>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>
