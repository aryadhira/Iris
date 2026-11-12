#!/usr/bin/env python3
"""Write Iris' editable price table from Hermes' own snapshot.

Run it with the Hermes interpreter and its workspace on PYTHONPATH:

    W=$(ls -d ~/.hermes/installs/*/environments/*/workspace | head -1)
    PY=$(dirname "$W")/venv/bin/python
    PYTHONPATH="$W" "$PY" scripts/seed-prices.py

The output shape is:

    {"<model>": {"input": 0.15, "output": 0.6, "cache_read": 0.003,
                 "cache_write": null, "unit": "USD_per_1M", "source": "..."}}

Rules:
  * Every number comes from agent/usage_pricing.py's official snapshot table.
  * A rate the table does not publish is written as null, never 0.
  * Tiered entries (a different price above N tokens) are skipped: the flat
    file cannot express them honestly, and a wrong number is worse than none.
  * ``deepseek-v4.1-flash`` gets the table's own Flash row. The table's comment
    says "deepseek-v4-flash ... and the retired aliases are served by V4.1-Flash
    at the Flash price". This is the documented model identity, not a new rate.
    The served model id differs from the table key, so without this alias the
    meter would say "price unknown" for every real turn on this machine.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path


def _workspace_default() -> str:
    installs = Path.home() / ".hermes" / "installs"
    found = sorted(installs.glob("*/environments/*/workspace"))
    return str(found[0]) if found else ""


def _number(value):
    return None if value is None else float(value)


def build(workspace: str) -> tuple[dict, list[str], list[str]]:
    if workspace and workspace not in sys.path:
        sys.path.insert(0, workspace)
    from agent import usage_pricing  # noqa: E402  (path set from the CLI)

    table = usage_pricing._OFFICIAL_DOCS_PRICING  # snapshot, not a network call
    prices: dict[str, dict] = {}
    skipped: list[str] = []
    aliases: list[str] = []

    for (provider, model), entry in table.items():
        if getattr(entry, "tier_threshold_tokens", None) is not None:
            skipped.append(f"{provider}/{model} (tiered above {entry.tier_threshold_tokens})")
            continue
        row = {
            "input": _number(entry.input_cost_per_million),
            "output": _number(entry.output_cost_per_million),
            "cache_read": _number(entry.cache_read_cost_per_million),
            "cache_write": _number(entry.cache_write_cost_per_million),
            "unit": "USD_per_1M",
            "source": (
                f"{entry.source_url or 'unknown'}"
                f" ({entry.pricing_version or entry.source or 'unknown'})"
            ),
        }
        # A provider-qualified key is available for a model served by two
        # providers at different rates. The bare id stays for the common case.
        prices[f"{provider}/{model}"] = row
        if model not in prices:
            prices[model] = row

    # The table cannot see the id this gateway actually serves. Its own comment
    # names V4.1-Flash as the Flash-price model; record that mapping in the
    # source string so Boss can see exactly why the number is there.
    if "deepseek-v4.1-flash" not in prices:
        flash = prices.get("deepseek/deepseek-v4-flash")
        if flash:
            prices["deepseek-v4.1-flash"] = {
                **flash,
                "source": (
                    f"{flash['source']}; table comment: V4.1-Flash is served at "
                    "the Flash price (alias of deepseek-v4-flash)"
                ),
            }
            aliases.append("deepseek-v4.1-flash -> deepseek-v4-flash")
        flash2 = prices.get("opencode-go/deepseek-v4.1-flash")
        if flash2:
            # A provider-qualified published row already exists; prefer it.
            prices["deepseek-v4.1-flash"] = flash2

    return dict(sorted(prices.items())), skipped, aliases


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", default=os.environ.get("HERMES_WORKSPACE") or _workspace_default())
    parser.add_argument("--out", default=str(Path.home() / ".config" / "iris" / "prices.json"))
    parser.add_argument("--stdout", action="store_true", help="print the JSON instead of writing it")
    args = parser.parse_args()

    if not args.workspace:
        print("no Hermes workspace found; pass --workspace", file=sys.stderr)
        return 2
    if not Path(args.workspace).is_dir():
        print(f"workspace not found: {args.workspace}", file=sys.stderr)
        return 2

    prices, skipped, aliases = build(args.workspace)
    if not prices:
        print("the Hermes price table was empty; refusing to write an empty file", file=sys.stderr)
        return 1

    payload = json.dumps(prices, indent=2, sort_keys=True) + "\n"
    if args.stdout:
        sys.stdout.write(payload)
        return 0

    out = Path(args.out).expanduser()
    out.parent.mkdir(parents=True, exist_ok=True)
    tmp = out.with_suffix(out.suffix + ".tmp")
    tmp.write_text(payload, encoding="utf-8")
    os.chmod(tmp, 0o600)
    os.replace(tmp, out)
    print(f"wrote {len(prices)} entries to {out} (mode 0600)")
    if aliases:
        print("aliases: " + ", ".join(aliases))
    if skipped:
        print(f"skipped {len(skipped)} tiered entries:")
        for item in skipped:
            print(f"  - {item}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
