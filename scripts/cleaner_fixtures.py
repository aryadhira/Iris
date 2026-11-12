#!/usr/bin/env python3
"""Print one reply fixture cleaned by voice.normalise.clean_reply as base64."""
from __future__ import annotations

import base64
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from voice.normalise import clean_reply  # noqa: E402


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: cleaner_fixtures.py <file>", file=sys.stderr)
        return 2
    # Read bytes so Python does not translate CRLF; parity is byte-for-byte.
    raw = Path(sys.argv[1]).read_bytes().decode("utf-8")
    print(base64.b64encode(clean_reply(raw).encode("utf-8")).decode("ascii"))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
