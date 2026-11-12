#!/usr/bin/env python3
"""Measure the streaming speech path against the buffered one, and prove the
concatenated audio is still intelligible.

Stdlib only. Invoked by scripts/p8-acceptance.sh with:
  IRIS_BASE       http://127.0.0.1:3309
  IRIS_COOKIE     iris_session=<value>
  IRIS_PROBE_TEXT two-sentence Indonesian summary

Prints one JSON object. Exits non-zero if the first AUDIO byte takes 1200 ms or
more, or if the transcript loses the words that were spoken.
"""
from __future__ import annotations

import base64
import json
import os
import sys
import time
import urllib.request

BASE = os.environ["IRIS_BASE"].rstrip("/")
COOKIE = os.environ["IRIS_COOKIE"]
TEXT = os.environ.get(
    "IRIS_PROBE_TEXT",
    "Selesai, port delapan ribu kosong. Server lama sudah dimatikan.",
)


def post(path: str, data: bytes, content_type: str, accept: str = "*/*"):
    request = urllib.request.Request(
        BASE + path,
        data=data,
        headers={"Content-Type": content_type, "Cookie": COOKIE, "Accept": accept},
    )
    return urllib.request.urlopen(request, timeout=240)


def transcribe(audio: bytes) -> str:
    boundary = "----iris-p8-boundary"
    body = (
        f"--{boundary}\r\n"
        'Content-Disposition: form-data; name="audio"; filename="speech.mp3"\r\n'
        "Content-Type: audio/mpeg\r\n\r\n"
    ).encode() + audio + f"\r\n--{boundary}--\r\n".encode()
    response = post("/api/voice/transcribe", body, f"multipart/form-data; boundary={boundary}")
    return str(json.loads(response.read().decode()).get("text", "")).strip()


def main() -> int:
    payload = json.dumps({"text": TEXT}).encode()

    # 1. Buffered path. Its first body byte and its last body byte are the same
    #    instant for a short answer, which is exactly the wait Boss feels.
    started = time.perf_counter()
    buffered = post("/api/voice/speak", payload, "application/json")
    first_byte = buffered.read(1)
    buffered_first_ms = (time.perf_counter() - started) * 1000
    buffered_audio = first_byte + buffered.read()
    buffered_total_ms = (time.perf_counter() - started) * 1000

    # 2. Streaming path. TTFB is the first AUDIO frame, not the meta line.
    started = time.perf_counter()
    stream = post("/api/voice/speak/stream", payload, "application/json", "application/x-ndjson")
    first_audio_ms = None
    done_ms = None
    chunks: list[bytes] = []
    sentences: list[str] = []
    while True:
        line = stream.readline()
        if not line:
            break
        try:
            frame = json.loads(line)
        except json.JSONDecodeError:
            continue
        if frame.get("type") == "audio":
            if first_audio_ms is None:
                first_audio_ms = (time.perf_counter() - started) * 1000
            chunks.append(base64.b64decode(frame["audio"]))
            sentences.append(frame.get("text", ""))
        elif frame.get("type") == "done":
            done_ms = (time.perf_counter() - started) * 1000
    stream_total_ms = (time.perf_counter() - started) * 1000

    stream_audio = b"".join(chunks)
    transcript = transcribe(stream_audio)
    buffered_transcript = transcribe(buffered_audio)

    result = {
        "text": TEXT,
        "non_streaming_first_byte_ms": round(buffered_first_ms, 1),
        "non_streaming_total_ms": round(buffered_total_ms, 1),
        "non_streaming_bytes": len(buffered_audio),
        "streaming_first_audio_ms": None if first_audio_ms is None else round(first_audio_ms, 1),
        "streaming_total_ms": round(stream_total_ms, 1),
        "streaming_done_ms": None if done_ms is None else round(done_ms, 1),
        "streaming_chunks": len(chunks),
        "streaming_sentences": sentences,
        "streaming_bytes": len(stream_audio),
        "streaming_transcript": transcript,
        "buffered_transcript": buffered_transcript,
    }

    problems = []
    if first_audio_ms is None:
        problems.append("the stream produced no audio frame")
    elif first_audio_ms >= 1200:
        problems.append(f"first audio byte took {first_audio_ms:.0f} ms (want < 1200)")
    if len(chunks) < 2:
        problems.append(f"only {len(chunks)} audio chunk(s); the stream was not sentence-split")
    lowered = transcript.lower()
    for word in ("selesai", "port", "kosong"):
        if word not in lowered:
            problems.append(f"transcript lost {word!r}: {transcript!r}")

    print(json.dumps({**result, "problems": problems}, ensure_ascii=False))
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
