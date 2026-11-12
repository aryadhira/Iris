"""Speech normalisation for Iris.

The markdown/expansion rules are copied from
``~/Documents/projects/voice-hud/bridge/normalise.py`` (the tested normaliser
written for the voice HUD) and extended for Iris: tables are dropped, secrets
are redacted, and the result is cut to the two-sentence summary ADR-005 asks
for. Nothing is imported across projects; this file is the Iris copy.

Speech is a summary, never the formatted reply. The screen keeps the full text.
Everything here is rule-based on purpose: a prompt cannot guarantee that a
token never reaches a synthesiser, a regex can.
"""
from __future__ import annotations

import re

# ---------------------------------------------------------------- reply cleaner
#
# This is the Python mirror of internal/reply in irisd. Both implementations
# read the same files in testdata/replies/ and scripts/check-cleaner-parity.sh
# fails if one byte differs. Keep the rules in the same order as the Go file.

_STATUS_LINE = re.compile(
    r"^[ \t>*_`#]*STATUS:[ \t]*(?:RUNNING|DONE|BLOCKED|CONTINUE)\b[^\n]*(?:\r?\n)?(?:\r?\n)?",
    re.IGNORECASE | re.MULTILINE,
)
_ARTIFACT_LINE = re.compile(
    r"^[ \t]*\[\[\s*artifact\s*:\s*(.+?)\s*\]\][ \t]*\r?\n?", re.MULTILINE
)
_RESIDUAL_STATUS = re.compile(
    r"STATUS:\s*(?:RUNNING|DONE|BLOCKED|CONTINUE)\b[^\n]*", re.IGNORECASE
)
_TAGGED_STATUS = re.compile(
    r"(?:<mark>)?STATUS(?:</mark>)?\s*:\s*(?:<mark>)?"
    r"(?:RUNNING|DONE|BLOCKED|CONTINUE)\b[^\n]*",
    re.IGNORECASE,
)


def _sentinel(index: int) -> str:
    return f"\x00R{index}\x01"


def _opening_fence(line: str):
    trimmed = line.lstrip(" ")
    if len(line) - len(trimmed) > 3 or len(trimmed) < 3:
        return None
    char = trimmed[0]
    if char not in "`~":
        return None
    width = 0
    while width < len(trimmed) and trimmed[width] == char:
        width += 1
    return (char, width) if width >= 3 else None


def _closing_fence(line: str, char: str, width: int) -> bool:
    trimmed = line.lstrip(" ")
    if len(line) - len(trimmed) > 3 or len(trimmed) < width:
        return False
    if trimmed[:width] != char * width:
        return False
    return trimmed[width:].strip() == ""


def _mask_inline(line: str, base: int):
    out: list[str] = []
    spans: list[str] = []
    i = 0
    while i < len(line):
        if line[i] != "`":
            out.append(line[i])
            i += 1
            continue
        run = 0
        while i + run < len(line) and line[i + run] == "`":
            run += 1
        close_at = _find_backtick_run(line, i + run, run)
        if close_at < 0:
            out.append(line[i : i + run])
            i += run
            continue
        end = close_at + run
        out.append(_sentinel(base + len(spans)))
        spans.append(line[i:end])
        i = end
    return "".join(out), spans


def _find_backtick_run(text: str, start: int, width: int) -> int:
    i = start
    while i < len(text):
        if text[i] != "`":
            i += 1
            continue
        run = 0
        while i + run < len(text) and text[i + run] == "`":
            run += 1
        if run == width:
            return i
        i += run
    return -1


def _mask_code(text: str):
    lines = text.splitlines(keepends=True)
    if not lines:
        return text, []
    out: list[str] = []
    spans: list[str] = []
    i = 0
    while i < len(lines):
        line = lines[i]
        opening = _opening_fence(line)
        if opening:
            char, width = opening
            end = len(lines) - 1
            for j in range(i + 1, len(lines)):
                if _closing_fence(lines[j], char, width):
                    end = j
                    break
            out.append(_sentinel(len(spans)))
            spans.append("".join(lines[i : end + 1]))
            i = end + 1
            continue
        masked, line_spans = _mask_inline(line, len(spans))
        out.append(masked)
        spans.extend(line_spans)
        i += 1
    return "".join(out), spans


def _is_space(char: str) -> bool:
    return char in " \t\n\r"


