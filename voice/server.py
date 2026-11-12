#!/usr/bin/env python3
"""Iris voice sidecar — loopback STT and free TTS.

    POST /transcribe   raw audio bytes (webm/opus, ogg, wav) -> {text, ms}
    POST /speak        {"text": "..."} -> audio/mpeg + X-Iris-Spoken-Text
    GET  /health       {ok, model, voice, warm}

Run it with the existing voice-HUD virtualenv (already has faster-whisper,
edge-tts, aiohttp and numpy):

    ~/Documents/projects/voice-hud/bridge/.venv/bin/python voice/server.py

It binds 127.0.0.1:3310 only. irisd is the only caller; the browser never talks
to this process. A cold first turn reads as a hang, so the model and one
throwaway synthesis are warmed at boot and the timings are logged.
"""
from __future__ import annotations

import asyncio
import base64
import json
import logging
import os
import shutil
import subprocess
import tempfile
import time
import wave
from pathlib import Path

import edge_tts
import numpy as np
from aiohttp import web
import aiohttp
from faster_whisper import WhisperModel

try:  # package import (tests, `python -m voice.server`)
    from .normalise import normalise_for_speech, split_sentences
except ImportError:  # direct script run (`python voice/server.py`)
    from normalise import normalise_for_speech, split_sentences

HOST = os.environ.get("IRIS_VOICE_HOST", "127.0.0.1")
PORT = int(os.environ.get("IRIS_VOICE_PORT", "3310"))
STT_MODEL = os.environ.get("IRIS_STT_MODEL", "small")
STT_THREADS = int(os.environ.get("IRIS_STT_THREADS", "4"))
VOICE = os.environ.get("IRIS_TTS_VOICE", "id-ID-GadisNeural")
VOICE_RATE = os.environ.get("IRIS_TTS_RATE", "-8%")
VOICE_PITCH = os.environ.get("IRIS_TTS_PITCH", "+0Hz")
# Provider TTS: "edge" (edge-tts, default) atau "engine" (voice-engine lokal,
# bisa cloning suara sendiri). Kontrak /speak tidak berubah untuk keduanya.
PROVIDER = os.environ.get("IRIS_TTS_PROVIDER", "edge").strip().lower()
ENGINE_URL = os.environ.get("IRIS_TTS_URL", "http://127.0.0.1:3320").rstrip("/")
ENGINE_VOICE = os.environ.get("IRIS_TTS_ENGINE_VOICE", "")
ENGINE_KEY = os.environ.get("IRIS_TTS_KEY", "")
ENGINE_SPEED = float(os.environ.get("IRIS_TTS_ENGINE_SPEED", "1.0"))
if PROVIDER not in ("edge", "engine"):
    raise SystemExit(f"IRIS_TTS_PROVIDER tidak dikenal: {PROVIDER!r} (pilih 'edge' atau 'engine')")
SAMPLE_RATE = 16_000
# Vocabulary biasing, not a transcript. Whisper small hears "selesai" as
# "selasai" on this voice without it; domain words are a normal STT hint.
STT_PROMPT = os.environ.get(
    "IRIS_STT_PROMPT", "Selesai. Port delapan ribu. Berkas. Pesan."
)

DEFAULT_FFMPEG = Path.home() / ".hermes" / "tools" / "ffmpeg-9.0.1-linux-x64" / "bin" / "ffmpeg"
FFMPEG = os.environ.get("IRIS_FFMPEG") or (str(DEFAULT_FFMPEG) if DEFAULT_FFMPEG.exists() else shutil.which("ffmpeg"))

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("iris-voice")


def _sniff_container(data: bytes) -> str:
    """Identify the container from magic bytes, never from a client hint."""
    if data[:4] == b"\x1aE\xdf\xa3":
        return ".webm"
    if len(data) >= 12 and data[4:8] == b"ftyp":
        return ".mp4"
    if data[:4] == b"OggS":
        return ".ogg"
    if data[:4] == b"RIFF":
        return ".wav"
    if data[:3] == b"ID3" or data[:2] in (b"\xff\xfb", b"\xff\xf3", b"\xff\xf2"):
        return ".mp3"
    return ".bin"


