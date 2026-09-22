#!/usr/bin/env python3
"""Build one points-only longshot trifecta plan for the 2026-09-19/20 all-in run."""
from __future__ import annotations

import json
import math
import sys
from datetime import datetime, timedelta
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE
sys.path[:0] = [str(HERE)]
import build_vote_plan as B  # noqa: E402

JST = B.JST
POLICY_PREFIX = "COMPETITION-2026-ALLIN-SANRENTAN"
ADAPTER_VERSION = "competition-2026-allin-sanrentan-20260919"
MODEL_WEIGHT = 0.05
MIN_ODDS = 375.0
MAX_ODDS = 8000.0

# Calibrated before the target weekend from 186 T-10 races on
# 2026-08-29/30, 09-05/06 and 09-12/13.  The score is the summed blended
# probability of the best 13 eligible longshot trifectas in a race.
QUALITY_CUTOFF_C = 0.021288363337224135
QUALITY_CUTOFF_A = 0.022393743836591803
QUALITY_ANCHOR = {"A": 0.0230, "B": 0.02184, "C": 0.0205}

CLASS_SPECS = {
    "A": {"tickets": 31, "boost": 100, "bands": (17, 9, 5)},
    "B": {"tickets": 22, "boost": 200, "bands": (12, 6, 4)},
    "C": {"tickets": 13, "boost": 300, "bands": (7, 4, 2)},
}


def atomic(path: Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n",
                         encoding="utf-8")
    temporary.replace(path)


def policy_id(day: str) -> str:
    return f"{POLICY_PREFIX}-{day}"


def _band(odds: float) -> int:
    if odds < 1000.0:
        return 0
    if odds < 2500.0:
        return 1
    return 2


def score_surface(surface: dict) -> tuple[list[dict], dict]:
    triples = surface.get("sanrentan") or []
    wins = sorted(surface.get("win") or [], key=lambda item: item[0])
    if not triples or not wins:
        raise B.PlanError("win or sanrentan odds are missing")
    ranks = {int(nums[0]): index + 1 for index, (_odds, nums) in enumerate(wins)}
    favorite = int(wins[0][1][0])
    field_size = len(ranks)
    outsider_rank = max(4, min(7, math.ceil(field_size / 2) + 1))
    win_p = B.win_probabilities(surface)
    inverse_total = sum(1.0 / odds for odds, _nums in triples if odds > 0)
    if inverse_total <= 0 or not win_p:
        raise B.PlanError("market probabilities cannot be calculated")

    candidates = []
    seen: set[tuple[int, int, int]] = set()
    for odds, raw_nums in triples:
        nums = tuple(int(number) for number in raw_nums)
        if len(nums) != 3 or len(set(nums)) != 3 or nums in seen:
            continue
        seen.add(nums)
        if not MIN_ODDS <= float(odds) <= MAX_ODDS:
            continue
        if nums[0] == favorite:
            continue
        if not any(ranks.get(number, field_size + 1) >= outsider_rank for number in nums):
            continue
        market_probability = (1.0 / float(odds)) / inverse_total
        model_probability = B.order_probability("sanrentan", list(nums), win_p)
        blended = (1.0 - MODEL_WEIGHT) * market_probability + MODEL_WEIGHT * model_probability
        candidates.append({
            "odds": float(odds), "nums": nums, "band": _band(float(odds)),
            "market_probability": market_probability,
            "model_probability": model_probability,
            "score": blended,
        })
    candidates.sort(key=lambda row: (-row["score"], row["odds"], row["nums"]))
    if len(candidates) < CLASS_SPECS["A"]["tickets"]:
        raise B.PlanError(
            f"only {len(candidates)} eligible longshot trifectas; 31 are required")
    quality = sum(row["score"] for row in candidates[:13])
    return candidates, {
        "quality_score": quality,
        "field_size": field_size,
        "favorite": favorite,
        "outsider_rank_threshold": outsider_rank,
        "eligible_candidates": len(candidates),
    }


