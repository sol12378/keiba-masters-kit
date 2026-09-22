"""The state-dependent policy table.

A tournament is not the same problem as a betting edge.  With a fixed number of
races and a bank that must reach some level for the entry to place, the right
action depends on *where you are*: how many races are left and how much you
hold.  This module solves that by backward induction over

    state  = (races remaining, bank)
    action = (price band, number of tickets, stake per ticket)

and writes the argmax out as a table the voting side can simply look up, so no
optimizer has to run at post time.

What the solver does *not* do
-----------------------------
It assumes no edge.  Each price band carries a return rate ``R`` — the fraction
of turnover the band pays back — and the hit probability of one ticket at odds
``O`` is taken as ``R / O``.  With ``R < 1`` every action loses money in
expectation; the table wins only in the sense of maximizing the chance of
reaching the goal, by choosing *when* to accept variance.  Feeding it ``R > 1``
means you are asserting an edge, and the table will happily believe you.

The shipped default is a flat statutory-takeout figure.  Measure your own
return rates before trusting any number here.
"""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass, field
from pathlib import Path

import numpy as np

__all__ = [
    "PriceBand",
    "PolicySpec",
    "PolicyTable",
    "Action",
    "DEFAULT_BANDS",
    "solve",
    "lookup",
    "simulate_table",
]

#: Stake sizing rules, as fractions.  ``gap`` rules size the ticket so a hit
#: closes the distance to the goal; ``bank`` rules stake a fraction of what is
#: held.  The solver picks between them per state.
DEFAULT_GAP_FRACTIONS = (1.0, 1.2, 1.5, 2.0)
DEFAULT_BANK_FRACTIONS = (0.01, 0.02, 0.05, 0.10, 0.25)
DEFAULT_TICKET_CHOICES = (1, 2, 3, 5, 10)


@dataclass(frozen=True)
class PriceBand:
    """One price band the policy is allowed to buy.

    ``return_rate`` is the fraction of turnover the band pays back.  Below 1
    for any real pari-mutuel pool.
    """

    label: str
    odds: float
    return_rate: float
    max_tickets: int = 10

    def __post_init__(self) -> None:
        if self.odds <= 1.0:
            raise ValueError(f"{self.label}: odds must exceed 1.0")
        if not 0.0 < self.return_rate <= 1.5:
            raise ValueError(f"{self.label}: return_rate must be within (0, 1.5]")
        if self.max_tickets < 1:
            raise ValueError(f"{self.label}: max_tickets must be at least 1")


#: A neutral starting point: Japanese pari-mutuel pools return roughly 70-80%
#: of turnover depending on the pool.  These are NOT measurements of any
#: particular band's realized return; replace them with your own.
DEFAULT_BANDS: tuple[PriceBand, ...] = (
    PriceBand("win-mid", 10.0, 0.80, max_tickets=3),
    PriceBand("exotic-100", 100.0, 0.75, max_tickets=5),
    PriceBand("exotic-500", 500.0, 0.75, max_tickets=10),
    PriceBand("exotic-1500", 1500.0, 0.75, max_tickets=10),
    PriceBand("exotic-5000", 5000.0, 0.72, max_tickets=10),
)


