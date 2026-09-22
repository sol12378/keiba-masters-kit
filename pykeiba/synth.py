"""A synthetic season, so the pipeline runs with no data of its own.

This repository ships no race data.  Historical odds and results are covered by
third-party terms, and the bulk captures behind the original work are far too
large to distribute.  What it ships instead is a generator that produces the
same panel shape with known ground truth, which is better than real data for
three things: the whole pipeline runs on a fresh clone, tests are deterministic,
and you can check the estimators against the truth that produced the sample.

The generator is deliberately honest about the market:

* Quoted prices come from a market that is *nearly* efficient — its
  probabilities are the true ones perturbed by noise, then marked up by a
  takeout.  There is no exploitable edge planted in the data.
* Winning tickets settle **below** their decision-time price by default.
  Money keeps arriving after you bet, so the pool you are paid from is larger
  than the one you priced against.  This is what the allocator's
  ``odds_factor`` exists to absorb; leaving it out of the simulation would make
  every strategy look better than it is.
"""

from __future__ import annotations

import itertools
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path

import numpy as np

from .harville import order_probability
from .panel import write_race

__all__ = ["SeasonSpec", "generate_season"]


@dataclass(frozen=True)
class SeasonSpec:
    days: int = 6
    races_per_day: int = 12
    min_runners: int = 8
    max_runners: int = 12
    #: Fraction of turnover returned to bettors, per pool.
    win_return_rate: float = 0.80
    trifecta_return_rate: float = 0.75
    #: Standard deviation of the market's log-probability error.  Zero makes
    #: the market exactly right; larger values make it noisier, never sharper.
    market_noise: float = 0.25
    #: Median of the settlement drift applied to a winning ticket's price.
    #: Below 1 means winners pay less than the price you saw.
    settlement_drift_median: float = 0.85
    settlement_drift_sigma: float = 0.20
    start_date: str = "20260404"
    seed: int = 7


def _dates(spec: SeasonSpec) -> list[str]:
    start = datetime.strptime(spec.start_date, "%Y%m%d")
    return [(start + timedelta(days=7 * index)).strftime("%Y%m%d") for index in range(spec.days)]


def _true_probabilities(rng: np.random.Generator, runners: int) -> np.ndarray:
    strengths = rng.lognormal(mean=0.0, sigma=0.9, size=runners)
    return strengths / strengths.sum()


def _market_probabilities(rng: np.random.Generator, truth: np.ndarray, noise: float) -> np.ndarray:
    if noise <= 0:
        return truth.copy()
    perturbed = np.log(truth) + rng.normal(0.0, noise, size=truth.size)
    perturbed = np.exp(perturbed - perturbed.max())
    return perturbed / perturbed.sum()


def _sample_finish(rng: np.random.Generator, truth: np.ndarray, places: int = 3) -> list[int]:
    """Plackett-Luce draw: repeatedly pick from the remaining field."""
    remaining = list(range(truth.size))
    weights = truth.copy()
    finish: list[int] = []
    for _ in range(places):
        probabilities = weights[remaining] / weights[remaining].sum()
        choice = rng.choice(len(remaining), p=probabilities)
        finish.append(remaining.pop(int(choice)))
    return finish


def generate_season(spec: SeasonSpec, out_dir: Path) -> dict:
    """Write a full panel under ``out_dir`` and return a summary."""
    rng = np.random.default_rng(spec.seed)
    out_dir = Path(out_dir)
    written = 0
    combination_count = 0

    for day_index, date in enumerate(_dates(spec)):
        day_start = datetime.strptime(date, "%Y%m%d").replace(hour=1, minute=0, tzinfo=UTC)
        for race_number in range(1, spec.races_per_day + 1):
            runners = int(rng.integers(spec.min_runners, spec.max_runners + 1))
            truth = _true_probabilities(rng, runners)
            market = _market_probabilities(rng, truth, spec.market_noise)
            horses = list(range(1, runners + 1))

            win_odds = {
                str(horse): round(float(spec.win_return_rate / market[index]), 1)
                for index, horse in enumerate(horses)
            }
            market_win_p = {horse: float(market[index]) for index, horse in enumerate(horses)}

            trifecta_odds: dict[str, float] = {}
            for triple in itertools.permutations(horses, 3):
                probability = order_probability(triple, market_win_p)
                if probability <= 0:
                    continue
                trifecta_odds["-".join(map(str, triple))] = round(
                    float(spec.trifecta_return_rate / probability), 1
                )
            combination_count += len(trifecta_odds)

            finish_indices = _sample_finish(rng, truth)
            finish = [horses[index] for index in finish_indices]
            label = "-".join(map(str, finish))
            quoted = trifecta_odds.get(label)
            payout: dict[str, float] = {}
            if quoted is not None:
                drift = float(
                    rng.lognormal(mean=np.log(spec.settlement_drift_median), sigma=spec.settlement_drift_sigma)
                )
                payout[label] = round(max(1.0, quoted * drift) * 100.0, 1)

            post_at = day_start + timedelta(minutes=30 * race_number)
            write_race(
                out_dir,
                {
                    "race_id": f"{date}{day_index % 10:02d}{race_number:02d}",
                    "date": date,
                    "post_at": post_at.strftime("%Y-%m-%dT%H:%M:%SZ"),
                    "captured_at": (post_at - timedelta(minutes=10)).strftime("%Y-%m-%dT%H:%M:%SZ"),
                    "win": win_odds,
                    "trifecta": trifecta_odds,
                    "result": {"finish": finish, "payout_per_100": payout},
                },
            )
            written += 1

    return {
        "races": written,
        "dates": _dates(spec),
        "trifecta_combinations": combination_count,
        "seed": spec.seed,
        "note": (
            "Synthetic data. The market is nearly efficient by construction and "
            "winning tickets settle below their quoted price; no edge is planted."
        ),
    }
