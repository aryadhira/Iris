"""The sanitiser is the part that decides whether speech is worth hearing.

Run from the repository root:

    ~/Documents/projects/voice-hud/bridge/.venv/bin/python -m unittest discover -s voice/tests -t . -v
"""
from __future__ import annotations

import unittest

from voice.normalise import normalise_for_speech, number_to_words, split_sentences


class TestNumbers(unittest.TestCase):
    def test_cardinals(self):
        cases = {
            0: "nol", 1: "satu", 11: "sebelas", 19: "sembilan belas",
            20: "dua puluh", 21: "dua puluh satu",
            100: "seratus", 101: "seratus satu",
            1000: "seribu", 1500: "seribu lima ratus",
            2000: "dua ribu", 1_000_000: "satu juta",
        }
        for n, want in cases.items():
            self.assertEqual(number_to_words(n), want, f"number_to_words({n})")

    def test_thousands_separator(self):
        self.assertIn("seribu enam ratus enam", normalise_for_speech("kredit Rp1.606 triliun"))

    def test_decimal_comma(self):
        self.assertIn("dua koma lima", normalise_for_speech("tumbuh 2,5 persen"))

    def test_long_digit_strings_read_digit_by_digit(self):
        out = normalise_for_speech("NIK 3273012345678901")
        self.assertIn("tiga dua tujuh", out)


class TestMarkdown(unittest.TestCase):
    def test_markdown_only_input_has_no_markup(self):
        raw = "# **Judul**\n\n**teks** `kode` | kolom |\n| --- | --- |"
        out = normalise_for_speech(raw)
        for banned in ("*", "#", "`", "|"):
            self.assertNotIn(banned, out)

    def test_bold_and_italic_are_removed(self):
        self.assertEqual(normalise_for_speech("**PHK** besar"), "pemutusan hubungan kerja besar")

    def test_code_fence_is_dropped(self):
        out = normalise_for_speech("Ini contoh:\n```python\nprint(1)\n```\nSelesai.")
        self.assertNotIn("print", out)
        self.assertIn("Selesai", out)
        self.assertNotIn("```", out)

    def test_unfinished_code_fence_is_dropped(self):
        out = normalise_for_speech("Contoh:\n```python\nprint(1)")
        self.assertNotIn("print", out)
        self.assertNotIn("```", out)

    def test_tables_are_dropped(self):
        raw = "Hasil:\n| nama | nilai |\n| --- | --- |\n| port | 8000 |\nSelesai."
        out = normalise_for_speech(raw)
        self.assertNotIn("|", out)
        self.assertNotIn("nama", out)
        self.assertIn("Selesai", out)

    def test_links_become_their_text(self):
        self.assertIn("lihat berita", normalise_for_speech("Buka [lihat berita](https://x.test/a)"))

    def test_bare_urls_are_dropped(self):
        out = normalise_for_speech("Detail di https://example.com/panjang/sekali")
        self.assertNotIn("http", out)
        self.assertNotIn("example", out)

    def test_file_paths_are_dropped(self):
        out = normalise_for_speech("Berkasnya di /home/arya/iris-data/rooms/abc/outbox/laporan.md ya")
        self.assertNotIn("/home", out)
        self.assertNotIn("laporan.md", out)
        self.assertIn("ya", out)

    def test_bullets_become_sentences(self):
        out = normalise_for_speech("- satu\n- dua\n- tiga")
        self.assertNotIn("-", out)
        self.assertTrue(out.startswith("satu."), out)
        self.assertIn("tiga", out)
        self.assertLessEqual(out.count("."), 2, out)


class TestSummary(unittest.TestCase):
    def test_six_bullets_become_at_most_two_sentences(self):
        raw = "\n".join(
            f"- Langkah {i} selesai dengan hasil yang berbeda-beda"
            for i in range(1, 7)
        )
        out = normalise_for_speech(raw)
        self.assertLessEqual(out.count("."), 2, out)
        self.assertIn("detailnya ada di layar.", out)

    def test_long_answer_keeps_first_and_outcome(self):
        raw = (
            "Port delapan ribu ternyata sudah dipakai proses lain. "
            "Aku memeriksa pemakai port itu dan menemukan server lama. "
            "Server lama sudah dimatikan, jadi port delapan ribu sekarang kosong."
        )
        out = normalise_for_speech(raw)
        self.assertTrue(out.startswith("Port delapan ribu"), out)
        self.assertIn("kosong", out)
        self.assertIn("detailnya ada di layar.", out)
        self.assertLessEqual(out.count("."), 2, out)

    def test_code_note_appears_at_most_once(self):
        raw = "Ini kodenya:\n```py\nprint(1)\n```\nSudah jalan."
        out = normalise_for_speech(raw)
        self.assertLessEqual(out.count("kodenya ada di layar"), 1, out)

    def test_normal_sentence_passes_through_unchanged(self):
        self.assertEqual(
            normalise_for_speech("Selesai, port delapan ribu kosong."),
            "Selesai, port delapan ribu kosong.",
        )

    def test_empty(self):
        self.assertEqual(normalise_for_speech(""), "")
        self.assertEqual(normalise_for_speech("   "), "")