def _replace_standalone_dashes(text: str) -> str:
    if "--" not in text:
        return text
    out: list[str] = []
    i = 0
    while i < len(text):
        if (
            text.startswith("--", i)
            and (i == 0 or _is_space(text[i - 1]))
            and (i + 2 == len(text) or _is_space(text[i + 2]))
        ):
            end = i + 2
            while end < len(text) and text[end] == "-":
                end += 1
            if end - i == 2:
                out.append("—")
                i = end
                continue
        out.append(text[i])
        i += 1
    return "".join(out)


def _is_rule_line(line: str) -> bool:
    stripped = line.rstrip("\r\n").strip()
    return len(stripped) >= 3 and set(stripped) == {"-"}


def _remove_rule_lines(text: str) -> str:
    lines = text.splitlines(keepends=True)
    out: list[str] = []
    i = 0
    while i < len(lines):
        if _is_rule_line(lines[i]):
            if i + 1 < len(lines) and lines[i + 1].strip() == "":
                i += 1
            i += 1
            continue
        out.append(lines[i])
        i += 1
    return "".join(out)


def _clean_reply(text: str, drop_artifacts: bool, trim: bool) -> str:
    if text == "":
        return ""
    masked, spans = _mask_code(text)
    masked = _STATUS_LINE.sub("", masked)
    if drop_artifacts:
        masked = _ARTIFACT_LINE.sub("", masked)
    masked = _replace_standalone_dashes(masked)
    masked = _remove_rule_lines(masked)
    if trim:
        masked = masked.rstrip(" \t\r\n")
    for index, span in enumerate(spans):
        masked = masked.replace(_sentinel(index), span)
    return masked


def clean_reply(text: str) -> str:
    """The visible-text cleaner. Mirrors internal/reply.Clean in Go."""
    return _clean_reply(text, True, True)


def clean_keeping_artifacts(text: str) -> str:
    """Clean without removing markers; persistence resolves those separately."""
    return _clean_reply(text, False, True)


def clean_snippet(text: str) -> str:
    """Clean a search snippet, including a STATUS line cut by the snippet window."""
    out = clean_reply(text)
    out = _RESIDUAL_STATUS.sub("", out)
    out = _TAGGED_STATUS.sub("", out)
    return out.strip()


# --------------------------------------------------------------------- markdown

_FENCE = re.compile(r"```.*?```", re.S)
_UNCLOSED_FENCE = re.compile(r"```.*\Z", re.S)
_INLINE_CODE = re.compile(r"`([^`]*)`")
_LINK = re.compile(r"\[([^\]]+)\]\([^)]*\)")
_URL = re.compile(r"(?:https?://|www\.)\S+", re.I)
_PATH = re.compile(
    r"(?<![\w])(?:~|\.{1,2})?/(?:[\w.@%+\-]+/)+[\w.@%+\-]+"  # /home/a/b, ~/x/y, ./a/b
    r"|\b(?:inbox|outbox)/[\w.@%+\-]+"                       # room-relative paths
    r"|\b[A-Za-z]:\\[^\s]+"                                   # Windows path
)
_TOKEN = re.compile(
    r"\b(?:sk-[A-Za-z0-9_-]{8,}"
    r"|bot\d+:[A-Za-z0-9_-]{10,}"
    r"|\d{5,}:[A-Za-z0-9_-]{10,}"
    r"|gh[pousr]_[A-Za-z0-9_]{10,}"
    r"|github_pat_[A-Za-z0-9_]{10,}"
    r"|xox[baprs]-[A-Za-z0-9-]{10,})\b"
)
_LONG_HEX = re.compile(r"\b[A-Fa-f0-9]{24,}\b")
_EMPHASIS = re.compile(r"\*\*|__|~~|\*|_")
_HEADING = re.compile(r"^\s{0,3}#{1,6}\s*", re.M)
_QUOTE = re.compile(r"^\s{0,3}>\s?", re.M)
_BULLET = re.compile(r"^\s*[-•*–—]\s+")
_NUMBERED = re.compile(r"^\s*\d+[.)]\s+")
_HR = re.compile(r"^\s*([-*_]\s*){3,}$", re.M)
_EMOJI = re.compile(
    "[" "\U0001F300-\U0001FAFF" "\U00002600-\U000027BF"
    "\U0001F1E6-\U0001F1FF" "\U0000FE0F" "\U0000200D" "]",
    flags=re.UNICODE,
)

# ----------------------------------------------------------------- expansions

