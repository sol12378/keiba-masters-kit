"""The two-column log-linear model.

Features, for every ordered triple quoted in a race:

1. ``log`` of the trifecta pool's own overround-free probability
2. ``log`` of the Harville probability implied by the win pool

Both columns are market-derived; the model only learns how much weight to put
on each.  Fitting is a regularized conditional maximum likelihood over the
races' winning combination — a single softmax per race, so the fit sees one
observation per race, not one per ticket.

This model is small on purpose.  With two coefficients and a few hundred races
it cannot be expected to beat the market, and the shipped defaults make the
market column dominate.  Treat any holdout improvement as small until your own
evaluation says otherwise.
"""

from __future__ import annotations

import json
from collections.abc import Iterable, Sequence
from dataclasses import asdict, dataclass
from pathlib import Path

import numpy as np
from scipy.optimize import minimize
from scipy.special import logsumexp

from .harville import order_probability
from .odds import normalized_probabilities, win_probabilities
from .panel import Race

__all__ = ["RaceFeatures", "Model", "build_features", "train", "negative_log_likelihood", "blended_probabilities"]

#: Weight given to the fitted model when blending it with the market column.
DEFAULT_BLEND = 0.05
_FLOOR = 1e-15


@dataclass(frozen=True)
class RaceFeatures:
    """Design matrix for one race, plus the index of the winning combination."""

    race_id: str
    date: str
    combinations: list[tuple[int, int, int]]
    x: np.ndarray  # (n_combinations, 2)
    market: np.ndarray  # (n_combinations,)
    winner_index: int | None


@dataclass
class Model:
    model_id: str
    coefficients: list[float]
    blend: float
    training_races: int
    holdout_races: int
    training_dates: list[str]
    holdout_dates: list[str]
    market_holdout_nll: float | None
    model_holdout_nll: float | None
    caveat: str = (
        "Two market-derived columns fitted on a small chronological sample. "
        "No proven probability calibration and no demonstrated positive edge."
    )

    def to_json(self, path: Path) -> Path:
        path = Path(path)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(asdict(self), ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        return path

    @staticmethod
    def from_json(path: Path) -> Model:
        document = json.loads(Path(path).read_text(encoding="utf-8"))
        known = {field for field in Model.__dataclass_fields__}
        return Model(**{key: value for key, value in document.items() if key in known})


def build_features(race: Race) -> RaceFeatures:
    """Two log-probability columns for every quoted triple in one race.

    Raises ``ValueError`` when the race lacks a complete pair of markets; a
    partially captured race is dropped rather than imputed.
    """
    if not race.trifecta_odds:
        raise ValueError(f"{race.race_id}: no trifecta market was captured")
    win_p = win_probabilities(race.win_odds)
    labels = {"-".join(map(str, triple)): price for triple, price in race.trifecta_odds.items()}
    market_by_label = normalized_probabilities(labels)

    combinations = sorted(race.trifecta_odds)
    market = np.array([market_by_label["-".join(map(str, triple))] for triple in combinations])
    harville = np.array([order_probability(triple, win_p) for triple in combinations])
    harville = np.maximum(harville, _FLOOR)
    harville = harville / harville.sum()

    winner = race.winning_triple
    winner_index = combinations.index(winner) if winner in race.trifecta_odds else None
    return RaceFeatures(
        race_id=race.race_id,
        date=race.date,
        combinations=combinations,
        x=np.column_stack((np.log(np.maximum(market, _FLOOR)), np.log(harville))),
        market=market,
        winner_index=winner_index,
    )


def negative_log_likelihood(coefficients: Sequence[float], features: Iterable[RaceFeatures]) -> float:
    """Mean per-race conditional NLL of the winning combination."""
    coefficients = np.asarray(coefficients, dtype=float)
    terms = []
    for item in features:
        if item.winner_index is None:
            continue
        logits = item.x @ coefficients
        terms.append(float(logsumexp(logits) - logits[item.winner_index]))
    if not terms:
        raise ValueError("no settled races with a quoted winning combination")
    return float(np.mean(terms))


def train(
    features: Sequence[RaceFeatures],
    *,
    holdout_from: str | None = None,
    ridge: float = 0.1,
    bounds: Sequence[tuple[float, float]] = ((0.5, 1.5), (0.0, 0.5)),
    model_id: str = "trifecta-loglinear-v1",
    blend: float = DEFAULT_BLEND,
) -> Model:
    """Fit the two coefficients on a chronological split.

    ``holdout_from`` is a ``YYYYMMDD`` date: races on or after it are held out
    and never touched by the optimizer.  Pass ``None`` to fit on everything,
    which produces a model with no holdout evidence attached.

    The ridge term pulls the market coefficient toward 1 and the Harville
    coefficient toward 0, i.e. toward "use the quoted price and nothing else".
    Moving away from that prior has to be paid for by likelihood.
    """
    usable = [item for item in features if item.winner_index is not None]
    if not usable:
        raise ValueError("no settled races with a quoted winning combination")
    if holdout_from is None:
        training, holdout = usable, []
    else:
        training = [item for item in usable if item.date < holdout_from]
        holdout = [item for item in usable if item.date >= holdout_from]
    if not training:
        raise ValueError(f"every settled race falls on or after holdout_from={holdout_from}")

    def objective(coefficients: np.ndarray) -> float:
        penalty = ridge * ((coefficients[0] - 1.0) ** 2 + coefficients[1] ** 2)
        return negative_log_likelihood(coefficients, training) + penalty

    fit = minimize(objective, np.array([1.0, 0.0]), bounds=list(bounds), method="L-BFGS-B")
    if not fit.success:
        raise ValueError(f"model fitting failed: {fit.message}")

    market_nll = negative_log_likelihood([1.0, 0.0], holdout) if holdout else None
    model_nll = negative_log_likelihood(fit.x, holdout) if holdout else None
    return Model(
        model_id=model_id,
        coefficients=[float(value) for value in fit.x],
        blend=float(blend),
        training_races=len(training),
        holdout_races=len(holdout),
        training_dates=sorted({item.date for item in training}),
        holdout_dates=sorted({item.date for item in holdout}),
        market_holdout_nll=market_nll,
        model_holdout_nll=model_nll,
    )


def blended_probabilities(features: RaceFeatures, model: Model) -> np.ndarray:
    """``(1 - blend)`` market plus ``blend`` fitted model, per combination."""
    logits = features.x @ np.asarray(model.coefficients, dtype=float)
    learned = np.exp(logits - logsumexp(logits))
    blend = float(model.blend)
    if not 0.0 <= blend <= 1.0:
        raise ValueError(f"blend must be within [0, 1], got {blend}")
    return (1.0 - blend) * features.market + blend * learned
