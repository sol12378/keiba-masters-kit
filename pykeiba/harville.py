"""Harville ordered-finish probabilities.

Harville tends to understate longshots' place chances, so it is only used
as a second market-derived feature.
"""

from __future__ import annotations

import itertools
from collections.abc import Mapping, Sequence

__all__ = ["order_probability", "combination_probability", "trifecta_surface"]

_EPSILON = 1e-12


def order_probability(order: Sequence[int], win_p: Mapping[int, float]) -> float:
    """Probability that ``order`` is the exact finishing order of its places."""
    remaining = 1.0
    value = 1.0
    seen: set[int] = set()
    for horse in order:
        horse = int(horse)
        if horse in seen:
            return 0.0
        seen.add(horse)
        probability = float(win_p.get(horse, 0.0))
        if remaining <= _EPSILON or probability <= 0.0:
            return 0.0
        value *= probability / remaining
        remaining -= probability
    return value


def combination_probability(
    selection: Sequence[int], win_p: Mapping[int, float], *, ordered: bool
) -> float:
    """Probability of one ticket.

    ``ordered`` is True for exacta/trifecta and False for quinella/trio.
    """
    if len(selection) == 1:
        return order_probability(selection, win_p)
    if ordered:
        return order_probability(selection, win_p)
    return sum(order_probability(order, win_p) for order in itertools.permutations(selection))


def trifecta_surface(win_p: Mapping[int, float]) -> dict[tuple[int, int, int], float]:
    """Harville probability for every ordered triple of the quoted runners."""
    horses = sorted(win_p)
    surface: dict[tuple[int, int, int], float] = {}
    for order in itertools.permutations(horses, 3):
        surface[order] = order_probability(order, win_p)
    return surface