@dataclass
class PolicySpec:
    """Everything the solver needs.  Serialized into the table's metadata."""

    goal: int
    races: int
    initial_bank: int = 1_000_000
    bank_grid: int = 25_000
    bank_max: int | None = None
    per_race_cap: int = 20_000
    max_tickets: int = 10
    stake_unit: int = 100
    #: Minimum points that must be staked in a race, as ``(from_race, points)``
    #: pairs applied in order.  Models a turnover requirement.
    floor_schedule: tuple[tuple[int, int], ...] = ()
    bands: tuple[PriceBand, ...] = DEFAULT_BANDS
    gap_fractions: tuple[float, ...] = DEFAULT_GAP_FRACTIONS
    bank_fractions: tuple[float, ...] = DEFAULT_BANK_FRACTIONS
    ticket_choices: tuple[int, ...] = DEFAULT_TICKET_CHOICES
    #: ``reach`` maximizes P(final bank >= goal).  ``bank`` maximizes the
    #: expected final bank, capped at the goal.
    objective: str = "reach"

    def __post_init__(self) -> None:
        if self.goal <= self.initial_bank:
            raise ValueError("goal must exceed initial_bank for the problem to be non-trivial")
        if self.races < 1:
            raise ValueError("races must be at least 1")
        if self.objective not in ("reach", "bank"):
            raise ValueError(f"unknown objective {self.objective!r}")
        if self.bank_max is None:
            self.bank_max = int(self.goal * 1.25 // self.bank_grid * self.bank_grid)
        if not self.bands:
            raise ValueError("at least one price band is required")

    def floor_for(self, race_index: int) -> int:
        """Minimum stake required in race ``race_index`` (1-based)."""
        floor = 0
        for start, points in self.floor_schedule:
            if race_index >= start:
                floor = points
        return floor


@dataclass(frozen=True)
class Action:
    """What the table instructs in one state."""

    band: str
    target_odds: float
    tickets: int
    stake_each: int
    total_spend: int
    value: float

    @property
    def is_pass(self) -> bool:
        return self.tickets == 0


@dataclass
class PolicyTable:
    schema: str
    spec: dict
    banks: list[int]
    expected: dict
    rows: list[dict] = field(default_factory=list)

    def to_json(self, path: Path) -> Path:
        path = Path(path)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(asdict(self), ensure_ascii=False, indent=1, sort_keys=True) + "\n", encoding="utf-8")
        return path

    @staticmethod
    def from_json(path: Path) -> PolicyTable:
        document = json.loads(Path(path).read_text(encoding="utf-8"))
        return PolicyTable(
            schema=document["schema"],
            spec=document["spec"],
            banks=document["banks"],
            expected=document["expected"],
            rows=document["rows"],
        )


def _terminal_value(banks: np.ndarray, spec: PolicySpec) -> np.ndarray:
    if spec.objective == "reach":
        return (banks >= spec.goal).astype(float)
    return np.minimum(banks / float(spec.goal), 1.0)


def _enumerate_actions(banks: np.ndarray, spec: PolicySpec, floor: int):
    """Vectorized action set for one stage.

    Returns stacked arrays over (action, bank) for stake, spend, hit
    probability, and the *continuous* bank reached on a hit and on a miss.
    The next bank is kept as a real number on purpose: rounding it to the grid
    would let the solver mint money out of its own discretization, which shows
    up as a reported probability above the conservation bound.
    """
    unit = spec.stake_unit
    gap = np.maximum(0.0, spec.goal - banks)

    stake_rows, spend_rows, probability_rows, up_rows, down_rows, meta = [], [], [], [], [], []
    for band in spec.bands:
        single_hit = min(0.999, band.return_rate / band.odds)
        for tickets in spec.ticket_choices:
            if tickets > min(band.max_tickets, spec.max_tickets):
                continue
            probability = min(0.999, single_hit * tickets)
            rules = [("gap", fraction) for fraction in spec.gap_fractions]
            rules += [("bank", fraction) for fraction in spec.bank_fractions]
            for kind, fraction in rules:
                if kind == "gap":
                    stake = np.where(gap > 0, gap / (band.odds - 1.0) * fraction, 0.0)
                else:
                    stake = banks * fraction / tickets
                stake = np.ceil(stake / unit) * unit
                stake = np.minimum(stake, spec.per_race_cap / tickets)
                spend = stake * tickets
                over = spend > banks
                stake = np.where(over, np.floor(banks / tickets / unit) * unit, stake)
                spend = stake * tickets
                invalid = (stake < unit) | (spend > banks) | (spend < floor)
                stake_rows.append(np.where(invalid, 0.0, stake))
                spend_rows.append(np.where(invalid, 0.0, spend))
                probability_rows.append(np.where(invalid, 0.0, probability))
                up_rows.append(banks - spend + stake * band.odds)
                down_rows.append(banks - spend)
                meta.append({"band": band.label, "target_odds": band.odds, "tickets": tickets,
                             "stake_rule": f"{kind}:{fraction}"})
    if not meta:
        raise ValueError("no action was representable; check per_race_cap and stake_unit")
    stake = np.stack(stake_rows)
    return (
        stake,
        np.stack(spend_rows),
        np.stack(probability_rows),
        np.stack(up_rows),
        np.stack(down_rows),
        stake > 0,
        meta,
    )