class TestExpansions(unittest.TestCase):
    def test_currency(self):
        out = normalise_for_speech("biaya Rp50.000")
        self.assertIn("rupiah", out)
        self.assertIn("lima puluh ribu", out)

    def test_percent(self):
        self.assertIn("dua belas persen", normalise_for_speech("naik 12%"))

    def test_units(self):
        out = normalise_for_speech("ukuran 16 GB dan 512 MB")
        self.assertIn("enam belas gigabita", out)
        self.assertIn("lima ratus dua belas megabita", out)

    def test_arrow(self):
        self.assertIn("menjadi", normalise_for_speech("sebelum → sesudah"))

    def test_abbreviations(self):
        self.assertIn("pemutusan hubungan kerja", normalise_for_speech("PHK massal"))
        self.assertIn("kecerdasan buatan", normalise_for_speech("pakai AI"))

    def test_abbreviation_not_applied_inside_words(self):
        self.assertNotIn("ei ai", normalise_for_speech("anak itu main di pantai"))


class TestSecrets(unittest.TestCase):
    def test_openai_style_key_is_redacted(self):
        out = normalise_for_speech("Kuncinya sk-proj-AbCdEf1234567890 jangan dibagikan")
        self.assertNotIn("sk-proj", out)
        self.assertNotIn("AbCdEf1234567890", out)
        self.assertIn("kunci rahasia", out)

    def test_bot_token_is_redacted(self):
        out = normalise_for_speech("token 123456:AAHfakeTokenValue12345 simpan")
        self.assertNotIn("AAHfakeTokenValue12345", out)
        self.assertIn("kunci rahasia", out)

    def test_long_hex_is_redacted(self):
        out = normalise_for_speech("sha256 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08")
        self.assertNotIn("9f86d081", out)
        self.assertIn("kunci rahasia", out)


if __name__ == "__main__":
    unittest.main(verbosity=2)


class TestHardCap(unittest.TestCase):
    """A long answer must stay speakable: capped, at a word boundary, with the screen clause."""

    def test_long_answer_is_capped_at_a_word_boundary(self):
        text = ("Selesai. Port 8080 kosong dan tiga tes lulus. "
                + "Ada satu catatan penting soal AI dan ukuran file 16 GB yang harus kamu tahu sebelum "
                  "lanjut ke langkah berikutnya karena ini akan memakan waktu lama dan biaya besar kalau "
                  "salah, dan masih ada beberapa hal lain yang perlu dipertimbangkan dengan hati-hati. ")
        out = normalise_for_speech(text)
        self.assertLessEqual(len(out), 220, out)
        self.assertIn("Detailnya ada di layar.", out)
        self.assertTrue(out.endswith("."))
        # never cut mid-word: the character before the final clause must end a word
        head = out.replace("Detailnya ada di layar.", "").strip()
        self.assertFalse(head.endswith("-"), out)
        self.assertNotRegex(head, r"[a-z]{1}$", out)

    def test_short_answer_is_left_alone(self):
        out = normalise_for_speech("Selesai. Port 8080 kosong.")
        # numbers are expanded on purpose (that is the point of the sanitiser)
        self.assertEqual(out, "Selesai. Port delapan ribu delapan puluh kosong.")


class TestSplitSentences(unittest.TestCase):
    def test_two_sentences_become_two_chunks(self):
        self.assertEqual(
            split_sentences("Selesai. Port delapan ribu kosong."),
            ["Selesai.", "Port delapan ribu kosong."],
        )

    def test_no_empty_chunks(self):
        self.assertEqual(split_sentences("  Satu.   Dua.  "), ["Satu.", "Dua."])
        self.assertEqual(split_sentences(""), [])
