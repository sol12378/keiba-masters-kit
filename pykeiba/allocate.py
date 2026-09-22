"""Turning probabilities into an amount to stake on which combinations.

The allocation answers one question per race: *given a bank, a target balance
and a budget for this race, which tickets maximize the chance that a hit puts
the bank at or above the target?*

Two things are worth being explicit about, because they are easy to overstate:

* Probabilities are used only to rank candidates and to size the shortlist.
  The target balance, the candidate filter and the stopping rule are separate
  decision rules.  A hit does not mean "the model predicted the winner".
* The knapsack is optimal *within one race's budget*.  It is not a globally
  optimal multi-race or multi-player strategy.
"""

from __future__ import annotations

import math
from collections.abc import Sequence
from dataclasses import dataclass

import numpy as np

from .model import Model, RaceFeatures, blended_probabilities
from .odds import win_probabilities
from .panel import Race

__all__ = ["Candidate", "Allocation", "CandidateFilter", "candidates", "race_allowance", "allocate"]

#: Fraction of the quoted price assumed to survive to settlement.  Winning
#: tickets settle below their decision-time price because the pool moves after
#: you bet; sizing against the full quote systematically undershoots the target.
DEFAULT_ODDS_FACTOR = 0.80

#: Points per unit.  Stakes are always whole multiples of this.
STAKE_UNIT = 100

#: Shortlist size handed to the knapsack.
MAX_SHORTLIST = 200


@dataclass(frozen=True)
class CandidateFilter:
    """Which combinations are allowed to be bought at all.

    The defaults describe a longshot policy: a price band, the market favourite
    excluded from the winning position, and at least one unfancied runner in
    the combination.  They are a *policy choice*, not a finding — change them
    and the whole character of the strategy changes.
    """

    min_odds: float = 375.0
    max_odds: float = 8000.0
    exclude_favourite_first: bool = True
    require_outsider: bool = True

    def outsider_rank(self, runners: int) -> int:
        return max(4, min(7, math.ceil(runners / 2) + 1))


@dataclass(frozen=True)
class Candidate:
    combination: tuple[int, int, int]
    odds: float
    probability: float
    market_probability: float


@dataclass(frozen=True)
class Ticket:
    combination: tuple[int, int, int]
    odds: float
    probability: float
    stake: int


@dataclass(frozen=True)
class Allocation:
    tickets: list[Ticket]
    stake: int
    allowance: int
    required_return: float
    target: int
    hit_probability: float
    market_hit_probability: float
    minimum_bank_on_hit: float
    reason: str = ""


def candidates(race: Race, features: RaceFeatures, model: Model, rule: CandidateFilter) -> list[Candidate]:
    """Combinations that pass ``rule``, carrying their blended probability."""
    mixed = blended_probabilities(features, model)
    win_p = win_probabilities(race.win_odds)
    by_price = sorted(race.win_odds.items(), key=lambda item: item[1])
    rank = {int(horse): index + 1 for index, (horse, _) in enumerate(by_price)}
    favourite = int(by_price[0][0])
    threshold = rule.outsider_rank(len(win_p))

    result: list[Candidate] = []
    for index, combination in enumerate(features.combinations):
        odds = float(race.trifecta_odds[combination])
        if not rule.min_odds <= odds <= rule.max_odds:
            continue
        if rule.exclude_favourite_first and combination[0] == favourite:
            continue
        if rule.require_outsider and not any(rank.get(horse, 99) >= threshold for horse in combination):
            continue
        result.append(
            Candidate(
                combination=combination,
                odds=odds,
                probability=float(mixed[index]),
                market_probability=float(features.market[index]),
            )
        )
    return result


def race_allowance(budget: int, remaining_slots: int, total_slots: int) -> int:
    """Split ``budget`` across the remaining races with linear time weights.

    Slot *i* of *n* gets weight ``i``, so later races get more.  A slot that
    goes unused rolls its share forward, and the last slot may spend the rest.
    """
    if not 1 <= remaining_slots <= total_slots:
        raise ValueError(f"remaining_slots must be within [1, {total_slots}], got {remaining_slots}")
    weight = total_slots - remaining_slots + 1
    divisor = sum(range(weight, total_slots + 1))
    return int(budget) * weight // divisor // STAKE_UNIT * STAKE_UNIT


