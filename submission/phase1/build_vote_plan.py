#!/usr/bin/env python3
"""Turn one race's bet-time odds into a voting plan, using the frozen policy table.

The policy does not predict horses. It names a pool, a target odds and a ticket
count; this script buys whichever combinations are priced closest to that target.
Marks are filled from market rank because the competition requires them and they
only ever matter as a tie-break.

State comes from a ledger the caller maintains:

    {"races_remaining": 264, "bank": 1000000, "turnover": 0,
     "front_races_done": 0}

The stake floor is recomputed every race rather than fixed, so a race that gets
skipped is made up by the ones after it:

    floor = clamp(ceil((500000 - turnover) / front races left / 100) * 100,
                  100, per-race cap)

Usage:
  python3 scripts/build_competition_vote_plan.py \
      --race-id 202608290101 --odds path/to/manifest.json \
      --state outputs/competition-2026/ledger.json \
      --out outputs/competition-2026/plans/202608290101.json
"""
from __future__ import annotations

import argparse
import hashlib
import itertools
import json
import math
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE
ADAPTER_VERSION = "competition-2026-v1.9-tail133"
POLICY_ID = "COMPETITION-2026-V17"
TAIL_POLICY_PATH = HERE / "tail_policy.json"
JST = timezone(timedelta(hours=9))
# How much of the choice inside the price window the probability model decides.
# Measured on 4,350 races: 0.05 changes which tickets are bought in 59.7% of them
# while moving the bought odds by 0.11 percentage points of median deviation.
MODEL_WEIGHT = 0.05
MODEL_ID = "harville-market-v1"

# bet_id prefix per official pool order, which is not the odds_type order.
BET_PREFIX = {"win": "b1", "place": "b2", "wakuren": "b3", "umaren": "b4",
              "wide": "b5", "umatan": "b6", "sanrenpuku": "b7", "sanrentan": "b8"}
ORDERED = {"umatan", "sanrentan"}
# odds_type -> pool, for the daily-lab manifest layout.
ODDS_TYPE = {1: "win", 2: "place", 3: "wakuren", 4: "umaren", 5: "wide",
             6: "umatan", 8: "sanrenpuku", 9: "sanrentan"}
MIN_TURNOVER = 500_000
STAKE_UNIT = 100
# The daemon computes these from the policy and rejects anything else.
TARGET_SUBMIT_BEFORE_POST = 300
HARD_CUTOFF_BEFORE_POST = 240


def ceil_unit(value: float, unit: int = STAKE_UNIT) -> int:
    return int(math.ceil(max(float(value), float(unit)) / unit) * unit)


def load_tail_policy() -> tuple[dict | None, str | None]:
    """Load the day-scoped strategy without changing the frozen V17 table."""
    if not TAIL_POLICY_PATH.is_file() or TAIL_POLICY_PATH.is_symlink():
        return None, None
    policy = json.loads(TAIL_POLICY_PATH.read_text(encoding="utf-8"))
    if policy.get("policy_id") != "COMPETITION-2026-V18-TAIL133":
        raise PlanError("the 2026-08-30 tail policy id is invalid")
    return policy, sha256_file(TAIL_POLICY_PATH)


def effective_goal(policy: dict, race_date: str, now: datetime) -> tuple[int, dict]:
    """Use a fresh heartbeat ranking target, otherwise retain the 2M floor."""
    base = int(policy["base_goal_points"])
    maximum = int(policy["maximum_goal_points"])
    goal = base
    evidence = {"ranking_target_status": "BASE_GOAL_FALLBACK",
                "ranking_target_path": policy.get("ranking_target_path")}
    target_path = ROOT / str(policy.get("ranking_target_path", ""))
    if target_path.is_file() and not target_path.is_symlink():
        try:
            target = json.loads(target_path.read_text(encoding="utf-8"))
            captured = datetime.fromisoformat(str(target["captured_at"]))
            if captured.tzinfo is None:
                captured = captured.replace(tzinfo=JST)
            age = (now - captured).total_seconds()
            if (target.get("race_date") == race_date and 0 <= age <=
                    int(policy["ranking_target_max_age_seconds"])):
                leader = int(target["opponent_leader_points"])
                proposed = max(int(target.get("goal_points", 0)),
                               leader + int(policy["opponent_leader_buffer_points"]))
                goal = min(maximum, max(base, ceil_unit(proposed)))
                evidence.update({
                    "ranking_target_status": "FRESH",
                    "ranking_target_sha256": sha256_file(target_path),
                    "ranking_target_age_seconds": age,
                    "opponent_leader_points": leader,
                })
        except (KeyError, TypeError, ValueError, json.JSONDecodeError):
            evidence["ranking_target_status"] = "INVALID_BASE_GOAL_FALLBACK"
    return goal, evidence