def _read_wav(path: str) -> np.ndarray:
    with wave.open(path, "rb") as handle:
        frames = handle.readframes(handle.getnframes())
        channels = handle.getnchannels()
    samples = np.frombuffer(frames, dtype=np.int16).astype(np.float32) / 32768.0
    if channels > 1:
        samples = samples.reshape(-1, channels).mean(axis=1)
    return samples


class Engines:
    """STT + TTS, loaded once and kept warm."""

    def __init__(self) -> None:
        self.model: WhisperModel | None = None
        self.tts_warm = False
        self.stt_lock = asyncio.Lock()
        self.model_load_ms: float | None = None
        self.tts_warm_ms: float | None = None

    @property
    def ready(self) -> bool:
        return self.model is not None and self.tts_warm

    async def warm(self) -> None:
        if self.model is None:
            started = time.perf_counter()
            self.model = await asyncio.to_thread(
                WhisperModel,
                STT_MODEL,
                device="cpu",
                compute_type="int8",
                cpu_threads=STT_THREADS,
            )
            self.model_load_ms = (time.perf_counter() - started) * 1000
            log.info("stt model %s loaded in %.0f ms", STT_MODEL, self.model_load_ms)

        if not self.tts_warm:
            started = time.perf_counter()
            if PROVIDER == "engine":
                # voice-engine sudah memuat modelnya sendiri; cukup pastikan hidup.
                async with aiohttp.ClientSession(timeout=aiohttp.ClientTimeout(total=5)) as session:
                    async with session.get(ENGINE_URL + "/healthz") as res:
                        if res.status != 200:
                            raise RuntimeError(f"voice-engine healthz {res.status}")
                        info = await res.json()
                if not info.get("ready"):
                    log.warning("voice-engine menjawab tapi belum ready: %s", info.get("backends"))
            else:
                # edge-tts pays a WebSocket handshake on the first call. Burn it at
                # boot, never on Boss's first question.
                async for _ in edge_tts.Communicate("siap", VOICE, rate=VOICE_RATE, pitch=VOICE_PITCH).stream():
                    pass
            self.tts_warm = True
            self.tts_warm_ms = (time.perf_counter() - started) * 1000
            log.info("tts warmed in %.0f ms (provider=%s voice=%s)", self.tts_warm_ms,
                     PROVIDER, VOICE if PROVIDER == "edge" else (ENGINE_VOICE or "-"))

    async def transcribe(self, audio: bytes, suffix: str) -> tuple[str, float]:
        """Return (text, ms). Empty/no-speech is a normal 200, not an error."""
        if self.model is None:
            await self.warm()
        assert self.model is not None

        started = time.perf_counter()
        wav_path = ""
        with tempfile.NamedTemporaryFile(suffix=suffix, delete=True) as source:
            source.write(audio)
            source.flush()
            wav_path = source.name + ".16k.wav"
            try:
                # faster-whisper's own PyAV decoder is fragile on new Python.
                # Decode with ffmpeg and hand the model a float32 array.
                await asyncio.to_thread(
                    subprocess.run,
                    [
                        FFMPEG, "-hide_banner", "-loglevel", "error", "-y",
                        "-i", source.name, "-ar", str(SAMPLE_RATE), "-ac", "1",
                        "-f", "wav", wav_path,
                    ],
                    check=True,
                )
                samples = await asyncio.to_thread(_read_wav, wav_path)
            finally:
                Path(wav_path).unlink(missing_ok=True)

        if samples.size < int(SAMPLE_RATE * 0.15):
            return "", (time.perf_counter() - started) * 1000

        def run() -> str:
            segments, _info = self.model.transcribe(
                samples,
                language="id",
                beam_size=1,
                vad_filter=True,
                initial_prompt=STT_PROMPT,
            )
            return " ".join(segment.text.strip() for segment in segments).strip()

        # One request at a time: the CTranslate2 model is not thread-safe.
        async with self.stt_lock:
            text = await asyncio.to_thread(run)
        return text, (time.perf_counter() - started) * 1000

    async def speak(self, text: str) -> tuple[bytes, str, float]:
        spoken = normalise_for_speech(text)
        if not spoken:
            return b"", "", 0.0
        audio, synth_ms = await self.synthesise(spoken)
        return audio, spoken, synth_ms

    async def synthesise(self, spoken: str) -> tuple[bytes, float]:
        """Synthesise text that has ALREADY been normalised."""
        if not self.tts_warm:
            await self.warm()
        started = time.perf_counter()
        if PROVIDER == "engine":
            payload: dict[str, object] = {"input": spoken, "response_format": "mp3"}
            if ENGINE_VOICE:
                payload["voice"] = ENGINE_VOICE
            if ENGINE_SPEED and ENGINE_SPEED != 1.0:
                payload["speed"] = ENGINE_SPEED
            headers = {"X-API-Key": ENGINE_KEY} if ENGINE_KEY else {}
            timeout = aiohttp.ClientTimeout(total=180)
            async with aiohttp.ClientSession(timeout=timeout) as session:
                async with session.post(ENGINE_URL + "/v1/audio/speech", json=payload, headers=headers) as res:
                    body = await res.read()
                    if res.status != 200:
                        raise RuntimeError(f"voice-engine {res.status}: {body[:200]!r}")
                    if not body:
                        raise RuntimeError("voice-engine mengembalikan audio kosong")
            return body, (time.perf_counter() - started) * 1000
        buffer = bytearray()
        async for chunk in edge_tts.Communicate(
            spoken, VOICE, rate=VOICE_RATE, pitch=VOICE_PITCH
        ).stream():
            if chunk["type"] == "audio":
                buffer.extend(chunk["data"])
        return bytes(buffer), (time.perf_counter() - started) * 1000


