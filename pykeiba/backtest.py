"""Replay a panel through a strategy.

Tickets settle at the official payout, not the bet-time price. Races with
an unknown settlement are skipped and counted separately.
"""

from __future__ import annotations

from collections.abc import Iterable, Sequence
from dataclasses import dataclass, field

from .allocate import Allocation, CandidateFilter, allocate, candidates, race_allowance
from .dp import PolicyTable, index_table
from .model import Model, build_features
from .panel import Race
from .plan import BET_TYPE_TRIFECTA, bet_id
from .select import pick_near_odds

__all__ = ["RaceOutcome", "BacktestResult", "run_target_strategy", "run_policy_table_strategy"]


@dataclass
class RaceOutcome:
    race_id: str
    date: str
    bank_before: int
    stake: int
    tickets: int
    payout: int
    bank_after: int
    hit: bool
    settled: bool
    note: str = ""


@dataclass
class BacktestResult:
    initial_bank: int
    final_bank: int
    goal: int
    outcomes: list[RaceOutcome] = field(default_factory=list)

    @property
    def races_bet(self) -> int:
        return sum(1 for outcome in self.outcomes if outcome.stake > 0)

    @property
    def hits(self) -> int:
        return sum(1 for outcome in self.outcomes if outcome.hit)

    @property
    def turnover(self) -> int:
        return sum(outcome.stake for outcome in self.outcomes)

    @property
    def payouts(self) -> int:
        return sum(outcome.payout for outcome in self.outcomes)

    @property
    def unsettled(self) -> int:
        return sum(1 for outcome in self.outcomes if outcome.stake > 0 and not outcome.settled)

    def summary(self) -> dict:
        return {
            "initial_bank": self.initial_bank,
            "final_bank": self.final_bank,
            "goal": self.goal,
            "reached_goal": self.final_bank >= self.goal,
            "races_seen": len(self.outcomes),
            "races_bet": self.races_bet,
            "hits": self.hits,
            "hit_rate": (self.hits / self.races_bet) if self.races_bet else 0.0,
            "turnover": self.turnover,
            "payouts": self.payouts,
            "return_on_turnover": (self.payouts / self.turnover) if self.turnover else 0.0,
            "unsettled_races_bet": self.unsettled,
        }


def _settle(race: Race, bought: Sequence[tuple[tuple[int, int, int], int]]) -> tuple[int, bool, bool]:
    """Return ``(payout, hit, settled)`` for the tickets bought in one race."""
    if not race.settled:
        return 0, False, False
    payout = 0
    hit = False
    for combination, stake in bought:
        per_100 = race.payout_per_100(combination)
        if per_100 is None:
            return 0, False, False
        if per_100 > 0:
            payout += int(round(per_100 * stake / 100.0))
            hit = True
    return payout, hit, True


def _no_bet(result: BacktestResult, race: Race, bank: int, note: str) -> None:
    """Record a race the strategy did not bet, with the reason it did not."""
    result.outcomes.append(
        RaceOutcome(
            race_id=race.race_id,
            date=race.date,
            bank_before=bank,
            stake=0,
            tickets=0,
            payout=0,
            bank_after=bank,
            hit=False,
            settled=race.settled,
            note=note,
        )
    )


def run_target_strategy(
    races: Iterable[Race],
    model: Model,
    *,
    initial_bank: int,
    goal: int,
    rule: CandidateFilter | None = None,
    odds_factor: float = 0.80,
    total_slots: int | None = None,
    freeze_on_profit: bool = True,
) -> BacktestResult:
    """The target-balance strategy: size every ticket to close the gap to
    ``goal`` in one hit, and stop once a hit has made the day profitable."""
    rule = rule or CandidateFilter()
    races = list(races)
    total_slots = total_slots or max(1, len(races))
    bank = int(initial_bank)
    result = BacktestResult(initial_bank=bank, final_bank=bank, goal=goal)
    frozen = False

    for index, race in enumerate(races):
        remaining = total_slots - index
        if frozen or remaining < 1:
            _no_bet(result, race, bank, "frozen")
            continue
        try:
            features = build_features(race)
        except ValueError as error:
            _no_bet(result, race, bank, str(error))
            continue
        shortlist = candidates(race, features, model, rule)
        budget = max(0, bank - 0)
        allowance = race_allowance(budget, remaining, total_slots)
        plan: Allocation = allocate(
            shortlist,
            bank=bank,
            allowance=allowance,
            target=goal,
            odds_factor=odds_factor,
            spend_remainder=(remaining == 1),
        )
        if not plan.tickets:
            _no_bet(result, race, bank, plan.reason)
            continue
        bought = [(ticket.combination, ticket.stake) for ticket in plan.tickets]
        payout, hit, settled = _settle(race, bought)
        bank_before = bank
        bank = bank - plan.stake + payout
        result.outcomes.append(
            RaceOutcome(race.race_id, race.date, bank_before, plan.stake, len(bought), payout, bank, hit, settled)
        )
        if hit and freeze_on_profit and bank > initial_bank:
            frozen = True

    result.final_bank = bank
    return result


def run_policy_table_strategy(
    races: Iterable[Race],
    model: Model,
    table: PolicyTable,
    *,
    initial_bank: int,
    model_weight: float = 0.05,
) -> BacktestResult:
    """The state-dependent table: look up (races remaining, bank), buy the
    combinations priced closest to the band the table names."""
    races = list(races)
    rows = index_table(table)
    grid = int(table.spec["bank_grid"])
    goal = int(table.spec["goal"])
    horizon = int(table.spec["races"])
    bank = int(initial_bank)
    result = BacktestResult(initial_bank=bank, final_bank=bank, goal=goal)

    for index, race in enumerate(races):
        remaining = max(1, min(horizon, horizon - index))
        bucket = min(int(bank) // grid * grid, max(table.banks))
        row = rows.get((remaining, bucket))
        if bank >= goal:
            _no_bet(result, race, bank, "goal_reached")
            continue
        if row is None or row["action"] != "bet":
            _no_bet(result, race, bank, "table_says_pass")
            continue
        try:
            features = build_features(race)
        except ValueError as error:
            _no_bet(result, race, bank, str(error))
            continue
        picks = pick_near_odds(
            features,
            race.trifecta_odds,
            model,
            target_odds=float(row["target_odds"]),
            count=int(row["tickets"]),
            model_weight=model_weight,
        )
        stake_each = int(row["stake_each"])
        total = stake_each * len(picks)
        if not picks or stake_each < 100 or total > bank:
            _no_bet(result, race, bank, "unaffordable")
            continue
        bought = [(pick.combination, stake_each) for pick in picks]
        payout, hit, settled = _settle(race, bought)
        bank_before = bank
        bank = bank - total + payout
        result.outcomes.append(
            RaceOutcome(race.race_id, race.date, bank_before, total, len(bought), payout, bank, hit, settled)
        )

    result.final_bank = bank
    return result


def bet_ids_for(outcome_combinations: Iterable[tuple[int, int, int]]) -> list[str]:
    """Bet ids for a set of trifecta combinations, for handing to the daemon."""
    return [bet_id(BET_TYPE_TRIFECTA, combination) for combination in outcome_combinations]
