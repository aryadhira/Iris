/**
 * Server errors are English (the backend's contract). The person reading Iris
 * reads Indonesian, so known lines are translated here and unknown ones are
 * wrapped in a sentence that does not pretend to know more than it does.
 */
const KNOWN: Record<string, string> = {
  'That passphrase is not correct.': 'Passphrase-nya belum tepat. Coba lagi.',
  'Sign in to Iris first.': 'Sesi kamu sudah berakhir. Masuk lagi.',
  'Your Iris session has ended. Sign in again.': 'Sesi kamu sudah berakhir. Masuk lagi.',
  'Write a message first.': 'Tulis pesan dulu.',
  'There is no room with that id.': 'Room itu tidak ada.',
  'Iris could not save your message. Nothing was sent.':
    'Iris tidak bisa menyimpan pesanmu. Tidak ada yang terkirim.',
  'Hermes did not finish this turn. Try again.':
    'Hermes tidak menyelesaikan balasan ini. Coba lagi.',
  'Hermes went quiet, so the turn stopped. Anything it wrote is kept below.':
    'Hermes berhenti menjawab, jadi giliran ini dihentikan. Teks yang sudah ditulis tetap disimpan.',
  'Hermes reported a problem with this turn.':
    'Hermes melaporkan masalah pada giliran ini.',
  'A room needs a name.': 'Room perlu nama.',
  'Hermes is not answering. No room was created.':
    'Hermes tidak menjawab. Tidak ada room yang dibuat.',
  'Iris could not save the room. Nothing was created.':
    'Iris tidak bisa menyimpan room. Tidak ada yang dibuat.',
}

export function humanizeServerError(message: string): string {
  const trimmed = message.trim()
  if (trimmed === '') return 'Terjadi masalah saat memproses balasan ini.'
  if (KNOWN[trimmed]) return KNOWN[trimmed]
  return `Terjadi masalah: ${trimmed}`
}