engines = Engines()


def _header_safe(value: str) -> str:
    """HTTP headers are latin-1. Indonesian copy is ASCII; be safe anyway."""
    return value.encode("latin-1", "replace").decode("latin-1")


async def health(_request: web.Request) -> web.Response:
    return web.json_response({
        "ok": True,
        "model": STT_MODEL,
        "provider": PROVIDER,
        "voice": VOICE if PROVIDER == "edge" else (ENGINE_VOICE or "engine-default"),
        "warm": engines.ready,
    })


async def transcribe(request: web.Request) -> web.Response:
    audio = await request.read()
    suffix = _sniff_container(audio)

    if not audio:
        return web.json_response({"text": "", "ms": 0, "reason": "no_speech"})

    try:
        text, elapsed_ms = await engines.transcribe(audio, suffix)
    except Exception as exc:  # a tap that recorded nothing is normal
        log.warning("stt: %d bytes (%s) could not be decoded: %s", len(audio), suffix, exc)
        return web.json_response({
            "text": "", "ms": 0, "reason": "no_speech",
        })

    if not text:
        log.info("stt %.0f ms (%s, %.1f KB) -> no speech", elapsed_ms, suffix, len(audio) / 1024)
        return web.json_response({
            "text": "", "ms": round(elapsed_ms, 1), "reason": "no_speech",
        })

    log.info("stt %.0f ms (%s, %.1f KB) -> %r", elapsed_ms, suffix, len(audio) / 1024, text[:80])
    return web.json_response({"text": text, "ms": round(elapsed_ms, 1)})