def allocate(
    shortlist: Sequence[Candidate],
    *,
    bank: int,
    allowance: int,
    target: int,
    odds_factor: float = DEFAULT_ODDS_FACTOR,
    max_shortlist: int = MAX_SHORTLIST,
    spend_remainder: bool = False,
) -> Allocation:
    """0/1 knapsack over the shortlist, in ``STAKE_UNIT`` units.

    Each ticket is sized so that, on its own, a hit lands the bank at or above
    ``target``.  Because every ticket in a race is mutually exclusive, the
    objective is simply the sum of the chosen tickets' probabilities.
    """
    allowance = min(int(bank), int(allowance)) // STAKE_UNIT * STAKE_UNIT
    if bank >= target:
        return Allocation([], 0, allowance, 0.0, target, 0.0, 0.0, float(bank), "target_already_met")
    if allowance < STAKE_UNIT:
        return Allocation([], 0, allowance, 0.0, target, 0.0, 0.0, float(bank), "no_budget")
    if not 0.0 < odds_factor <= 1.0:
        raise ValueError(f"odds_factor must be within (0, 1], got {odds_factor}")

    required_return = target - bank + allowance
    options: list[tuple[Candidate, int]] = []
    for candidate in shortlist:
        stake = math.ceil(required_return / (candidate.odds * odds_factor) / STAKE_UNIT) * STAKE_UNIT
        if 0 < stake <= allowance:
            options.append((candidate, int(stake)))
    if not options:
        return Allocation([], 0, allowance, required_return, target, 0.0, 0.0, float(bank), "no_affordable_ticket")

    options.sort(key=lambda item: (-item[0].probability / item[1], -item[0].probability, item[0].combination))
    options = options[:max_shortlist]

    capacity = allowance // STAKE_UNIT
    value = np.zeros(capacity + 1)
    chosen = np.zeros((len(options), capacity + 1), dtype=np.bool_)
    for index, (candidate, stake) in enumerate(options):
        cost = stake // STAKE_UNIT
        if cost > capacity:
            continue
        proposed = value[:-cost].copy() + candidate.probability
        improved = proposed > value[cost:] + 1e-15
        chosen[index, cost:] = improved
        value[cost:] = np.maximum(value[cost:], proposed)

    used = capacity
    picked: list[tuple[Candidate, int]] = []
    for index in range(len(options) - 1, -1, -1):
        if chosen[index, used]:
            candidate, stake = options[index]
            picked.append((candidate, stake))
            used -= stake // STAKE_UNIT
    picked.sort(key=lambda item: (-item[0].probability, item[0].combination))

    total_stake = sum(stake for _, stake in picked)
    if spend_remainder and picked and allowance > total_stake:
        # Only a race that will not be followed by another may spend the
        # rounding remainder: every ticket was already sized against the full
        # allowance, so topping one up cannot pull another below the target.
        candidate, stake = picked[0]
        picked[0] = (candidate, stake + (allowance - total_stake))
        total_stake = allowance

    tickets = [
        Ticket(combination=candidate.combination, odds=candidate.odds, probability=candidate.probability, stake=stake)
        for candidate, stake in picked
    ]
    minimum_bank_on_hit = min(
        (bank - total_stake + ticket.stake * ticket.odds * odds_factor for ticket in tickets),
        default=float(bank),
    )
    return Allocation(
        tickets=tickets,
        stake=total_stake,
        allowance=allowance,
        required_return=float(required_return),
        target=int(target),
        hit_probability=float(sum(ticket.probability for ticket in tickets)),
        market_hit_probability=float(sum(candidate.market_probability for candidate, _ in picked)),
        minimum_bank_on_hit=float(minimum_bank_on_hit),
    )