def one_hit_reserve_instruction(state: dict, policy: dict, goal: int,
                                cap: int) -> tuple[int, dict]:
    """Smallest stake that preserves qualification and makes one hit reach goal."""
    unit = int(policy["stake_unit"])
    odds = float(policy["target_odds"])
    bank = int(state["bank"])
    turnover = int(state.get("turnover", 0))
    count = int(state.get("races_bet", state.get("front_races_done", 0)))
    turnover_need = max(0, int(policy["minimum_turnover_points"]) - turnover)
    count_need = max(0, int(policy["minimum_qualified_races"]) - count)
    future_count_reserve = int(policy["minimum_future_race_stake_points"]) * max(count_need - 1, 0)

    one_hit = ceil_unit((goal + turnover_need - bank) / odds, unit)
    if turnover_need - one_hit < future_count_reserve:
        one_hit = ceil_unit(max(
            turnover_need - future_count_reserve,
            (goal + future_count_reserve - bank) / max(odds - 1.0, 1e-9),
        ), unit)
    qualification_floor = (ceil_unit(turnover_need / max(count_need, 1), unit)
                           if count_need > 0 and turnover_need > 0 else unit)
    stake = max(unit, one_hit, qualification_floor)
    stake = min(int(policy["maximum_stake_per_race_points"]), cap, stake)
    if stake > bank:
        stake = (bank // unit) * unit
    if stake < unit:
        raise PlanError(f"bank {bank} cannot fund the {unit}pt minimum")
    return stake, {
        "goal_points": goal,
        "qualification_turnover_remaining": turnover_need,
        "qualification_races_remaining": count_need,
        "qualification_floor": qualification_floor,
        "one_hit_minimum_stake": one_hit,
    }


def official_race_id(race_key: str) -> str:
    """The daily-lab directory name is 16 characters; the API wants the 12 the
    official site uses.

    The 16-character key is date(8) + venue(2) + kai(2) + nichi(2) + race(2).
    The voting id drops the month and day and keeps the meeting coordinates:
    year(4) + venue(2) + kai(2) + nichi(2) + race(2). The 26 race ids that were
    accepted and read back as CONFIRMED by the official API on 2026-08-22 and
    2026-08-23 all follow this rule, and `keiba/day_data_api.py` and
    `keiba/research/jra_public.py` derive it the same way.
    """
    text = str(race_key)
    if len(text) == 12:
        return text
    if len(text) == 16:
        return text[:4] + text[8:16]
    raise PlanError(f"cannot derive an official race id from {text!r}")


class PlanError(RuntimeError):
    """Raised when the plan cannot be built. The caller must not submit."""


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def canonical_sha256(obj) -> str:
    body = json.dumps(obj, ensure_ascii=False, sort_keys=True,
                      separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(body).hexdigest()


def parse_comb(pool: str, comb: str) -> list[int] | None:
    """Normalise a surface combination to a list of numbers."""
    text = str(comb)
    if pool in ("win", "place"):
        parts = [text]
    elif pool == "wakuren":
        if "-" in text:
            parts = text.split("-")
        elif len(text) == 2 and text.isdigit():
            parts = [text[0], text[1]]
        else:
            return None
    elif pool == "umaren" and "-" not in text and len(text) == 4:
        parts = [text[:2], text[2:]]
    else:
        parts = text.split("-")
    try:
        nums = [int(p) for p in parts]
    except ValueError:
        return None
    if not nums or any(n < 1 or n > 18 for n in nums):
        return None
    return nums if pool in ORDERED else sorted(nums)


def load_surface(path: Path):
    """Return {pool: [(odds, numbers)]}, seconds to post, and the race block."""
    data = json.loads(path.read_text())
    rows = data.get("odds_surface") or data.get("rows") or []
    out: dict[str, list[tuple[float, list[int]]]] = {}
    for row in rows:
        pool = ODDS_TYPE.get(row.get("odds_type"))
        odds = row.get("odds")
        if pool is None or not odds or odds <= 0:
            continue
        nums = parse_comb(pool, row.get("comb"))
        if nums is None:
            continue
        out.setdefault(pool, []).append((float(odds), nums))
    return out, data.get("seconds_to_post"), (data.get("race") or {})


def current_floor(state: dict, cap: int, n_front: int, front_floor: int,
                  tail_floor: int) -> int:
    """Recompute the floor so skipped races are absorbed by the remaining ones."""
    turnover = int(state.get("turnover", 0))
    if turnover >= MIN_TURNOVER:
        return tail_floor
    done = int(state.get("front_races_done", 0))
    left = max(1, n_front - done)
    need = math.ceil((MIN_TURNOVER - turnover) / left / STAKE_UNIT) * STAKE_UNIT
    return int(min(cap, max(tail_floor, need, 0) or tail_floor))


def lookup(table: dict, races_remaining: int, bank: int) -> dict:
    """Round the bank down to the grid. Falling back upward would read a richer
    state's instruction, which the bank cannot afford, so only ever go down."""
    grid = int(table["bank_grid"])
    n = int(table["n_races"])
    rem = max(1, min(n, int(races_remaining)))
    key_bank = max(0, (int(bank) // grid) * grid)
    index = table["_index"]
    row = index.get((rem, key_bank))
    if row is not None:
        return row
    available = sorted(b for r, b in index if r == rem)
    if not available:
        raise PlanError(f"the policy table has no row for {rem} races remaining")
    below = [b for b in available if b <= key_bank]
    if not below:
        raise PlanError(f"bank {bank} is below every row in the policy table")
    return index[(rem, below[-1])]


def win_probabilities(surface) -> dict[int, float]:
    """Market win probabilities, overround removed. The model's input, not its output."""
    out: dict[int, float] = {}
    for odds, nums in (surface.get("win") or []):
        if odds and odds > 0 and nums:
            out[int(nums[0])] = 1.0 / float(odds)
    total = sum(out.values())
    return {k: v / total for k, v in out.items()} if total > 0 else {}


def order_probability(pool: str, nums: list[int], win_p: dict[int, float]) -> float:
    """Harville: the chance this exact combination fills the places it needs.

    For an ordered pool it is the product of conditional win probabilities down
    the finishing order. For an unordered pool it is the sum over the orders that
    would settle it, which for three runners is six terms.
    """
    if not win_p or not nums:
        return 0.0

    def chain(seq: list[int]) -> float:
        remaining, value = 1.0, 1.0
        for horse in seq:
            p = win_p.get(horse, 0.0)
            if remaining <= 1e-9 or p <= 0.0:
                return 0.0
            value *= p / remaining
            remaining -= p
        return value

    if pool in ORDERED or len(nums) == 1:
        return chain(nums)
    return sum(chain(list(order)) for order in itertools.permutations(nums))


def pick(surface, pool: str, target: float, k: int,
         model_weight: float = MODEL_WEIGHT) -> list[tuple[float, list[int]]]:
    """Choose k combinations: mostly by price, partly by the probability model.

    The policy table fixes the price. Which combinations at that price to buy is
    the part the competition requires a model to decide, and a small weight is
    enough to decide it: the candidates sit within a fraction of a percent of
    each other in price, so `model_weight` 0.05 reorders the shortlist in most
    races while moving the bought odds by about a tenth of a percent.
    """
    cands = surface.get(pool) or []
    if not cands:
        raise PlanError(f"{pool} is not quoted in this snapshot")
    if not 0.0 <= model_weight <= 1.0:
        raise PlanError(f"model weight must be within [0, 1], got {model_weight}")
    n = len(cands)
    price_rank = {id(c): i for i, c in
                  enumerate(sorted(cands, key=lambda x: abs(math.log(x[0]) - math.log(target))))}
    if model_weight <= 0.0:
        return sorted(cands, key=lambda c: price_rank[id(c)])[:k]
    win_p = win_probabilities(surface)
    if not win_p:
        raise PlanError("win odds are missing, so the model cannot score candidates")
    model_rank = {id(c): i for i, c in
                  enumerate(sorted(cands, key=lambda x: -order_probability(pool, x[1], win_p)))}
    blended = sorted(cands, key=lambda c: ((1.0 - model_weight) * price_rank[id(c)]
                                           + model_weight * model_rank[id(c)]) / n)
    return blended[:k]


def marks_from_market(surface) -> dict[str, int]:
    """Mark by win-odds rank. Only ever used as the official tie-break."""
    win = surface.get("win") or []
    if not win:
        return {}
    ranked = sorted(win, key=lambda x: x[0])
    out: dict[str, int] = {}
    for i, (_odds, nums) in enumerate(ranked[:4]):
        out[str(nums[0])] = i + 1
    return out or {str(ranked[0][1][0]): 1}


def build_plan(race_id: str, odds_path: Path, state: dict, table: dict,
               table_path: Path, table_hash: str, post_time: str | None,
               allow_past: bool = False, now: datetime | None = None) -> dict:
    surface, secs, race_block = load_surface(odds_path)
    now = now or datetime.now(JST)
    raw_post = post_time or race_block.get("post_at")
    if not raw_post:
        raise PlanError("the snapshot carries no scheduled post time")
    post = datetime.fromisoformat(raw_post)
    if post.tzinfo is None:
        post = post.replace(tzinfo=JST)
    race_date = post.astimezone(JST).strftime("%Y-%m-%d")

    cap = int(table["per_race_cap"])
    sched = table["floor_schedule"]
    n_front = int(sched[0]["races_to"])
    floor = current_floor(state, cap, n_front, int(sched[0]["floor"]),
                          int(sched[1]["floor"]))
    bank = int(state["bank"])
    strategy_policy, strategy_hash = load_tail_policy()
    strategy_debug: dict = {"strategy_mode": "policy_table_v17"}
    policy_id = POLICY_ID
    if strategy_policy and strategy_policy.get("active_date") == race_date:
        pool = str(strategy_policy["bet_type"])
        target = float(strategy_policy["target_odds"])
        k = int(strategy_policy["tickets"])
        goal, goal_debug = effective_goal(strategy_policy, race_date, now)
        stake, reserve_debug = one_hit_reserve_instruction(state, strategy_policy,
                                                           goal, cap)
        floor = int(reserve_debug["qualification_floor"])
        policy_id = str(strategy_policy["policy_id"])
        strategy_debug = {
            "strategy_mode": str(strategy_policy["stake_mode"]),
            "strategy_policy_path": str(TAIL_POLICY_PATH),
            "strategy_policy_sha256": strategy_hash,
            **goal_debug,
            **reserve_debug,
        }
    else:
        row = lookup(table, state["races_remaining"], bank)
        if row.get("action") == "min_stake_only" or not row.get("bet_type"):
            pool, target, k, stake = "place", 1.8, 1, max(floor, STAKE_UNIT)
        else:
            pool = row["bet_type"]
            target = float(row["target_odds"])
            k = int(row["tickets"])
            stake = int(row["stake_each"])
    if stake * k < floor:
        k = max(k, math.ceil(floor / stake))
    spend = stake * k
    if spend > cap:
        k = max(1, cap // stake)
        spend = stake * k
    if spend > bank:
        raise PlanError(f"instruction costs {spend} but the bank holds {bank}")

    chosen = pick(surface, pool, target, k)
    if len(chosen) < k:
        raise PlanError(f"{pool} has only {len(chosen)} combinations, need {k}")
    prefix = BET_PREFIX[pool]
    bet_list = [{"bet_id": prefix + "_c0_" + "_".join(str(n) for n in nums),
                 "money": str(stake)} for _odds, nums in chosen]

    deadline = post - timedelta(seconds=HARD_CUTOFF_BEFORE_POST)
    if now >= deadline and not allow_past:
        raise PlanError(f"already past the hard deadline {deadline.isoformat()}")
    if allow_past:
        # replay of a finished card: keep created_at inside the envelope's own window
        now = min(now, deadline - timedelta(seconds=1))
    official = official_race_id(race_id)
    payload = {"race_id": official, "mark": marks_from_market(surface),
               "bet_list": bet_list}
    if not payload["mark"]:
        raise PlanError("no win odds available, cannot fill the required marks")
    plan = {
        # The daemon accepts version 1 only; the envelope shape is its contract.
        "schema_version": 1,
        "plan_id": f"{official}-{now.strftime('%Y%m%dT%H%M%S')}",
        "policy_id": policy_id,
        "race_date": race_date,
        "created_at": now.isoformat(timespec="seconds"),
        "scheduled_post_time": post.isoformat(timespec="seconds"),
        "target_submit_time": (post - timedelta(
            seconds=TARGET_SUBMIT_BEFORE_POST)).isoformat(timespec="seconds"),
        "hard_submit_deadline": deadline.isoformat(timespec="seconds"),
        "payload": payload,
        "payload_sha256": canonical_sha256(payload),
        "provenance": {
            "forecast_path": str(odds_path),
            "forecast_sha256": sha256_file(odds_path),
            "model_sha256": strategy_hash if policy_id != POLICY_ID else None,
            "adapter_version": ADAPTER_VERSION,
        },
    }
    # Kept beside the envelope, not inside it: the schema forbids extra keys.
    plan_debug = {
        "pool": pool, "target_odds": target, "tickets": k, "stake_each": stake,
        "race_spend": spend,
        "picked": [{"comb": "-".join(str(n) for n in nums), "odds": o}
                   for o, nums in chosen],
        # Recorded every race so the competition itself becomes the forward test
        # of whether the model's ordering is worth anything.
        "model_id": MODEL_ID,
        "model_weight": MODEL_WEIGHT,
        "model_probability": [order_probability(pool, nums, win_probabilities(surface))
                              for _o, nums in chosen],
        "price_only_choice": ["-".join(str(n) for n in nums) for _o, nums in
                              pick(surface, pool, target, k, model_weight=0.0)],
        "worst_log_distance": max(abs(math.log(o) - math.log(target))
                                  for o, _ in chosen),
        "policy_table_sha256": table_hash,
        "odds_seconds_to_post": secs,
        "races_remaining": int(state["races_remaining"]),
        "bank": bank,
        "turnover": int(state.get("turnover", 0)),
        "floor_applied": floor,
        **strategy_debug,
    }
    return plan, plan_debug


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--race-id", required=True)
    ap.add_argument("--odds", required=True, type=Path)
    ap.add_argument("--state", required=True, type=Path)
    ap.add_argument("--table", type=Path,
                    default=HERE / "policy_table_v17.json")
    ap.add_argument("--post-time")
    ap.add_argument("--out", type=Path)
    args = ap.parse_args()

    table = json.loads(args.table.read_text())
    table["_index"] = {(r["races_remaining"], r["bank"]): r for r in table["rows"]}
    state = json.loads(args.state.read_text())
    try:
        plan, debug = build_plan(args.race_id, args.odds, state, table,
                                 args.table, sha256_file(args.table),
                                 args.post_time)
    except PlanError as err:
        print(f"PLAN_REFUSED {err}", file=sys.stderr)
        return 2
    text = json.dumps(plan, ensure_ascii=False, indent=1)
    if args.out:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(text)
        args.out.with_suffix(".debug.json").write_text(
            json.dumps(debug, ensure_ascii=False, indent=1))
        d = debug
        print(f"{args.race_id}  {d['pool']}@{d['target_odds']:g} x{d['tickets']} "
              f"{d['stake_each']:,}pt/点 = {d['race_spend']:,}pt  "
              f"-> {args.out}")
    else:
        print(text)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