# Spoken as letters (Indonesian pronunciation of the Latin letters). Copied
# from the voice HUD normaliser; Iris keeps the vocabulary it needs.
_SPELLED = {
    "AI": "kecerdasan buatan", "API": "A P I", "CPU": "C P U", "GPU": "G P U",
    "URL": "tautan", "PDF": "pe de ef", "CSV": "ce es ve",
    "PHK": "pemutusan hubungan kerja", "UMR": "upah minimum regional",
    "UMK": "upah minimum kabupaten", "KPR": "kredit pemilikan rumah",
    "BBM": "bahan bakar minyak", "SPBU": "es pe be u",
    "DPR": "de pe er", "DPRD": "de pe er de", "MK": "em ka",
    "KPK": "ka pe ka", "POLRI": "polri", "TNI": "te en i",
    "BMKG": "be em ka ge", "BNPB": "be en pe be",
    "OJK": "o je ka", "BI": "be i", "BPS": "be pe es",
    "IHSG": "i ha es ge", "UMKM": "u em ka em",
    "CBT": "ce be te", "SaaS": "sas", "MVP": "em ve pe",
    "IT": "i te", "HR": "ha er", "CEO": "si i o", "CTO": "si ti o",
    "SQL": "es ki el", "JSON": "je son", "HTTP": "ha te te pe",
}

_UNITS = [
    "nol", "satu", "dua", "tiga", "empat", "lima", "enam", "tujuh", "delapan",
    "sembilan", "sepuluh", "sebelas",
]


def _below_hundred(n: int) -> str:
    if n < 12:
        return _UNITS[n]
    if n < 20:
        return f"{_UNITS[n - 10]} belas"
    tens, rest = divmod(n, 10)
    out = f"{_UNITS[tens]} puluh"
    if rest:
        out += f" {_UNITS[rest]}"
    return out


def _below_thousand(n: int) -> str:
    if n < 100:
        return _below_hundred(n)
    hundreds, rest = divmod(n, 100)
    head = "seratus" if hundreds == 1 else f"{_UNITS[hundreds]} ratus"
    if rest:
        return f"{head} {_below_hundred(rest)}"
    return head


def number_to_words(n: int) -> str:
    """Indonesian cardinal words. Handles 0 .. 999_999_999_999."""
    if n == 0:
        return "nol"
    if n < 0:
        return f"minus {number_to_words(-n)}"

    parts: list[str] = []
    scales = (
        (10**12, "triliun"),
        (10**9, "miliar"),
        (10**6, "juta"),
        (10**3, "ribu"),
    )
    remainder = n

    for value, name in scales:
        if remainder >= value:
            count, remainder = divmod(remainder, value)
            if value == 10**3 and count == 1:
                parts.append("seribu")
            else:
                parts.append(f"{_below_thousand(count)} {name}")

    if remainder:
        parts.append(_below_thousand(remainder))
    return " ".join(parts)


def _digits_to_words(token: str) -> str:
    """'1.606' -> thousands, '2,5' -> decimal, '2026' -> cardinal."""
    token = token.replace("\u00a0", "")

    # A dot followed by exactly three digits is an Indonesian thousands separator.
    if re.fullmatch(r"\d{1,3}(\.\d{3})+", token):
        return number_to_words(int(token.replace(".", "")))

    # A comma is the Indonesian decimal mark.
    if re.fullmatch(r"\d+,\d+", token):
        whole, frac = token.split(",")
        spoken_frac = " ".join(_UNITS[int(d)] for d in frac)
        return f"{number_to_words(int(whole))} koma {spoken_frac}"

    if token.isdigit():
        # Long digit strings read better digit by digit (phone numbers, NIK).
        if len(token) > 9:
            return " ".join(_UNITS[int(d)] for d in token)
        return number_to_words(int(token))

    return token


_NUMBER = re.compile(r"\d{1,3}(?:\.\d{3})+(?:,\d+)?|\d+(?:,\d+)?")

_CURRENCY = [
    (re.compile(r"\bRp\.?\s*", re.I), "rupiah "),
    (re.compile(r"\bIDR\s*", re.I), "rupiah "),
    (re.compile(r"\bUSD\s*", re.I), "dolar amerika "),
    (re.compile(r"\bSGD\s*", re.I), "dolar singapura "),
]

