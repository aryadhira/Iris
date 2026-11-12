# Iris voice sidecar

Local speech engines behind a loopback HTTP service. `irisd` is the only caller;
the browser never talks to this process.

```
POST /transcribe   raw audio bytes (webm/opus, ogg, wav, mp3) -> {text, ms}
POST /speak        {"text": "..."} -> audio/mpeg + X-Iris-Spoken-Text
POST /speak/stream {"text": "..."} -> application/x-ndjson (see below)
GET  /health       {ok, model, voice, warm}
```

`/transcribe` decodes with ffmpeg to 16 kHz mono, then runs
`faster-whisper` `small`. An empty recording, a decode failure or a recording
with no speech answers **200** with `{"text": "", "reason": "no_speech"}` — a
quick tap that records nothing is a normal event, never a 500.

`/speak` runs the sanitiser in `voice/normalise.py` first, then edge-tts
(`id-ID-GadisNeural`, rate `-8%`). `X-Iris-Spoken-Text` is the exact sentence
the engine received, which is how tests prove markdown and secrets never reach
TTS. The full formatted answer stays on screen.

## Streaming speech (`/speak/stream`)

Same sanitiser, same voice, but the summary is split into sentences and each
sentence is synthesised and written immediately. The wire format is
**newline-delimited JSON** (`application/x-ndjson`), one object per line, UTF-8:

```json
{"type":"meta","spoken":"Selesai, port kosong. Detailnya ada di layar.","voice":"id-ID-GadisNeural","sentences":2}
{"type":"audio","index":0,"text":"Selesai, port kosong.","ms":812,"audio":"<base64 MP3>"}
{"type":"audio","index":1,"text":"Detailnya ada di layar.","ms":744,"audio":"<base64 MP3>"}
{"type":"done","chunks":2,"total_ms":1610}
```

- `meta` leaves as soon as the text is split; it carries no audio.
- Each `audio.audio` is a complete MP3 chunk, base64-encoded (no data URL).
  Concatenate the decoded bytes in `index` order to get the full summary.
- A sentence that fails to synthesise emits `{"type":"error","index":N,...}` and
  the stream continues with the next sentence.
- `done` is the last line. `chunks` is the number of audio chunks actually
  produced; it can be smaller than `sentences` after an engine failure.
- `X-Iris-Spoken-Text` is also set for callers that only want to know what was
  spoken.

`POST /speak` is unchanged and still buffers the whole summary; it remains the
fallback for clients that cannot consume a stream.

## Run it

Use the existing voice-HUD virtualenv. Do not create another one and do not
install into the system Python:

```bash
IRIS_VOICE_PYTHON="${IRIS_VOICE_PYTHON:-$HOME/Documents/projects/voice-hud/bridge/.venv/bin/python}"

"$IRIS_VOICE_PYTHON" voice/server.py
```

Defaults:

| Variable | Default |
|---|---|
| `IRIS_VOICE_HOST` | `127.0.0.1` |
| `IRIS_VOICE_PORT` | `3310` |
| `IRIS_STT_MODEL` | `small` |
| `IRIS_TTS_VOICE` | `id-ID-GadisNeural` |
| `IRIS_TTS_RATE` | `-8%` |
| `IRIS_FFMPEG` | `~/.hermes/tools/ffmpeg-9.0.1-linux-x64/bin/ffmpeg` |

Then start irisd with the same URL:

```bash
IRIS_VOICE_URL=http://127.0.0.1:3310 irisd
```

`irisd` proxies the three endpoints behind its cookie auth:

```
POST /api/voice/transcribe   multipart field "audio" -> {text, reason?}
POST /api/voice/speak        {"text": "..."} -> audio/mpeg
POST /api/voice/speak/stream {"text": "..."} -> application/x-ndjson
```

The stream proxy flushes every line (`http.Flusher`), so sentence 1's audio
reaches the browser while sentence 2 is still being synthesised.

If the sidecar is down both answer **503** with
`suara sedang tidak tersedia — coba lagi sebentar.`, not a stack trace.

## Warmth

At boot the sidecar loads the Whisper model and runs one throwaway synthesis.
Both timings are logged. A cold first turn that takes four seconds reads as a
hang, so the cost is paid before the first real request. STT runs one request at
a time (the CTranslate2 model is not thread-safe); TTS requests may overlap.

## Tests

```bash
"$IRIS_VOICE_PYTHON" -m unittest discover -s voice/tests -t . -v
```

The suite includes the sanitiser rules and a real round trip: synthesise
`Selesai, port delapan ribu kosong.`, transcribe the MP3, expect `selesai` and
`port`.

## The microphone needs HTTPS

Browsers only expose `getUserMedia` in a **secure context**. `http://localhost`
is one; `http://192.168.x.x` (a LAN IP) is **not**. On a phone, open Iris over
the Tailscale HTTPS hostname, not the LAN IP, or the mic button will always
report permission failure. This is a browser rule, not an Iris setting.
