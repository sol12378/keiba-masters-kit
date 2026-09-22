"""Learn a small cross-market model and allocate virtual points to a target.

PUBLISHED AS A RECORD.  This is the script that ran on 2026-09-20, with only
its paths made relative.  ``historical()`` reads T-10 snapshots that are
captured odds and are not redistributable, so it cannot run from a clone; the
path below is left exactly where it pointed so the input is identifiable.

To verify the fit without that data, run ``reproduce.py``, which re-derives the
coefficients from the published objective surface.

All probabilities are conditional market/model estimates, not championship odds.
The shortlist knapsack maximizes single-race target-hit probability for a fixed
race allowance; it does not claim a globally optimal multi-player strategy.
"""
from __future__ import annotations

import json
import math
import os
import sys
from pathlib import Path

import numpy as np
from scipy.optimize import minimize
from scipy.special import logsumexp

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent
sys.path[:0] = [str(ROOT / "phase1")]
import allin_plan as P

START_BANK = 1_268_500
TARGET = 3_000_000
ODDS_FACTOR = 0.80
MODEL_BLEND = 0.05
MAX_TICKETS = 200
MODEL_PATH = HERE / "model.json"


def features(surface):
    rows = surface.get("sanrentan") or []
    win = P.B.win_probabilities(surface)
    odds = np.asarray([float(row[0]) for row in rows])
    combinations = [tuple(nums) for _, nums in rows]
    if (len(rows) < 1 or not win or np.any(odds <= 0) or np.any(~np.isfinite(odds))
            or len(set(combinations)) != len(combinations)):
        raise ValueError("complete trifecta and win markets are required")
    market = 1 / odds
    market /= market.sum()
    harville = np.asarray([P.B.order_probability("sanrentan", list(row[1]), win)
                           for row in rows])
    harville = np.maximum(harville, 1e-15)
    harville /= harville.sum()
    return rows, market, np.column_stack((np.log(market), np.log(harville)))


def probabilities(surface, model):
    rows, market, x = features(surface)
    logits = x @ np.asarray(model["coefficients"])
    learned = np.exp(logits - logsumexp(logits))
    # Limited influence of a small fitted model, with no claimed betting edge.
    mixed = (1 - MODEL_BLEND) * market + MODEL_BLEND * learned
    return rows, market, mixed


def candidates(surface, model):
    rows, market, mixed = probabilities(surface, model)
    wins = sorted(surface["win"], key=lambda row: row[0])
    ranks = {int(nums[0]): i + 1 for i, (_, nums) in enumerate(wins)}
    favorite = int(wins[0][1][0])
    outsider = max(4, min(7, math.ceil(len(ranks) / 2) + 1))
    result = []
    for i, (odds, nums) in enumerate(rows):
        nums = tuple(map(int, nums))
        if (P.MIN_ODDS <= odds <= P.MAX_ODDS and len(set(nums)) == 3
                and nums[0] != favorite and any(ranks.get(n, 99) >= outsider for n in nums)):
            result.append({"nums": nums, "odds": float(odds),
                           "probability": float(mixed[i]), "market_probability": float(market[i])})
    return result


def race_allowance(budget, remaining, total=24):
    """Linear time weights 1..24; missed slots roll forward, last slot uses all."""
    if not 1 <= remaining <= total:
        raise ValueError("invalid number of remaining race slots")
    weight = total - remaining + 1
    return int(budget) * weight // sum(range(weight, total + 1)) // 100 * 100


