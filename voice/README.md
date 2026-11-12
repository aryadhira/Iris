# Iris voice sidecar

Local speech engines behind a loopback HTTP service. `irisd` is the only caller;
the browser never talks to this process.

```
POST /transcribe   raw audio bytes (webm/opus, ogg, wav, mp3) -> {text, ms}
POST /speak        {"text": "..."} -> audio/mpeg + X-Iris-Spoken-Text
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

`irisd` proxies the two endpoints behind its cookie auth:

```
POST /api/voice/transcribe   multipart field "audio" -> {text, reason?}
POST /api/voice/speak        {"text": "..."} -> audio/mpeg
```

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
