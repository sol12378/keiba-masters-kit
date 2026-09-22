#!/usr/bin/env python3
"""Turn a synthetic race into the odds-surface manifest phase 1 expects.

The scripts under ``submission/`` are the ones that actually ran during the
contest, and they read a snapshot manifest produced by the collector.  Those
snapshots are captured odds: they belong to the sites they came from and this
repository cannot redistribute them.

This converter closes that gap.  It writes the same manifest schema from
``pykeiba.synth`` output, so anyone can execute the real decision path
end to end -- odds in, plan and stake out -- without a single real price
changing hands.

What it does not do: prove that the real snapshots had this shape.  For that,
``submission/ledger/`` carries the SHA-256 of every real input that was used.

    python3 submission/tools/synthetic_surface.py \
        --panel data/synthetic --race-id <id> --out surface.json
"""

from __future__ import annotations

import argparse
import itertools
import json
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT))

from pykeiba.harville import order_probability  # noqa: E402
from pykeiba.odds import win_probabilities  # noqa: E402
from pykeiba.panel import load_panel  # noqa: E402

#: Pool numbering used by the collector's manifests, mirrored from
#: ``build_vote_plan.ODDS_TYPE``.
#:
#: Note that this is NOT the bet-id numbering.  A bet id counts pools in the
#: official order (b7 trio, b8 trifecta), while a manifest's odds_type skips 7
#: and uses 8 for the trio and 9 for the trifecta.  The two schemes agree up to
#: 6 and then diverge, which is exactly the sort of thing that silently
#: produces "the pool is not quoted" instead of a wrong price.
ODDS_TYPE = {
    "win": 1,
    "place": 2,
    "wakuren": 3,
    "umaren": 4,
    "wide": 5,
    "umatan": 6,
    "sanrenpuku": 8,
    "sanrentan": 9,
}

#: Fraction of turnover each pool returns, used to price the pools the
#: synthetic panel does not quote directly.  These are round statutory-ish
#: figures, not measurements.
RETURN_RATE = {
    "place": 0.80,
    "wakuren": 0.775,
    "umaren": 0.775,
    "wide": 0.775,
    "umatan": 0.75,
    "sanrenpuku": 0.75,
}


def _price(probability: float, return_rate: float) -> float | None:
    if probability <= 0:
        return None
    return round(return_rate / probability, 1)


def build_surface(race, seconds_to_post: int = 300) -> dict:
    """Manifest for one race: every pool phase 1 may be told to buy.

    The panel quotes win and trifecta directly.  The other pools are derived
    from the win market through the same Harville construction the model uses,
    then marked up by a takeout.  They exist so the policy table can name any
    pool and still find a price; they are not claimed to match a real board.
    """
    win_p = win_probabilities(race.win_odds)
    horses = sorted(race.win_odds)
    rows: list[dict] = []

    for horse in horses:
        rows.append({"odds_type": ODDS_TYPE["win"], "comb": str(horse),
                     "odds": round(float(race.win_odds[horse]), 1)})

    for horse in horses:
        # Place: the chance of finishing in the first three.
        probability = sum(
            order_probability(order, win_p)
            for order in itertools.permutations(horses, 3)
            if horse in order
        )
        price = _price(probability, RETURN_RATE["place"])
        if price:
            rows.append({"odds_type": ODDS_TYPE["place"], "comb": str(horse), "odds": price})

    for first, second in itertools.permutations(horses, 2):
        exacta = order_probability((first, second), win_p)
        price = _price(exacta, RETURN_RATE["umatan"])
        if price:
            rows.append({"odds_type": ODDS_TYPE["umatan"],
                         "comb": f"{first}-{second}", "odds": price})

    for pair in itertools.combinations(horses, 2):
        quinella = sum(order_probability(order, win_p) for order in itertools.permutations(pair))
        price = _price(quinella, RETURN_RATE["umaren"])
        if price:
            rows.append({"odds_type": ODDS_TYPE["umaren"],
                         "comb": f"{pair[0]}-{pair[1]}", "odds": price})
        wide = sum(
            order_probability(order, win_p)
            for order in itertools.permutations(horses, 3)
            if pair[0] in order and pair[1] in order
        )
        price = _price(wide, RETURN_RATE["wide"])
        if price:
            rows.append({"odds_type": ODDS_TYPE["wide"],
                         "comb": f"{pair[0]}-{pair[1]}", "odds": price})

    for triple in itertools.combinations(horses, 3):
        trio = sum(order_probability(order, win_p) for order in itertools.permutations(triple))
        price = _price(trio, RETURN_RATE["sanrenpuku"])
        if price:
            rows.append({"odds_type": ODDS_TYPE["sanrenpuku"],
                         "comb": "-".join(map(str, triple)), "odds": price})

    for triple, price in sorted(race.trifecta_odds.items()):
        rows.append({"odds_type": ODDS_TYPE["sanrentan"],
                     "comb": "-".join(map(str, triple)), "odds": round(float(price), 1)})

    return {
        "source": "synthetic",
        "note": (
            "Generated by pykeiba.synth and converted for submission/phase1. "
            "No real odds are contained in this file."
        ),
        "race": {"race_id": race.race_id, "post_at": race.post_at.isoformat()},
        "seconds_to_post": seconds_to_post,
        "odds_surface": rows,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--panel", default="data/synthetic")
    parser.add_argument("--race-id", default=None, help="defaults to the first race in the panel")
    parser.add_argument("--seconds-to-post", type=int, default=300)
    parser.add_argument("--out", required=True)
    arguments = parser.parse_args()

    races = list(load_panel(Path(arguments.panel)))
    if not races:
        raise SystemExit(f"no races under {arguments.panel}")
    if arguments.race_id:
        races = [race for race in races if race.race_id == arguments.race_id]
        if not races:
            raise SystemExit(f"race {arguments.race_id} is not in {arguments.panel}")

    surface = build_surface(races[0], arguments.seconds_to_post)
    out = Path(arguments.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(surface, ensure_ascii=False, indent=1, sort_keys=True) + "\n",
                   encoding="utf-8")
    pools = {row["odds_type"] for row in surface["odds_surface"]}
    print(json.dumps({
        "out": str(out),
        "race_id": surface["race"]["race_id"],
        "rows": len(surface["odds_surface"]),
        "pools": sorted(pools),
    }, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