def score_manifest(manifest_path: Path) -> tuple[float, dict]:
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    if manifest.get("market_complete") is not True or \
            manifest.get("complete_all_pool_capture") is not True:
        raise B.PlanError("odds capture is incomplete")
    surface, seconds_to_post, _race = B.load_surface(manifest_path)
    _candidates, debug = score_surface(surface)
    debug["odds_seconds_to_post"] = seconds_to_post
    return float(debug["quality_score"]), debug


def proposed_class(quality_score: float) -> str:
    if quality_score >= QUALITY_CUTOFF_A:
        return "A"
    if quality_score <= QUALITY_CUTOFF_C:
        return "C"
    return "B"


def select_candidates(candidates: list[dict], race_class: str) -> list[dict]:
    try:
        spec = CLASS_SPECS[race_class]
    except KeyError as exc:
        raise B.PlanError(f"unknown race class {race_class!r}") from exc
    selected: list[dict] = []
    selected_nums: set[tuple[int, int, int]] = set()
    for band, wanted in enumerate(spec["bands"]):
        for row in (candidate for candidate in candidates if candidate["band"] == band):
            if len([item for item in selected if item["band"] == band]) >= wanted:
                break
            selected.append(row)
            selected_nums.add(row["nums"])
    # Small fields can have too few combinations in one price band.  Preserve
    # the total point count by filling from the best remaining eligible rows.
    for row in candidates:
        if len(selected) >= spec["tickets"]:
            break
        if row["nums"] not in selected_nums:
            selected.append(row)
            selected_nums.add(row["nums"])
    if len(selected) != spec["tickets"]:
        raise B.PlanError(
            f"class {race_class} requires {spec['tickets']} tickets; got {len(selected)}")
    selected.sort(key=lambda row: (-row["score"], row["odds"], row["nums"]))
    return selected


def expected_stake(race_class: str, extra_points: int = 0) -> int:
    spec = CLASS_SPECS[race_class]
    return spec["tickets"] * 1000 + spec["boost"] + int(extra_points)


def expected_amounts(race_class: str, extra_points: int = 0) -> list[int]:
    spec = CLASS_SPECS[race_class]
    return sorted([1000 + spec["boost"] + int(extra_points)]
                  + [1000] * (spec["tickets"] - 1))


def build(race_key: str, manifest_path: Path, now: datetime, race_class: str,
          *, extra_points: int = 0, active_policy_id: str | None = None) -> tuple[dict, dict]:
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
    candidates, quality_debug = score_surface(surface)
    chosen = select_candidates(candidates, race_class)

    post = datetime.fromisoformat(str(race["post_at"]))
    if post.tzinfo is None:
        post = post.replace(tzinfo=JST)
    hard = post - timedelta(seconds=B.HARD_CUTOFF_BEFORE_POST)
    if now >= hard:
        raise B.PlanError("hard deadline reached")
    day = post.astimezone(JST).strftime("%Y%m%d")
    spec = CLASS_SPECS[race_class]
    bet_list = []
    for index, row in enumerate(chosen):
        money = 1000 + (spec["boost"] + int(extra_points) if index == 0 else 0)
        bet_list.append({
            "bet_id": "b8_c0_" + "_".join(str(number) for number in row["nums"]),
            "money": str(money),
        })
    payload = {
        "race_id": B.official_race_id(race_key),
        "mark": B.marks_from_market(surface),
        "bet_list": bet_list,
    }
    plan = {
        "schema_version": 1,
        "plan_id": f"{payload['race_id']}-{now.strftime('%Y%m%dT%H%M%S')}",
        "policy_id": active_policy_id or policy_id(day),
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
            "adapter_version": ADAPTER_VERSION,
        },
    }
    debug = {
        "pool": "sanrentan", "race_class": race_class,
        "class_spec": spec, "extra_points": int(extra_points),
        "tickets": len(bet_list), "race_spend": sum(int(bet["money"]) for bet in bet_list),
        "picked": [{"comb": "-".join(map(str, row["nums"])),
                    "odds": row["odds"], "band": row["band"],
                    "blended_probability": row["score"]} for row in chosen],
        "model_id": B.MODEL_ID, "model_weight": MODEL_WEIGHT,
        "odds_seconds_to_post": seconds_to_post,
        **quality_debug,
    }
    return plan, debug