# Units are spoken as words, not read as initials. Done before the number pass
# so "16 GB" becomes "enam belas gigabita" (not "enam belas ge be").
_UNIT_WORDS = [
    (re.compile(r"\b(TB|GB|MB|KB)\b"), {
        "TB": "terabita", "GB": "gigabita", "MB": "megabita", "KB": "kilobita",
    }),
    (re.compile(r"\b(km|kg|cm|mm)\b", re.I), {
        "km": "kilometer", "kg": "kilogram", "cm": "sentimeter", "mm": "milimeter",
    }),
    (re.compile(r"\bms\b", re.I), {"ms": "milidetik"}),
    (re.compile(r"\bmAh\b"), {"mAh": "miliampere jam"}),
]

_SUFFIX = [
    (re.compile(r"(\d)\s*T\b"), r"\1 triliun"),
    (re.compile(r"(\d)\s*M\b"), r"\1 miliar"),
    (re.compile(r"(\d)\s*(?:Jt|jt)\b"), r"\1 juta"),
    (re.compile(r"(\d)\s*(?:Rb|rb)\b"), r"\1 ribu"),
    (re.compile(r"(\d)\s*(?:K|k)\b"), r"\1 ribu"),
]

_SYMBOLS = [
    (re.compile(r"%"), " persen"),
    (re.compile(r"→|->"), " menjadi "),
    (re.compile(r"\bvs\.?\b", re.I), " versus"),
    (re.compile(r"\bdsb\.?\b", re.I), " dan sebagainya"),
    (re.compile(r"\bdll\.?\b", re.I), " dan lain-lain"),
    (re.compile(r"\bdst\.?\b", re.I), " dan seterusnya"),
    (re.compile(r"\bNo\.\s*"), "nomor "),
    (re.compile(r"\bhal\.\s*"), "halaman "),
    (re.compile(r"&"), " dan "),
    (re.compile(r"\+"), " plus "),
    (re.compile(r"\s*—\s*|\s*–\s*"), ", "),
]

_DETAIL = "detailnya ada di layar."
_DETAIL_SENTENCE = "Detailnya ada di layar."
_CODE_NOTE = "kodenya ada di layar."
_TABLE_NOTE = "tabelnya ada di layar."
_MAX_CHARS = 220
_MAX_SENTENCES = 2


def _is_table_line(line: str) -> bool:
    stripped = line.strip()
    if "|" not in stripped:
        return False
    # A markdown table row starts/ends with a pipe or contains at least two
    # pipes. This also catches the header separator row (|---|---|).
    return stripped.startswith("|") or stripped.endswith("|") or stripped.count("|") >= 2


def _strip_markdown(text: str) -> tuple[str, bool, bool]:
    """Return (prose, dropped_code, dropped_table)."""
    dropped_code = bool(_FENCE.search(text) or _UNCLOSED_FENCE.search(text))
    text = _FENCE.sub(" ", text)
    text = _UNCLOSED_FENCE.sub(" ", text)
    text = _INLINE_CODE.sub(r"\1", text)

    lines: list[str] = []
    dropped_table = False
    for line in text.splitlines():
        if _is_table_line(line):
            dropped_table = True
            continue
        lines.append(line)
    text = "\n".join(lines)

    text = _LINK.sub(r"\1", text)
    text = _URL.sub(" ", text)
    text = _PATH.sub(" ", text)
    text = _TOKEN.sub("kunci rahasia", text)
    text = _LONG_HEX.sub("kunci rahasia", text)
    text = _EMPHASIS.sub("", text)
    text = _HR.sub(" ", text)
    text = _HEADING.sub("", text)
    text = _QUOTE.sub("", text)
    text = _EMOJI.sub("", text)
    return text, dropped_code, dropped_table


def _bullets_to_sentences(text: str) -> str:
    """Bullet markers become sentence breaks, never a spoken dash."""
    out: list[str] = []
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped:
            continue
        if _HR.match(stripped):
            continue
        item = _BULLET.sub("", stripped)
        if item != stripped:
            # A bullet item is one thought; give it a full stop so the
            # two-sentence summary can see a real boundary.
            if item and not item.endswith((".", "!", "?", ":", ";")):
                item += "."
            out.append(item)
            continue
        item = _NUMBERED.sub("", stripped)
        if item != stripped:
            if item and not item.endswith((".", "!", "?", ":", ";")):
                item += "."
            out.append(item)
            continue
        out.append(stripped)
    return " ".join(out)


