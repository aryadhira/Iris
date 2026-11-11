<script setup lang="ts">
import { onBeforeUnmount, onMounted, watch } from 'vue'
import { RouterView, useRoute } from 'vue-router'
import { PhX } from '@phosphor-icons/vue'
import BrandMark from '../components/BrandMark.vue'
import RoomList from '../components/RoomList.vue'
import NewRoomDialog from '../components/NewRoomDialog.vue'
import { loadRooms } from '../stores/rooms'
import { closeRoomSheet, roomSheetOpen } from '../stores/ui'

const route = useRoute()

watch(
  () => route.fullPath,
  () => closeRoomSheet(),
)

watch(roomSheetOpen, (open) => {
  document.body.style.overflow = open ? 'hidden' : ''
})

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') closeRoomSheet()
}

onMounted(() => {
  void loadRooms()
  window.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
  document.body.style.overflow = ''
})
</script>

<template>
  <div class="flex h-dvh overflow-hidden bg-bg">
    <!-- Desktop: the room list is always beside the conversation. -->
    <aside class="hidden w-[292px] shrink-0 flex-col border-r border-line bg-bg-deep lg:flex">
      <div class="flex items-center gap-2.5 px-5 pb-4 pt-5">
        <BrandMark :size="30" />
        <div class="min-w-0">
          <p class="text-[15px] font-semibold leading-none tracking-tight text-ink">Iris</p>
          <p class="mt-1.5 truncate text-[11px] leading-none text-quiet">
            your Hermes, in your own rooms
          </p>
        </div>
      </div>
      <RoomList class="min-h-0 flex-1" />
    </aside>

    <div class="flex min-h-0 min-w-0 flex-1 flex-col">
      <RouterView />
    </div>

    <!-- Phone: the same list, as a sheet from the left. -->
    <Teleport to="body">
      <Transition
        enter-active-class="transition duration-150 ease-out"
        enter-from-class="opacity-0"
        leave-active-class="transition duration-100 ease-in"
        leave-to-class="opacity-0"
      >
        <div v-if="roomSheetOpen" class="fixed inset-0 z-40 lg:hidden">
          <div
            class="absolute inset-0 bg-bg-deep/75 backdrop-blur-sm"
            @click="closeRoomSheet"
          />
          <div
            class="sheet-in absolute inset-y-0 left-0 flex w-[86%] max-w-[320px] flex-col border-r border-line bg-bg-deep shadow-2xl"
            role="dialog"
            aria-modal="true"
            aria-label="Daftar room"
          >
            <div class="safe-top flex items-center justify-between px-4 pb-2">
              <div class="flex items-center gap-2.5">
                <BrandMark :size="26" />
                <span class="text-[15px] font-semibold tracking-tight text-ink">Iris</span>
              </div>
              <button
                type="button"
                class="tap flex items-center justify-center rounded-lg text-quiet hover:text-ink"
                aria-label="Tutup daftar room"
                @click="closeRoomSheet"
              >
                <PhX :size="18" />
              </button>
            </div>
            <RoomList class="min-h-0 flex-1" />
          </div>
        </div>
      </Transition>
    </Teleport>

    <NewRoomDialog />
  </div>
</template>
