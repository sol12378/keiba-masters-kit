"""Choosing which combinations to buy at a price the policy table fixed.

The table says *what price to buy*.  Which combinations at that price is a
separate, much smaller question: candidates within a band sit within a fraction
of a percent of each other, so the choice barely moves the expected return.  A
small model weight is enough to decide it, and keeping the weight small is
deliberate — it keeps the price, not the model, responsible for the outcome.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from .model import Model, RaceFeatures, blended_probabilities

__all__ = ["Selection", "pick_near_odds", "DEFAULT_MODEL_WEIGHT"]

#: Weight on the probability ranking when breaking ties inside a price band.
DEFAULT_MODEL_WEIGHT = 0.05


@dataclass(frozen=True)
class Selection:
    combination: tuple[int, int, int]
    odds: float
    probability: float


def pick_near_odds(
    features: RaceFeatures,
    odds_by_combination: dict[tuple[int, int, int], float],
    model: Model,
    *,
    target_odds: float,
    count: int,
    model_weight: float = DEFAULT_MODEL_WEIGHT,
) -> list[Selection]:
    """The ``count`` combinations priced closest to ``target_odds``.

    Ranking mixes distance-in-price (weight ``1 - model_weight``) with the
    model's probability ranking (weight ``model_weight``).  Ranks, not raw
    values, are mixed so the two scales cannot fight each other.
    """
    if count < 1:
        raise ValueError(f"count must be at least 1, got {count}")
    if not 0.0 <= model_weight <= 1.0:
        raise ValueError(f"model_weight must be within [0, 1], got {model_weight}")
    combinations = features.combinations
    if not combinations:
        return []

    probabilities = blended_probabilities(features, model)
    prices = np.array([odds_by_combination[combination] for combination in combinations])
    price_distance = np.abs(np.log(prices) - np.log(target_odds))

    price_rank = np.empty(len(combinations), dtype=float)
    price_rank[np.argsort(price_distance, kind="stable")] = np.arange(len(combinations))
    model_rank = np.empty(len(combinations), dtype=float)
    model_rank[np.argsort(-probabilities, kind="stable")] = np.arange(len(combinations))

    score = (1.0 - model_weight) * price_rank + model_weight * model_rank
    order = np.argsort(score, kind="stable")[:count]
    return [
        Selection(
            combination=combinations[index],
            odds=float(prices[index]),
            probability=float(probabilities[index]),
        )
        for index in order
    ]