async def speak_stream(request: web.Request) -> web.StreamResponse:
    """Stream speech as newline-delimited JSON, one audio chunk per sentence.

    Protocol (documented in voice/README.md):
        {"type":"meta","spoken":"...","voice":"...","sentences":2}
        {"type":"audio","index":0,"text":"...","ms":640,"audio":"<base64 mp3>"}
        {"type":"error","index":1,"message":"..."}
        {"type":"done","chunks":2,"total_ms":1450}
    Each line ends with \\n. The first audio line is written as soon as
    sentence 1 is synthesised; the first bytes leave before the last sentence
    exists.
    """
    try:
        payload = await request.json()
    except json.JSONDecodeError:
        return web.json_response({"error": "body must be JSON"}, status=400)

    text = str(payload.get("text") or "").strip()
    if not text:
        return web.json_response({"error": "text is required"}, status=400)

    started = time.perf_counter()
    spoken = normalise_for_speech(text)
    response = web.StreamResponse(status=200)
    response.headers["Content-Type"] = "application/x-ndjson"
    response.headers["Cache-Control"] = "no-store"
    response.headers["X-Accel-Buffering"] = "no"
    response.headers["X-Iris-Spoken-Text"] = _header_safe(spoken)
    await response.prepare(request)

    async def emit(payload_obj: dict) -> None:
        await response.write((json.dumps(payload_obj, ensure_ascii=False) + "\n").encode("utf-8"))

    try:
        if not spoken:
            log.info("tts stream: nothing speakable in %d chars", len(text))
            await emit({"type": "done", "chunks": 0,
                        "total_ms": round((time.perf_counter() - started) * 1000, 1)})
            await response.write_eof()
            return response

        sentences = split_sentences(spoken) or [spoken]
        await emit({
            "type": "meta",
            "spoken": spoken,
            "voice": VOICE if PROVIDER == "edge" else (ENGINE_VOICE or "engine-default"),
            "sentences": len(sentences),
        })

        chunks = 0
        for index, sentence in enumerate(sentences):
            try:
                audio, synth_ms = await engines.synthesise(sentence)
            except Exception as exc:  # one bad chunk must not silence the rest
                log.warning("tts stream chunk %d failed: %s", index, exc)
                await emit({"type": "error", "index": index, "message": "speech engine failed"})
                continue
            await emit({
                "type": "audio",
                "index": index,
                "text": sentence,
                "ms": round(synth_ms, 1),
                "audio": base64.b64encode(audio).decode("ascii"),
            })
            chunks += 1

        total_ms = (time.perf_counter() - started) * 1000
        await emit({"type": "done", "chunks": chunks, "total_ms": round(total_ms, 1)})
        await response.write_eof()
        log.info("tts stream %.0f ms for %d chars -> %d chunk(s) %r", total_ms, len(text), chunks, spoken[:80])
        return response
    except (ConnectionResetError, asyncio.CancelledError):
        # Barge-in: the browser stopped reading. That is a normal cancellation,
        # not a server error.
        log.info("tts stream cancelled by the client")
        return response


async def speak(request: web.Request) -> web.Response:
    try:
        payload = await request.json()
    except json.JSONDecodeError:
        return web.json_response({"error": "body must be JSON"}, status=400)

    text = str(payload.get("text") or "").strip()
    if not text:
        return web.json_response({"error": "text is required"}, status=400)

    started = time.perf_counter()
    try:
        audio, spoken, synth_ms = await engines.speak(text)
    except Exception as exc:
        log.exception("tts failed: %s", exc)
        return web.json_response({"error": "speech engine failed"}, status=502)

    total_ms = (time.perf_counter() - started) * 1000
    if not spoken:
        log.info("tts: nothing speakable in %d chars", len(text))
        return web.Response(
            body=b"",
            content_type="audio/mpeg",
            headers={"X-Iris-Spoken-Text": ""},
        )

    log.info(
        "tts %.0f ms (%.0f ms in engine) for %d chars -> %r",
        total_ms, synth_ms, len(text), spoken[:120],
    )
    return web.Response(
        body=audio,
        content_type="audio/mpeg",
        headers={
            "X-Iris-Spoken-Text": _header_safe(spoken),
            "X-Iris-Synth-Ms": f"{synth_ms:.0f}",
        },
    )


def create_app() -> web.Application:
    app = web.Application(client_max_size=64 * 1024 * 1024)
    app.router.add_get("/health", health)
    app.router.add_post("/transcribe", transcribe)
    app.router.add_post("/speak", speak)
    app.router.add_post("/speak/stream", speak_stream)
    return app


async def _boot(app: web.Application) -> None:
    started = time.perf_counter()
    try:
        await engines.warm()
    except Exception:
        log.exception("warm-up failed; the first request will pay the cost")
    app["boot_ms"] = (time.perf_counter() - started) * 1000
    log.info(
        "warm-up finished in %.0f ms (model_load=%.0f ms, tts_warm=%.0f ms)",
        app["boot_ms"], engines.model_load_ms or 0, engines.tts_warm_ms or 0,
    )


def main() -> None:
    logging.getLogger("aiohttp").setLevel(logging.WARNING)
    if not FFMPEG:
        raise SystemExit("ffmpeg not found; set IRIS_FFMPEG to an ffmpeg binary")
    app = create_app()
    app.on_startup.append(_boot)
    log.info("iris-voice on http://%s:%d (model=%s, voice=%s)", HOST, PORT, STT_MODEL, VOICE)
    web.run_app(app, host=HOST, port=PORT, print=None, shutdown_timeout=5)


if __name__ == "__main__":
    main()
