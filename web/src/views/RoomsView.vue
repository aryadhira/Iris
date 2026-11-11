<script setup lang="ts">
import { computed } from 'vue'
import { PhArrowUpRight, PhList, PhPlus } from '@phosphor-icons/vue'
import BrandMark from '../components/BrandMark.vue'
import { activeRooms, loadRooms, roomsState } from '../stores/rooms'
import { openNewRoom, openRoomSheet } from '../stores/ui'
import { shortDate } from '../lib/format'

const state = roomsState()
const rooms = computed(() => activeRooms.value)
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <header class="flex items-center gap-2 border-b border-line px-4 pb-2.5 pt-[max(0.625rem,env(safe-area-inset-top))] lg:px-8 lg:py-5">
      <button
        type="button"
        class="tap -ml-2 flex items-center justify-center rounded-lg text-muted hover:text-ink lg:hidden"
        aria-label="Buka daftar room"
        data-testid="rooms-menu"
        @click="openRoomSheet"
      >
        <PhList :size="20" />
      </button>

      <div class="min-w-0 flex-1">
        <h1 class="text-[17px] font-semibold leading-tight tracking-tight text-ink lg:text-[22px]">
          Rooms
        </h1>
        <p class="hidden text-[13px] text-quiet lg:mt-1 lg:block">
          Hermes-mu, di room-mu sendiri. Semua room bicara dengan agen yang sama.
        </p>
      </div>

      <button
        type="button"
        class="tap hidden items-center gap-2 rounded-xl bg-violet px-4 text-[13.5px] font-medium text-ink transition-colors hover:bg-violet/90 lg:flex"
        data-testid="new-room"
        @click="openNewRoom"
      >
        <PhPlus :size="16" weight="bold" />
        Room baru
      </button>
    </header>

    <main class="min-h-0 flex-1 overflow-y-auto">
      <div class="mx-auto w-full max-w-3xl px-4 py-5 lg:px-8 lg:py-8">
        <div v-if="state.loading && !state.loaded" class="space-y-3" aria-busy="true">
          <p class="text-[13px] text-quiet">Memuat room…</p>
          <div class="h-24 rounded-2xl border border-line bg-surface/50" />
          <div class="h-24 rounded-2xl border border-line bg-surface/30" />
        </div>

        <div
          v-else-if="state.error && rooms.length === 0"
          class="rounded-2xl border border-line bg-surface/60 p-5"
        >
          <p class="text-[14px] text-muted">{{ state.error }}</p>
          <button
            type="button"
            class="tap mt-2 rounded-xl border border-line px-3 text-[13px] text-ink hover:border-orchid/50"
            @click="loadRooms(true)"
          >
            Coba lagi
          </button>
        </div>

        <div
          v-else-if="rooms.length === 0"
          class="flex flex-col items-center rounded-2xl border border-dashed border-line px-6 py-12 text-center"
        >
          <BrandMark :size="56" variant="full" />
          <h2 class="mt-5 text-[17px] font-semibold tracking-tight text-ink">Belum ada room</h2>
          <p class="mt-2 max-w-xs text-[13.5px] leading-relaxed text-quiet">
            Room adalah percakapan terpisah dengan Hermes. Mulai satu untuk bertanya, menjalankan
            tugas, atau melanjutkan sesuatu yang tadi.
          </p>
          <button
            type="button"
            class="tap mt-5 flex items-center gap-2 rounded-xl bg-violet px-4 text-[13.5px] font-medium text-ink hover:bg-violet/90"
            data-testid="new-room-empty"
            @click="openNewRoom"
          >
            <PhPlus :size="16" weight="bold" />
            Buat room pertama
          </button>
        </div>

        <ul v-else class="grid gap-3 sm:grid-cols-2">
          <li v-for="room in rooms" :key="room.id">
            <RouterLink
              :to="`/r/${room.id}`"
              class="group flex h-full flex-col rounded-2xl border border-line bg-surface/60 p-4 transition-colors hover:border-orchid/40 hover:bg-surface"
            >
              <div class="flex items-start justify-between gap-3">
                <h2 class="min-w-0 break-words text-[15px] font-medium leading-snug text-ink">
                  {{ room.name }}
                </h2>
                <PhArrowUpRight
                  :size="16"
                  class="mt-0.5 shrink-0 text-quiet transition-colors group-hover:text-orchid"
                />
              </div>
              <p v-if="room.topic" class="mt-1.5 line-clamp-2 text-[13px] leading-relaxed text-muted">
                {{ room.topic }}
              </p>
              <p v-else class="mt-1.5 text-[13px] italic text-quiet">Tanpa topik</p>
              <p class="mt-auto pt-4 text-[11.5px] text-quiet">
                Dibuat {{ shortDate(room.created_at) }}
              </p>
            </RouterLink>
          </li>
        </ul>

        <p v-if="rooms.length > 0" class="mt-6 text-[12px] leading-relaxed text-quiet">
          Arsipkan room dari daftar di samping. Room yang diarsipkan tetap bisa dibaca, tapi tidak
          bisa dikirimi pesan.
        </p>
      </div>
    </main>
  </div>
</template>
