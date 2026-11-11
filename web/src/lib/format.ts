// Human-facing Indonesian copy for machine names. The leading underscore on
// internal tools (e.g. _thinking) is noise on screen, never a secret.

const TOOL_LABELS: Record<string, string> = {
  thinking: 'Berpikir',
  terminal: 'Menjalankan perintah',
  shell: 'Menjalankan perintah',
  read_file: 'Membaca berkas',
  write_file: 'Menulis berkas',
  edit_file: 'Mengubah berkas',
  list_files: 'Melihat berkas',
  process_manage: 'Mengelola proses',
  browser: 'Membuka peramban',
  browser_control: 'Membuka peramban',
  web_search: 'Mencari di web',
  search: 'Mencari',
  memory: 'Mengingat',
  todo: 'Menyusun rencana',
  image: 'Membuat gambar',
  tts: 'Menyiapkan suara',
}

export function humanToolName(raw: string): string {
  const name = raw.replace(/^_+/, '').trim()
  if (name === '') return 'Bekerja'
  const key = name.toLowerCase()
  if (TOOL_LABELS[key]) return TOOL_LABELS[key]

  const words = name.replace(/[_-]+/g, ' ').trim()
  return words.charAt(0).toUpperCase() + words.slice(1)
}

export function elapsedLabel(startedAt: number, now: number): string {
  const seconds = Math.max(0, Math.floor((now - startedAt) / 1000))
  if (seconds < 60) return `${seconds} detik`
  const minutes = Math.floor(seconds / 60)
  const rest = seconds % 60
  if (minutes < 60) return `${minutes} mnt ${rest.toString().padStart(2, '0')} dtk`
  const hours = Math.floor(minutes / 60)
  return `${hours} jam ${(minutes % 60).toString().padStart(2, '0')} mnt`
}

export function relativeTime(iso: string, now: number = Date.now()): string {
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return ''
  const seconds = Math.round((now - then) / 1000)
  if (seconds < 45) return 'baru saja'
  if (seconds < 3600) return `${Math.max(1, Math.round(seconds / 60))} mnt lalu`
  if (seconds < 86400) return `${Math.round(seconds / 3600)} jam lalu`
  if (seconds < 86400 * 7) return `${Math.round(seconds / 86400)} hari lalu`
  return new Intl.DateTimeFormat('id-ID', {
    day: 'numeric',
    month: 'short',
  }).format(then)
}

export function shortDate(iso: string): string {
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return ''
  return new Intl.DateTimeFormat('id-ID', {
    day: 'numeric',
    month: 'short',
    year: 'numeric',
  }).format(then)
}
