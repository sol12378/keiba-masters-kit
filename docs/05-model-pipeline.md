# The model pipeline

```
quoted odds at decision time
   │
   ├─► market probabilities        (overround removed, no fitted parameter)
   └─► Harville ordered probability (from the win pool)
   │
   ▼
two-column log-linear model  ──►  p = 0.95 · market + 0.05 · model
   │
   ├─ policy table path:  (races remaining, bank) → price band, tickets, stake
   │                      then pick the combinations nearest that price
   └─ target path:        candidate filter → required stake → knapsack
   │
   ▼
plan bundle (hashed)  ──►  votingd
```

## Features

Two columns, both market-derived, for every ordered triple quoted in a race:

1. `log` of the trifecta pool's own overround-free probability
2. `log` of the Harville probability implied by the win pool

Devigging is proportional — `(1/odds) / Σ(1/odds)` — because it is the only
choice that needs no fitted parameter. A power or logistic devig would put an
estimated quantity into the column that is supposed to be the market's opinion.

Harville assumes the remaining runners keep their relative win probabilities
once the winner is removed. This is known to be wrong in a specific direction:
it understates longshots' place chances. It is used here as a *second
market-derived column*, never as a forecast on its own.

## Fitting

One softmax per race over its quoted combinations, with the winning
combination as the observation. A race contributes one observation, not one
per ticket, which is why a few hundred races is a small sample.

The objective is the mean per-race conditional NLL plus a ridge term pulling
the market coefficient toward 1 and the Harville coefficient toward 0 — that
is, toward "use the quoted price and nothing else". Moving away from that
prior has to be paid for in likelihood. Bounds are `[0.5, 1.5]` and `[0, 0.5]`,
solved with L-BFGS-B.

The split is chronological: `--holdout-from YYYYMMDD` holds out that date
onward and the optimizer never sees it.

### Read the result honestly

On the shipped synthetic season the fit lands near `[0.935, 0.0]` with a
holdout NLL improvement of about 0.015 out of 5.80.

The Harville coefficient sits at its lower bound. That is a finding, not a
bug: given the trifecta pool's own price, the win-pool-derived Harville term
added nothing. The market column carries essentially all the information.

An improvement of 0.015 nats is not evidence of a betting edge. It is not
evidence of calibration either. Do not report it as one.

## The policy table

A tournament is not the same problem as an edge. With a fixed number of races
and a bank that has to reach a level, the right action depends on the state.
The solver does backward induction over

```
state  = (races remaining, bank)
action = (price band, number of tickets, stake per ticket)
```

Each band carries a return rate `R`; one ticket at odds `O` hits with
probability `R/O`. With `R < 1` every action loses money in expectation. The
table is choosing *when to accept variance*, not finding value.

The shipped bands are flat statutory-takeout figures, not measurements.
Replace them with your own before you trust any number the solver prints —
and note that feeding it `R > 1` is asserting an edge, which it will believe.

### Two things the solver does to keep itself honest

**It floors, it does not round.** The bank is discretized. The continuous next
bank is evaluated at the grid point *below* it, which can only understate a
state, never overstate it. Rounding to the nearest point overstates about half
the transitions, and a maximizer finds exactly those. The reported value is
therefore a lower bound on the policy's true reach probability.

**It checks itself against conservation.** Money is conserved up to the return
rate, so

```
P(reach) ≤ R_max × initial_bank / goal
```

The solver raises rather than returning a table whose value exceeds this. That
check caught a real bug during development: with nearest-point rounding the
first implementation reported 0.4823 against a bound of 0.4211 — it was minting
money from its own grid.

Grid size trades accuracy for time. At `--bank-grid 25000` the value reads
0.296; at 5,000 it reads 0.397; at 2,500, 0.408. All below the 0.421 bound, all
solved in under a second.

### Verify it forwards

```bash
.venv/bin/python -m pykeiba verify --table out/policy_table.json
```

Replays the table forward under its own return-rate assumptions. Solved 0.397,
simulated 0.406, bound 0.421 — consistent. A solved value *above* the
simulation means the backward pass is wrong.

It also prints `mean_final_bank` (~810,000 from 1,000,000) and
`median_final_bank` (~35,000). The second number is the one to sit with: the
policy is a lottery ticket, and the median outcome is near zero.

## Allocation

Two strategies share the features.

**Policy table**: look up the state, buy the `k` combinations priced closest to
the band, staking what the table says. Ranking inside the band mixes
distance-in-price (95%) with the model's probability ranking (5%). Ranks are
mixed rather than raw values so the two scales cannot fight.

**Target balance**: size each ticket so that a hit alone puts the bank at or
above the goal —

```
stake = 100 × ceil((goal − bank + allowance) / (100 × odds × odds_factor))
```

then run a 0/1 knapsack in 100-point units over the best 200 candidates by
probability-per-point. Because tickets in one race are mutually exclusive, the
objective is just the sum of their probabilities.

`odds_factor` defaults to 0.80 and it is not a fudge. A winning ticket settles
below the price you bought at, because money keeps arriving after you bet.
Sizing against the full quote systematically undershoots the target.

Both are optimal *within one race's budget*. Neither is a globally optimal
multi-race strategy, and neither models what other entrants are doing.
