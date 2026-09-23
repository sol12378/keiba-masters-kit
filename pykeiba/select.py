"""Pick combinations close to the target odds set by the policy table."""

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
    """Return the ``count`` combinations closest to ``target_odds``.

    Ranking mixes price distance (1 - model_weight) and model probability
    (model_weight). Ranks are mixed rather than raw values.
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
