# Quickstart

From a clone to a submitted (paper) vote. About ten minutes, most of it waiting
for the scheduler.

## 1. Install

```bash
git clone https://github.com/sol12378/keiba-masters-kit.git
cd keiba-masters-kit
make setup
```

`make setup` creates `.venv`, installs the Python package in editable mode, and
builds `bin/votingd` and `bin/votectl`.

## 2. Build a model with no data

```bash
make model
```

Five steps run in sequence:

| Step | What it does | Typical output |
|---|---|---|
| `synth` | Writes a synthetic season under `data/synthetic` | 72 races, ~60,000 quoted trifecta combinations |
| `train` | Fits two coefficients on the earlier dates | `[0.935, 0.0]`, holdout NLL improvement ≈ 0.015 |
| `policy-table` | Solves `(races remaining, bank) → action` | ~27,000 states in under a second |
| `verify` | Forward-simulates the table 20,000 times | simulated reach ≈ 0.41, mean final bank ≈ 810,000 |
| `backtest` | Replays the panel through the table | one path, one or two hits at most |

Two numbers deserve a look before anything else.

`upper_bound_on_reach` is `R_max × initial / goal` — the most any policy can
achieve when money is conserved up to the return rate. `value_at_initial_bank`
must sit below it. If it does not, the solver refuses to return a table at all,
because a value above the bound means the dynamic program is gaining money from
its own discretization rather than from a strategy.

`mean_final_bank` is below the starting bank. It always will be: there is no
edge anywhere in this pipeline.

## 3. Run the whole loop locally

```bash
make demo
```

The demo:

1. clears its own state under `var/voting-demo`;
2. moves one day's races to start six minutes from now;
3. renders a policy for that date and builds a plan bundle;
4. starts `votingd` on the paper driver;
5. imports and arms the day against the bundle's SHA-256;
6. leaves the daemon running so you can watch it work.

In another shell:

```bash
./bin/votectl --policy var/policy_<date>.json status
cat var/voting-demo/paper_state.json
tail -f var/voting-demo/events.jsonl
```

A race moves `DISCOVERED → VALIDATED → ARMED → POSTING → PENDING_CONFIRMATION →
CONFIRMED`. The submission happens 300 seconds before post time and the
confirmation read-back follows about a minute later.

`paper_state.json` is the paper driver's ledger: the votes it accepted and the
simulated balance. After three races of two 10,000-point tickets each it reads
`940000`.

Stop the demo with Ctrl-C.

## 4. Point it at your own data

The pipeline reads a directory of per-race JSON files. Write your own collector
that emits that shape, then:

```bash
.venv/bin/python -m pykeiba train  --panel /path/to/panel --out out/model.json
.venv/bin/python -m pykeiba backtest --panel /path/to/panel --model out/model.json \
    --table out/policy_table.json
```

The format, and what must be true about `captured_at`, is in
[04-data-contract.md](04-data-contract.md).

## 5. Run it under launchd

```bash
./scripts/launchd.sh install --policy var/policy_<date>.json --driver paper
./scripts/launchd.sh status
./scripts/launchd.sh logs
./scripts/launchd.sh uninstall
```

See [03-launchd.md](03-launchd.md), particularly the part about sleep.