def _grid_index(bank_values: np.ndarray, grid: int, n_grid: int) -> np.ndarray:
    """Index of the grid point at or below a continuous bank.

    Evaluating the value function at the grid point *below* the true bank is
    deliberate.  The value function is non-decreasing in bank, so flooring can
    only understate a state, never overstate it.  Rounding to the nearest grid
    point does overstate it: half the transitions round up, the optimizer finds
    them, and the solved probability drifts above what conservation allows.
    The cost of flooring is that the reported value is a lower bound on the
    policy's true reach probability -- which is the direction a bettor should
    want to be wrong in.
    """
    clipped = np.clip(bank_values, 0.0, (n_grid - 1) * grid)
    return np.clip(np.floor(clipped / grid).astype(np.int64), 0, n_grid - 1)


def _value_at(value: np.ndarray, index: np.ndarray) -> np.ndarray:
    return value[index]


def solve(spec: PolicySpec, *, report_every: int = 0) -> PolicyTable:
    """Backward induction over the whole tournament.

    Returns a table with one row per (races remaining, bank) state.
    """
    grid = spec.bank_grid
    n_grid = int(spec.bank_max // grid) + 1
    banks = np.arange(n_grid) * grid
    value = _terminal_value(banks, spec)

    rows: list[dict] = []
    positions = np.arange(n_grid)
    action_cache: dict[int, tuple] = {}
    for remaining in range(1, spec.races + 1):
        race_index = spec.races - remaining + 1
        floor = spec.floor_for(race_index)
        if floor not in action_cache:
            stake, spend, probability, up, down, valid, meta = _enumerate_actions(banks, spec, floor)
            action_cache[floor] = (
                stake, spend, probability,
                _grid_index(up, grid, n_grid), _grid_index(down, grid, n_grid),
                valid, meta,
                _grid_index(np.maximum(0.0, banks - floor), grid, n_grid),
            )
        stake, spend, probability, up_at, down_at, valid, meta, pass_at = action_cache[floor]

        # A forced floor still has to be paid even when no action is taken.
        pass_value = _value_at(value, pass_at)
        candidate = np.where(
            valid,
            probability * _value_at(value, up_at) + (1.0 - probability) * _value_at(value, down_at),
            -1.0,
        )
        best_action = candidate.argmax(axis=0)
        best_value = candidate.max(axis=0)
        act = best_value > pass_value
        new_value = np.where(act, best_value, pass_value)
        best_action = np.where(act, best_action, -1)

        # The value of a state is non-decreasing in bank: a richer state can
        # always copy a poorer one's action and keep the difference.  Where the
        # discretized grid says otherwise, adopt the poorer state's action and
        # record which bank it was sized for.
        monotone = np.maximum.accumulate(new_value)
        source = np.maximum.accumulate(np.where(new_value >= monotone, positions, 0))
        value = monotone

        for index in range(n_grid):
            if banks[index] >= spec.goal:
                continue
            sized_for = int(source[index])
            action_index = int(best_action[sized_for])
            if action_index < 0:
                rows.append({"races_remaining": remaining, "bank": int(banks[index]),
                             "action": "pass", "value": float(value[index])})
                continue
            info = meta[action_index]
            rows.append({
                "races_remaining": remaining,
                "bank": int(banks[index]),
                "action": "bet",
                "band": info["band"],
                "target_odds": info["target_odds"],
                "tickets": info["tickets"],
                "stake_rule": info["stake_rule"],
                "sized_for_bank": int(banks[sized_for]),
                "stake_each": int(stake[action_index][sized_for]),
                "total_spend": int(spend[action_index][sized_for]),
                "value": float(value[index]),
            })
        if report_every and remaining % report_every == 0:
            start = int(min(spec.initial_bank // grid, n_grid - 1))
            print(f"  races_remaining={remaining:>4}  V(initial bank)={value[start]:.4f}", flush=True)

    start = int(min(spec.initial_bank // grid, n_grid - 1))
    expected = {
        "objective": spec.objective,
        "value_at_initial_bank": float(value[start]),
        "upper_bound_on_reach": _reach_upper_bound(spec),
        "states": len(rows),
    }
    bound = expected["upper_bound_on_reach"]
    if spec.objective == "reach" and expected["value_at_initial_bank"] > bound + 1e-6:
        raise ValueError(
            f"solved P(reach)={expected['value_at_initial_bank']:.4f} exceeds the conservation "
            f"bound {bound:.4f}. The solver is gaining money from its own discretization; "
            "this is a bug in the formulation, not a strategy."
        )
    return PolicyTable(
        schema="pykeiba.policy_table.v1",
        spec={**asdict(spec), "bands": [asdict(band) for band in spec.bands]},
        banks=[int(bank) for bank in banks],
        expected=expected,
        rows=rows,
    )


def _reach_upper_bound(spec: PolicySpec) -> float:
    """``P(reach) <= R_max * initial / goal``.

    Money is conserved up to the return rate, so no sequence of bets can move
    more than ``R_max`` of the starting bank across the finish line.  It is a
    useful sanity check: a solver reporting more than this has a bug.
    """
    best_return = max(band.return_rate for band in spec.bands)
    return float(min(1.0, best_return * spec.initial_bank / spec.goal))


def lookup(table: PolicyTable, races_remaining: int, bank: int) -> Action:
    """Read the table at one state, rounding the bank down to the grid."""
    grid = int(table.spec["bank_grid"])
    goal = int(table.spec["goal"])
    if bank >= goal:
        return Action(band="", target_odds=0.0, tickets=0, stake_each=0, total_spend=0, value=1.0)
    bucket = int(bank) // grid * grid
    races_remaining = max(1, min(int(races_remaining), int(table.spec["races"])))
    for row in table.rows:
        if row["races_remaining"] == races_remaining and row["bank"] == bucket:
            if row["action"] == "pass":
                return Action("", 0.0, 0, 0, 0, float(row["value"]))
            return Action(
                band=row["band"],
                target_odds=float(row["target_odds"]),
                tickets=int(row["tickets"]),
                stake_each=int(row["stake_each"]),
                total_spend=int(row["total_spend"]),
                value=float(row["value"]),
            )
    raise KeyError(f"no policy row for races_remaining={races_remaining}, bank bucket={bucket}")


def index_table(table: PolicyTable) -> dict[tuple[int, int], dict]:
    """Dictionary index for repeated lookups during a backtest."""
    return {(row["races_remaining"], row["bank"]): row for row in table.rows}


def simulate_table(table: PolicyTable, *, paths: int = 20_000, seed: int = 11) -> dict:
    """Forward Monte-Carlo check of a solved table.

    The backward solve reports a value computed on a discretized grid.  This
    replays the table forward under the same return-rate assumptions and
    reports what actually happens, so a formulation error shows up as a gap
    between the two numbers rather than hiding inside the solver.

    It verifies the *solver*, not the world: it reuses the table's own return
    rates, so it cannot tell you whether those rates are right.
    """
    rng = np.random.default_rng(seed)
    grid = int(table.spec["bank_grid"])
    goal = int(table.spec["goal"])
    horizon = int(table.spec["races"])
    return_rate = {band["label"]: float(band["return_rate"]) for band in table.spec["bands"]}
    top_bucket = max(table.banks)

    stage_rows: dict[int, dict[int, dict]] = {}
    for row in table.rows:
        stage_rows.setdefault(row["races_remaining"], {})[row["bank"]] = row

    bank = np.full(paths, float(table.spec["initial_bank"]))
    for remaining in range(horizon, 0, -1):
        rows = stage_rows.get(remaining, {})
        buckets = np.minimum((bank // grid * grid).astype(np.int64), top_bucket)
        spend = np.zeros(paths)
        payout = np.zeros(paths)
        hit_probability = np.zeros(paths)
        odds = np.zeros(paths)
        for bucket in np.unique(buckets):
            row = rows.get(int(bucket))
            if row is None or row.get("action") != "bet":
                continue
            mask = buckets == bucket
            tickets = int(row["tickets"])
            stake_each = float(row["stake_each"])
            band_odds = float(row["target_odds"])
            spend[mask] = stake_each * tickets
            odds[mask] = band_odds
            hit_probability[mask] = min(0.999, return_rate[row["band"]] / band_odds * tickets)
            payout[mask] = stake_each * band_odds
        affordable = (spend > 0) & (spend <= bank) & (bank < goal)
        hit = affordable & (rng.random(paths) < hit_probability)
        bank = bank - np.where(affordable, spend, 0.0) + np.where(hit, payout, 0.0)

    reached = float(np.mean(bank >= goal))
    return {
        "paths": paths,
        "seed": seed,
        "simulated_reach": reached,
        "solved_value": float(table.expected["value_at_initial_bank"]),
        "upper_bound_on_reach": float(table.expected["upper_bound_on_reach"]),
        "mean_final_bank": float(np.mean(bank)),
        "median_final_bank": float(np.median(bank)),
    }
