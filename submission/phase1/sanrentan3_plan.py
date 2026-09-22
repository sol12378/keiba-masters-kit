#!/usr/bin/env python3
"""Build the user-restored V20: sanrentan 3 tickets x 1,000pt."""
from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE
sys.path.insert(0, str(HERE))
import build_vote_plan as B  # noqa: E402

JST = timezone(timedelta(hours=9))
POLICY_ID = "COMPETITION-2026-V20-SANRENTAN3-1000"
TARGET = 936.5
TICKETS = 3
STAKE = 1000


def build(race_key: str, manifest_path: Path, now: datetime) -> tuple[dict, dict]:
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    race = manifest.get("race") or {}
    if str(race.get("race_key")) != race_key:
        raise B.PlanError("race identity mismatch")
    if manifest.get("market_complete") is not True or \
            manifest.get("complete_all_pool_capture") is not True:
        raise B.PlanError("odds capture is incomplete")
    if "sanrentan" not in set(manifest.get("offered_bet_types") or ()):
        raise B.PlanError("sanrentan is not offered")
    surface, seconds_to_post, _ = B.load_surface(manifest_path)
    chosen = B.pick(surface, "sanrentan", TARGET, TICKETS,
                    model_weight=B.MODEL_WEIGHT)
    if len(chosen) != TICKETS:
        raise B.PlanError("fewer than three sanrentan combinations")
    post = datetime.fromisoformat(str(race["post_at"]))
    hard = post - timedelta(seconds=B.HARD_CUTOFF_BEFORE_POST)
    if now >= hard:
        raise B.PlanError("hard deadline reached")
    payload = {
        "race_id": B.official_race_id(race_key),
        "mark": B.marks_from_market(surface),
        "bet_list": [{
            "bet_id": "b8_c0_" + "_".join(str(n) for n in nums),
            "money": str(STAKE),
        } for _odds, nums in chosen],
    }
    plan = {
        "schema_version": 1,
        "plan_id": f"{payload['race_id']}-{now.strftime('%Y%m%dT%H%M%S')}",
        "policy_id": POLICY_ID,
        "race_date": post.astimezone(JST).date().isoformat(),
        "created_at": now.isoformat(timespec="seconds"),
        "scheduled_post_time": post.isoformat(timespec="seconds"),
        "target_submit_time": (post - timedelta(seconds=300)).isoformat(timespec="seconds"),
        "hard_submit_deadline": hard.isoformat(timespec="seconds"),
        "payload": payload,
        "payload_sha256": B.canonical_sha256(payload),
        "provenance": {
            "forecast_path": str(manifest_path),
            "forecast_sha256": B.sha256_file(manifest_path),
            "model_sha256": None,
            "adapter_version": "competition-2026-v20-sanrentan3-1000",
        },
    }
    debug = {
        "pool": "sanrentan", "target_odds": TARGET, "tickets": TICKETS,
        "stake_each": STAKE, "race_spend": TICKETS * STAKE,
        "picked": [{"comb": "-".join(map(str, nums)), "odds": odds}
                   for odds, nums in chosen],
        "model_id": B.MODEL_ID, "model_weight": B.MODEL_WEIGHT,
        "odds_seconds_to_post": seconds_to_post,
    }
    return plan, debug


def atomic(path: Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    temporary.replace(path)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--race-key", required=True)
    parser.add_argument("--manifest", required=True, type=Path)
    parser.add_argument("--now")
    parser.add_argument("--out", type=Path)
    args = parser.parse_args()
    now = datetime.fromisoformat(args.now) if args.now else datetime.now(JST)
    try:
        plan, debug = build(args.race_key, args.manifest, now)
    except (B.PlanError, KeyError, TypeError, ValueError) as exc:
        print(f"PLAN_REFUSED {exc}", file=sys.stderr)
        return 2
    if args.out:
        atomic(args.out, plan)
        atomic(args.out.with_suffix(".debug.json"), debug)
    print(json.dumps({"plan": plan, "debug": debug}, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