def _tidy(text: str) -> str:
    text = re.sub(r"\s+", " ", text)
    # No space before punctuation — a synthesiser pauses oddly on " ,".
    text = re.sub(r"\s+([,.!?;:])", r"\1", text)
    text = re.sub(r"([,.!?;:])(?=[^\s\d])", r"\1 ", text)
    text = re.sub(r"\s{2,}", " ", text)
    return text.strip()


def _sentences(text: str) -> list[str]:
    parts = [part.strip() for part in re.split(r"(?<=[.!?])\s+", text) if part.strip()]
    return parts


def _with_full_stop(sentence: str) -> str:
    sentence = sentence.strip()
    if sentence == "":
        return sentence
    if sentence[-1] in ".!?":
        return sentence
    return sentence + "."


def _summarise(sentences: list[str], note: str) -> str:
    first = _with_full_stop(sentences[0])
    joined = " ".join(sentences)

    if len(sentences) <= _MAX_SENTENCES and len(joined) <= _MAX_CHARS:
        if note and len(sentences) < _MAX_SENTENCES and len(joined) + 1 + len(note) <= _MAX_CHARS:
            return f"{joined} {note}"
        return joined

    outcome = _with_full_stop(sentences[-1]) if len(sentences) > 1 else ""
    if outcome and outcome != first:
        # First sentence, then the outcome with the "look at the screen" clause
        # folded into the same sentence. That is two sentences, not three.
        second = outcome[:-1] + ", " + _DETAIL if outcome[-1] in ".!?" else outcome + " " + _DETAIL
        summary = f"{first} {second}"
    else:
        summary = first[:-1] + ", " + _DETAIL if first[-1] in ".!?" else first + " " + _DETAIL

    if note and len(_sentences(summary)) < _MAX_SENTENCES and len(summary) + 1 + len(note) <= _MAX_CHARS:
        summary = f"{summary} {note}"

    # The last sentence of a long answer can itself be a paragraph. Cap the whole summary at a WORD
    # boundary and keep the "look at the screen" clause — an assistant that reads 300 characters aloud,
    # or cuts a word in half, is worse than one that says less.
    if len(summary) > _MAX_CHARS:
        budget = _MAX_CHARS - len(_DETAIL) - 2
        head = summary[:budget]
        cut = head.rfind(" ")
        if cut > 40:
            head = head[:cut]
        head = head.rstrip(" ,;:-")
        if not head.endswith((".", "!", "?")):
            head += "."
        summary = f"{head} {_DETAIL_SENTENCE}"
    return summary


def split_sentences(text: str) -> list[str]:
    """Split already-normalised speech into sentences for streaming.

    The normaliser has already capped the summary at two sentences, so this is
    usually one or two chunks. It never returns an empty sentence.
    """
    return _sentences(text)


def normalise_for_speech(text: str) -> str:
    """Turn a written reply into the spoken summary Iris sends to edge-tts."""
    if not text or not text.strip():
        return ""

    # The same rules as the screen: STATUS, artifact markers, standalone -- and
    # horizontal rules are gone before markdown stripping sees the text.
    text = clean_reply(text)

    out, dropped_code, dropped_table = _strip_markdown(text)

    # Expand known abbreviations before touching numbers, so "AI" survives.
    for abbr, spoken in _SPELLED.items():
        out = re.sub(rf"\b{re.escape(abbr)}\b", spoken, out)

    for pattern, replacement in _CURRENCY:
        out = pattern.sub(replacement, out)

    for pattern, mapping in _UNIT_WORDS:
        out = pattern.sub(lambda m, _m=mapping: _m[m.group(0)] if m.group(0) in _m else _m[m.group(0).lower()], out)

    for pattern, replacement in _SUFFIX:
        out = pattern.sub(replacement, out)

    # Indonesian "ribu/juta/triliun" are words now, so the numbers become cardinals.
    out = _NUMBER.sub(lambda m: _digits_to_words(m.group(0)), out)

    for pattern, replacement in _SYMBOLS:
        out = pattern.sub(replacement, out)

    out = _bullets_to_sentences(out)
    out = _tidy(out)

    sentences = _sentences(out)
    if not sentences:
        if dropped_code:
            return _CODE_NOTE
        if dropped_table:
            return _TABLE_NOTE
        return ""

    note = _CODE_NOTE if dropped_code else (_TABLE_NOTE if dropped_table else "")
    return _summarise(sentences, note)
