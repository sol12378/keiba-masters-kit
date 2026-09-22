# Contributing

## Before you open a pull request

```bash
make test   # go vet, go test, pytest
make lint   # ruff
make model  # the full pipeline, no network, no data
```

CI runs the same on macOS, plus a gitleaks scan.

## Things this project will not accept

**Race data.** No odds, results, payouts or captured pages, in any format, at
any size. The inputs carry third-party terms. If a test needs data, extend
`pykeiba.synth`.

**Claims of an edge without evidence that survives a chronological holdout.**
A backtest that improves because it read a settlement field, a final price, or
a race after the split is not a finding. `docs/04-data-contract.md` lists the
rules the loader enforces and why.

**Credentials anywhere except the environment.** Not in a config, not in a
plist, not in a test fixture.

## Things it wants

- A collector that emits the panel format for a source you have the right to
  read, kept polite by default.
- Better return-rate estimation for the policy table's price bands. The
  shipped numbers are statutory takeout figures, not measurements.
- Platform support beyond macOS — but as an addition to `scripts/`, not by
  weakening what already works.

## Style

Go: `gofmt`, standard library first. Python: `ruff`, 120 columns, type hints on
public functions.

Comments explain *why*, especially where the code is deliberately conservative
— the floor-not-round rule in `pykeiba/dp.py`, the GET-only recovery in
`internal/voting`. If you change one of those, change its comment in the same
commit.
