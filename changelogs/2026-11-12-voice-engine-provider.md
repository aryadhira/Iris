# voice-engine provider — TTS lokal yang bisa di-clone

**Date:** 2026-11-12
**Status:** shipped; default tetap `edge` (belum diaktifkan)

## What changed

1. **Provider switch di `voice/server.py`.** `IRIS_TTS_PROVIDER=edge|engine`.
   `edge` (default) tetap `edge-tts` + `id-ID-GadisNeural` rate `-8%`; `engine`
   memanggil service `voice-engine` di `127.0.0.1:3320` lewat
   `POST /v1/audio/speech` (kontrak gaya OpenAI) dan mengembalikan MP3.
2. **Env baru:** `IRIS_TTS_URL`, `IRIS_TTS_ENGINE_VOICE`, `IRIS_TTS_KEY`,
   `IRIS_TTS_ENGINE_SPEED`. Kontrak `/speak` dan `/speak/stream` tidak berubah —
   `irisd` dan browser tidak disentuh.
3. **Warm-up menyesuaikan provider.** Untuk `engine`, boot hanya memeriksa
   `/healthz` milik voice-engine (model sudah dimuat di sana), bukan handshake
   WebSocket edge-tts.
4. **Health** kini melaporkan `provider` dan nama suara yang benar-benar dipakai.

## Why not switched on

Kualitas suara adalah keputusan rasa. Engine CPU (pocket-tts) belum tentu lebih
enak didengar daripada Gadis Neural, jadi default dibiarkan dan Boss memilih
setelah mendengar A/B. Rollback = satu variabel.

## Evidence (real, 2026-11-12)

- `POST /speak` dengan `IRIS_TTS_PROVIDER=engine`, `IRIS_TTS_ENGINE_VOICE=contoh-id`:
  `HTTP 200`, `Content-Type: audio/mpeg`, 5,28 dtk audio, `X-Iris-Spoken-Text`
  berisi kalimat yang benar.
- `POST /speak/stream` dengan provider `engine`: `meta` → `audio` per kalimat →
  `done`, 2 potongan, total 2,15 dtk.
- `python -m unittest discover -s voice/tests -t .` → **40 tests OK** (jalur `edge`).
- Service `voice-engine` sendiri: smoke test 11/11 (`scripts/smoke.sh` di repo-nya).

## Notes

- `voice/normalise.py` tetap satu-satunya tempat yang membersihkan markdown/rahasia;
  engine menerima teks yang sudah bersih, sama seperti edge-tts.
- Kalau voice-engine gagal (mati/timeout/balas non-200), sidecar mencatat WARNING
  lalu **fallback ke edge-tts** supaya Iris tidak bisu. Ini keputusan ketersediaan:
  suara bisa berubah sesaat lebih baik daripada tidak ada suara sama sekali.
- 2026-11-13: provider `engine` diaktifkan dengan suara `fanny-soegi`
  (sample 20 dtk, cloning zero-shot di CPU). Rollback = `IRIS_TTS_PROVIDER=edge`.
