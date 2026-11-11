import { ref } from 'vue'

/** Mobile room-list sheet; desktop never reads it. */
export const roomSheetOpen = ref(false)
export const newRoomOpen = ref(false)

export function openRoomSheet(): void {
  roomSheetOpen.value = true
}

export function closeRoomSheet(): void {
  roomSheetOpen.value = false
}

export function openNewRoom(): void {
  // On a phone the dialog should stand alone, not on top of the sheet.
  roomSheetOpen.value = false
  newRoomOpen.value = true
}

export function closeNewRoom(): void {
  newRoomOpen.value = false
}
