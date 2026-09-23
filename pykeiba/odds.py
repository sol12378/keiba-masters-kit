"""Convert odds to market probabilities with the overround removed."""

from __future__ import annotations

from collections.abc import Mapping

__all__ = ["overround", "win_probabilities", "normalized_probabilities"]


def _validate(odds: Mapping[int, float]) -> None:
    if not odds:
        raise ValueError("no prices were quoted")
    for key, value in odds.items():
        if not isinstance(key, int):
            raise TypeError(f"selection keys must be int, got {key!r}")
        if not (value > 0) or value != value or value in (float("inf"),):
            raise ValueError(f"price for {key} must be a finite positive number, got {value!r}")


def overround(odds: Mapping[int, float]) -> float:
    """Sum of implied probabilities.  Above 1 by the takeout the pool applies."""
    _validate(odds)
    return sum(1.0 / float(value) for value in odds.values())


def win_probabilities(win_odds: Mapping[int, float]) -> dict[int, float]:
    """Overround-free win probabilities keyed by horse number.

    Uses proportional normalisation, which has no fitted parameter.
    """
    _validate(win_odds)
    implied = {int(horse): 1.0 / float(price) for horse, price in win_odds.items()}
    total = sum(implied.values())
    return {horse: value / total for horse, value in implied.items()}


def normalized_probabilities(odds: Mapping[str, float]) -> dict[str, float]:
    """Overround-free probabilities for a combination pool, keyed by the pool's
    own combination label (for example ``"5-2-7"``)."""
    if not odds:
        raise ValueError("no prices were quoted")
    implied = {}
    for label, price in odds.items():
        value = float(price)
        if not (value > 0) or value != value:
            raise ValueError(f"price for {label} must be a finite positive number, got {price!r}")
        implied[label] = 1.0 / value
    total = sum(implied.values())
    return {label: value / total for label, value in implied.items()}