def allocate(rows, bank, allowance, target=TARGET, odds_factor=ODDS_FACTOR, *, exhaust=False):
    """0/1 knapsack over the best 200 probability/cost candidates, in 100pt units."""
    allowance = min(int(bank), int(allowance)) // 100 * 100
    if bank >= target or allowance < 100:
        return [], {"stake": 0, "probability": 0.0, "reason": "target_met_or_no_budget"}
    required_return = target - bank + allowance
    options = []
    for row in rows:
        money = math.ceil(required_return / (row["odds"] * odds_factor) / 100) * 100
        if 0 < money <= allowance:
            options.append({**row, "money": money})
    options.sort(key=lambda r: (-r["probability"] / r["money"], -r["probability"], r["nums"]))
    options = options[:MAX_TICKETS]
    capacity = allowance // 100
    dp = np.zeros(capacity + 1)
    take = np.zeros((len(options), capacity + 1), dtype=np.bool_)
    for i, row in enumerate(options):
        cost = row["money"] // 100
        proposed = dp[:-cost].copy() + row["probability"]
        improve = proposed > dp[cost:] + 1e-15
        take[i, cost:] = improve
        dp[cost:] = np.maximum(dp[cost:], proposed)
    used = capacity
    picked = []
    for i in range(len(options) - 1, -1, -1):
        if take[i, used]:
            picked.append(options[i])
            used -= options[i]["money"] // 100
    picked.sort(key=lambda r: (-r["probability"], r["nums"]))
    stake = sum(r["money"] for r in picked)
    # Only the final eligible race spends the rounding/shortlist remainder.
    # Every ticket was sized against the FULL allowance, so this cannot lower
    # another ticket's discounted post-hit balance below the target.
    topup = allowance - stake if exhaust and picked else 0
    if topup:
        picked[0] = {**picked[0], "money": picked[0]["money"] + topup}
        stake += topup
    return picked, {"stake": stake, "allowance": allowance,
                    "final_race_topup": topup,
                    "required_return": required_return, "target": target,
                    "probability": sum(r["probability"] for r in picked),
                    "market_probability": sum(r["market_probability"] for r in picked),
                    "odds_factor": odds_factor, "tickets": len(picked),
                    "minimum_discounted_bank_on_hit": min(
                        (bank-stake+r["money"]*r["odds"]*odds_factor for r in picked), default=bank)}


def historical():
    # The original location, inside the private research repository. Kept as
    # written so the inputs listed in model.json can be matched to it.
    panel = Path(os.environ.get("KEIBA_T10_PANEL",
                                "outputs/experiments/JRA-DAILY-LAB/dates"))
    records = []
    for day in ("20260829", "20260830", "20260905", "20260906", "20260912", "20260913", "20260919"):
        for race in sorted((panel / day / "races").glob("*")):
            manifests = sorted(race.glob("snapshots/t10/*/manifest.json"))
            settlements = sorted(race.glob("settlement/*/manifest.json"))
            if not settlements:
                continue
            valid = [p for p in manifests if (lambda d: d.get("market_complete") is True and
                     d.get("complete_all_pool_capture") is True)(json.loads(p.read_text()))]
            if not valid:
                continue
            manifest = valid[0]
            surface, _, _ = P.B.load_surface(manifest)
            if not surface.get("sanrentan") or not surface.get("win"):
                continue
            evidence = json.loads(settlements[0].read_text())
            payouts = evidence.get("payouts_per_100", {}).get("sanrentan", {})
            if not payouts or evidence.get("refunded_horses"):
                continue
            rows, market, x = features(surface)
            winners = {tuple(map(int, key.split("-"))) for key in payouts}
            indexes = [i for i, (_, nums) in enumerate(rows) if tuple(nums) in winners]
            if not indexes:
                continue
            records.append({"date": day, "race_key": race.name, "surface": surface,
                            "x": x, "market": market, "winner_indexes": indexes,
                            "payouts": payouts, "manifest": str(manifest)})
    return records


def train(records):
    training = [r for r in records if r["date"] <= "20260912"]
    holdout = [r for r in records if r["date"] > "20260912"]

    def nll(coef, subset):
        return float(np.mean([logsumexp(r["x"] @ coef) -
                     logsumexp((r["x"] @ coef)[r["winner_indexes"]]) for r in subset]))

    fit = minimize(lambda c: nll(c, training) + .1*((c[0]-1)**2+c[1]**2),
                   [1.0, 0.0], bounds=[(.5, 1.5), (0.0, .5)], method="L-BFGS-B")
    if not fit.success:
        raise ValueError(f"model fitting failed: {fit.message}")
    model = {"model_id": "trifecta-loglinear-trained-through-20260912-v1",
             "coefficients": fit.x.tolist(), "blend": MODEL_BLEND,
             "training_races": len(training), "holdout_races": len(holdout),
             "training_last_date": "20260912", "holdout_dates": ["20260913", "20260919"],
             "market_holdout_nll": nll(np.array([1., 0.]), holdout),
             "model_holdout_nll": nll(fit.x, holdout),
             "training_sources": [{"race_key": r["race_key"], "manifest": r["manifest"],
                                    "sha256": P.B.sha256_file(Path(r["manifest"]))} for r in training],
             "probability_caveat": "Small chronological holdout; no proven probability calibration or positive edge."}
    P.atomic(MODEL_PATH, model)
    return model


if __name__ == "__main__":
    records = historical()
    model = train(records)
    print(json.dumps({k:v for k,v in model.items() if k != "training_sources"}, indent=2))
