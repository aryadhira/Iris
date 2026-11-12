<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { PhArchive, PhPlus } from '@phosphor-icons/vue'
import { api } from '../lib/api'
import {
  activeRooms,
  archiveRoom,
  forgetRoom,
  loadRooms,
  roomsState,
} from '../stores/rooms'
import { markSignedOut } from '../stores/auth'
import { closeRoomSheet, openNewRoom } from '../stores/ui'
import BrandMark from './BrandMark.vue'
import RoomSearch from './RoomSearch.vue'

const route = useRoute()
const router = useRouter()
const state = roomsState()

const searching = ref(false)
const confirmingId = ref<string | null>(null)
const archivingId = ref<string | null>(null)
const archiveError = ref('')

const rooms = computed(() => activeRooms.value)
const activeId = computed(() => (typeof route.params.id === 'string' ? route.params.id : ''))

function isActive(id: string): boolean {
  return activeId.value === id
}

function startArchive(id: string): void {
  archiveError.value = ''
  confirmingId.value = id
}

async function confirmArchive(id: string): Promise<void> {
  archivingId.value = id
  archiveError.value = ''
  try {
    await archiveRoom(id)
    forgetRoom(id)
    confirmingId.value = null
    if (isActive(id)) {
      closeRoomSheet()
      await router.replace('/rooms')
    }
  } catch {
    archiveError.value = 'Room ini belum bisa diarsipkan. Coba lagi.'
  } finally {
    archivingId.value = null
  }
}

async function signOut(): Promise<void> {
  try {
    await api.logout()
  } catch {
    // The cookie may already be gone; the screen still locks.
  }
  markSignedOut()
  closeRoomSheet()
  await router.replace({ name: 'login' })
}

function goToRoom(id: string): void {
  closeRoomSheet()
  confirmingId.value = null
  void router.push(`/r/${id}`)
}

let refreshTimer: number | undefined

onMounted(() => {
  void loadRooms()
  // The dot is a live answer to "is Hermes waiting on me?", so the list reads
  // the count again instead of trusting the first page load.
  refreshTimer = window.setInterval(() => {
    void loadRooms(true)
  }, 5000)
})

onBeforeUnmount(() => {
  if (refreshTimer !== undefined) window.clearInterval(refreshTimer)
})
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <div class="px-3 pb-3">
      <button
        type="button"
        class="tap flex w-full items-center gap-2 rounded-xl bg-violet px-3.5 text-[13.5px] font-medium text-ink transition-colors hover:bg-violet/90"
        data-testid="new-room-sidebar"
        @click="openNewRoom"
      >
        <PhPlus :size="16" weight="bold" />
        Room baru
      </button>
    </div>

    <RoomSearch @searching="searching = $event" />

    <div v-if="!searching" class="min-h-0 flex-1 overflow-y-auto px-2 pb-3" data-testid="room-list">
      <p v-if="state.loading && !state.loaded" class="px-2 py-3 text-[13px] text-quiet">
        Memuat room…
      </p>

      <div v-else-if="state.error && rooms.length === 0" class="mx-1 rounded-xl border border-line px-3 py-3">
        <p class="text-[13px] text-muted">{{ state.error }}</p>
        <button
          type="button"
          class="mt-2 text-[13px] font-medium text-orchid"
          @click="loadRooms(true)"
        >
          Coba lagi
        </button>
      </div>

      <div v-else-if="rooms.length === 0" class="mx-1 rounded-xl border border-dashed border-line px-3 py-4">
        <p class="text-[13px] font-medium text-ink">Belum ada room</p>
        <p class="mt-1 text-[12.5px] leading-relaxed text-quiet">
          Room adalah percakapan terpisah dengan Hermes yang sama.
        </p>
      </div>

      <ul v-else class="space-y-1">
        <li v-for="room in rooms" :key="room.id">
          <div
            class="group relative rounded-xl transition-colors"
            :class="isActive(room.id) ? 'bg-surface' : 'hover:bg-surface/70'"
          >
            <button
              type="button"
              class="tap flex w-full flex-col items-start justify-center rounded-xl py-2.5 pl-3 pr-12 text-left"
              :aria-current="isActive(room.id) ? 'page' : undefined"
              @click="goToRoom(room.id)"
            >
              <span class="flex w-full items-center gap-1.5">
                <span
                  v-if="isActive(room.id)"
                  class="h-1.5 w-1.5 shrink-0 rounded-full bg-orchid"
                  aria-hidden="true"
                />
                <span class="truncate text-[13.5px]" :class="isActive(room.id) ? 'text-ink' : 'text-muted'">
                  {{ room.name }}
                </span>
                <span
                  v-if="(room.pending_approvals ?? 0) > 0"
                  class="h-2 w-2 shrink-0 rounded-full bg-orchid"
                  data-testid="room-approval-dot"
                  :aria-label="`${room.pending_approvals} izin menunggu`"
                  :title="`${room.pending_approvals} izin menunggu`"
                />
              </span>
              <span v-if="room.topic" class="w-full truncate text-[12px] text-quiet">
                {{ room.topic }}
              </span>
            </button>

            <button
              v-if="confirmingId !== room.id"
              type="button"
              class="tap absolute right-0 top-1/2 flex -translate-y-1/2 items-center justify-center rounded-lg text-quiet opacity-70 transition-opacity hover:text-ink lg:opacity-0 lg:group-hover:opacity-100 lg:group-focus-within:opacity-100"
              :aria-label="`Arsipkan room ${room.name}`"
              title="Arsipkan room"
              data-testid="archive-room"
              @click="startArchive(room.id)"
            >
              <PhArchive :size="17" />
            </button>
          </div>

          <div
            v-if="confirmingId === room.id"
            class="mx-1 mt-1 rounded-xl border border-line bg-surface px-3 py-2.5"
          >
            <p class="text-[12.5px] leading-snug text-muted">
              Arsipkan <span class="text-ink">{{ room.name }}</span
              >? Room hilang dari daftar dan jadi hanya-baca.
            </p>
            <div class="mt-2 flex items-center gap-2">
              <button
                type="button"
                class="tap rounded-lg bg-violet px-3 text-[12.5px] font-medium text-ink disabled:opacity-60"
                :disabled="archivingId === room.id"
                data-testid="archive-confirm"
                @click="confirmArchive(room.id)"
              >
                {{ archivingId === room.id ? 'Mengarsipkan…' : 'Ya, arsipkan' }}
              </button>
              <button
                type="button"
                class="tap rounded-lg px-3 text-[12.5px] text-muted hover:text-ink"
                :disabled="archivingId === room.id"
                data-testid="archive-cancel"
                @click="confirmingId = null"
              >
                Batal
              </button>
            </div>
          </div>
        </li>
      </ul>

      <p v-if="archiveError" class="mt-2 px-2 text-[12.5px] text-muted" role="alert">
        {{ archiveError }}
      </p>
    </div>

    <div class="border-t border-line px-3 py-3">
      <div class="flex items-center justify-between gap-2">
        <div class="flex min-w-0 items-center gap-2">
          <BrandMark :size="24" />
          <span class="truncate text-[12px] text-quiet">Iris · satu Hermes</span>
        </div>
        <button
          type="button"
          class="tap rounded-lg px-2 text-[12.5px] text-muted transition-colors hover:text-ink"
          @click="signOut"
        >
          Keluar
        </button>
      </div>
    </div>
  </div>
</template>
