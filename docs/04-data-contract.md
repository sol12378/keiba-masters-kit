# The data contract

**This repository ships no race data.** Historical odds and results carry
third-party terms this project cannot pass on, and the captures behind the
original work run to tens of gigabytes. What is shipped is the format, plus a
generator that produces it with known ground truth.

## The panel

A panel is a directory of one JSON file per race:

```
panel/
  20260926/
    202609260411.json
    202609260412.json
```

```json
{
  "race_id": "202609260411",
  "date": "20260926",
  "post_at": "2026-09-26T05:30:00Z",
  "captured_at": "2026-09-26T05:20:00Z",
  "win": {"1": 3.2, "2": 11.4, "3": 26.0},
  "trifecta": {"1-2-3": 320.4, "1-3-2": 455.1},
  "result": {
    "finish": [5, 2, 7],
    "payout_per_100": {"5-2-7": 4321.0}
  }
}
```

| Field | Meaning |
|---|---|
| `race_id` | Exactly 12 ASCII letters/digits. The runtime enforces this. |
| `date` | `YYYYMMDD`, the local race date. Must match the directory name. |
| `post_at` | Scheduled post time, with an offset. |
| `captured_at` | When these prices were read. **Strictly before `post_at`.** |
| `win` | Horse number to win odds, at `captured_at`. |
| `trifecta` | Ordered triple to odds, at `captured_at`. |
| `result` | Settlement. Absent until the race is settled. |

`payout_per_100` is the official return per 100 points staked — a multiple,
not a yen amount. Do not infer the unit from the column name; check one
official figure against the stored value before trusting a new source.

## Rules that are not conventions

**`captured_at` must be strictly before `post_at`, and the loader enforces it.**
A panel where prices were read after the gates opened describes decisions
nobody could have made. This is the single easiest way to fabricate a result,
and it is easy to do by accident when a collector retries.

**Never use file mtime as availability.** A copied or re-synced file has a
mtime from today and a price from last year.

**Settlement is not a feature.** `result` exists so a backtest can settle
tickets. No function that produces a probability or a decision may read it.
`load_race` keeps it in a separate field so a violation is visible at the call
site, and `build_features` never touches it except to locate the winning
combination's index for the likelihood.

**Final odds are not a feature either.** They are known only after betting
closed. If your source has them, do not put them in `win` or `trifecta`.

**Missing is not zero.** A settled race with no payout entry for a combination
means that ticket lost, and `payout_per_100` returns `0.0`. An unsettled or
unknown race returns `None`, and the backtest counts it separately rather than
folding it into the bank. Converting unknown to "lost" understates the result;
converting it to "won" overstates it. Report the count instead.

## Incomplete races

`build_features` raises rather than imputing when a market is partly captured.
A race with a missing pool is dropped, and `train` reports how many were
dropped. That number is part of the result: a pipeline that silently drops a
third of its races is selecting on capture success, which correlates with
whatever made capture fail.

## Bringing your own data

Write a collector that emits the shape above. Whatever your source, it is
yours to comply with:

* Read the terms of service of any site or feed you pull from.
* Honour `robots.txt` and rate limits. One request per second is not slow when
  you are reading a public site you do not pay for.
* Identify your client honestly in the User-Agent.
* Redistribution of collected odds is usually not permitted. Keep your panel
  out of git — `/data` is in `.gitignore` for that reason.

## The synthetic generator

```bash
.venv/bin/python -m pykeiba synth --out data/synthetic --days 6 --races-per-day 12 --seed 7
```

It is built to be *unflattering*:

* The market's probabilities are the true ones perturbed by noise, then marked
  up by a takeout. There is no edge planted for a model to find.
* Winning tickets settle **below** their quoted price (median drift 0.85).
  Money keeps arriving after you bet, so the pool you are paid from is bigger
  than the one you priced against. A simulation without this makes every
  strategy look better than it is, and it is what the allocator's
  `odds_factor` exists to absorb.
* The finishing order is a Plackett-Luce draw from the true strengths, so
  Harville is approximately right by construction — which is the friendliest
  possible case for it, and worth remembering when reading a holdout number.
