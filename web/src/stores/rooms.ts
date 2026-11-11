import { computed, reactive } from 'vue'
import { api } from '../lib/api'
import type { Room } from '../lib/types'

const state = reactive({
  rooms: [] as Room[],
  loading: false,
  loaded: false,
  error: '',
})

let inflight: Promise<void> | null = null

export function roomsState() {
  return state
}

export const activeRooms = computed(() => state.rooms.filter((room) => !room.archived_at))

export async function loadRooms(force = false): Promise<void> {
  if (state.loaded && !force) return
  if (inflight) return inflight

  state.loading = true
  state.error = ''
  inflight = (async () => {
    try {
      const rooms = await api.listRooms()
      state.rooms = rooms
      state.loaded = true
    } catch (error) {
      state.error =
        error instanceof Error
          ? 'Iris tidak bisa memuat daftar room. Coba lagi.'
          : 'Iris tidak bisa memuat daftar room.'
    } finally {
      state.loading = false
      inflight = null
    }
  })()

  return inflight
}

export function upsertRoom(room: Room): void {
  const index = state.rooms.findIndex((existing) => existing.id === room.id)
  if (index === -1) state.rooms.unshift(room)
  else state.rooms[index] = room
}

export function removeRoom(id: string): void {
  state.rooms = state.rooms.filter((room) => room.id !== id)
}

export async function createRoom(name: string, topic: string): Promise<Room> {
  const room = await api.createRoom(name, topic)
  upsertRoom(room)
  return room
}

export async function archiveRoom(id: string): Promise<Room> {
  const room = await api.archiveRoom(id)
  removeRoom(id)
  return room
}

/** Last room opened on this device; routing uses it for `/`. */
const LAST_ROOM_KEY = 'iris:lastRoom'

export function rememberRoom(id: string): void {
  try {
    window.localStorage.setItem(LAST_ROOM_KEY, id)
  } catch {
    // Private mode / disabled storage: routing falls back to the list.
  }
}

export function lastRoomId(): string | null {
  try {
    return window.localStorage.getItem(LAST_ROOM_KEY)
  } catch {
    return null
  }
}

export function forgetRoom(id: string): void {
  try {
    if (window.localStorage.getItem(LAST_ROOM_KEY) === id) {
      window.localStorage.removeItem(LAST_ROOM_KEY)
    }
  } catch {
    // ignore
  }
}
