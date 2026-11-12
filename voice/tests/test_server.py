"""Sidecar tests: real edge-tts, real faster-whisper, no fake audio.

Run from the repository root with the voice-HUD virtualenv:

    ~/Documents/projects/voice-hud/bridge/.venv/bin/python -m unittest discover -s voice/tests -t . -v
"""
from __future__ import annotations

import unittest

from aiohttp.test_utils import TestClient, TestServer

from voice.server import create_app


class VoiceServerTest(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self) -> None:
        self.client = TestClient(TestServer(create_app()))
        await self.client.start_server()

    async def asyncTearDown(self) -> None:
        await self.client.close()

    async def test_health(self):
        response = await self.client.get("/health")
        self.assertEqual(response.status, 200)
        body = await response.json()
        self.assertTrue(body["ok"])
        self.assertEqual(body["model"], "small")
        self.assertEqual(body["voice"], "id-ID-GadisNeural")
        self.assertTrue(body["warm"])

    async def test_empty_audio_is_no_speech_not_a_500(self):
        response = await self.client.post("/transcribe", data=b"")
        self.assertEqual(response.status, 200)
        body = await response.json()
        self.assertEqual(body["text"], "")
        self.assertEqual(body["reason"], "no_speech")

    async def test_garbage_audio_is_no_speech_not_a_500(self):
        response = await self.client.post("/transcribe", data=b"not audio at all" * 300)
        self.assertEqual(response.status, 200)
        body = await response.json()
        self.assertEqual(body["text"], "")
        self.assertEqual(body["reason"], "no_speech")

    async def test_speak_returns_audio_and_a_markdown_free_header(self):
        markdown = (
            "## Ringkasan\n\n**PHK** naik 12% karena [AI](https://x.test/a).\n\n"
            "- biaya Rp50.000\n"
            "- ukuran 16 GB\n\n"
            "| kolom | nilai |\n| --- | --- |\n| a | 1 |\n\n"
            "```python\nprint('rahasia')\n```\n"
        )
        response = await self.client.post("/speak", json={"text": markdown})
        self.assertEqual(response.status, 200)
        self.assertEqual(response.headers["Content-Type"], "audio/mpeg")
        audio = await response.read()
        self.assertGreater(len(audio), 2000, "edge-tts returned no real audio")

        spoken = response.headers["X-Iris-Spoken-Text"]
        self.assertTrue(spoken.strip(), "the header must name what was spoken")
        for banned in ("*", "#", "`", "|", "http", "print"):
            self.assertNotIn(banned, spoken, spoken)

    async def test_speak_rejects_empty_text(self):
        response = await self.client.post("/speak", json={"text": "   "})
        self.assertEqual(response.status, 400)

    async def test_round_trip_speech_to_transcript(self):
        phrase = "Selesai, port delapan ribu kosong."
        spoken = await self.client.post("/speak", json={"text": phrase})
        self.assertEqual(spoken.status, 200)
        audio = await spoken.read()
        self.assertGreater(len(audio), 2000)

        # The sidecar sniffs the container; edge-tts returns MP3.
        result = await self.client.post(
            "/transcribe", data=audio, headers={"Content-Type": "audio/mpeg"}
        )
        self.assertEqual(result.status, 200)
        body = await result.json()
        text = body["text"].lower()
        self.assertIn("selesai", text)
        self.assertIn("port", text)


if __name__ == "__main__":
    unittest.main(verbosity=2)
