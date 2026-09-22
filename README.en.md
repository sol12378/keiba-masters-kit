# keiba-masters-kit

A local betting runtime and the model pipeline that feeds it, extracted from an
entry to the AI 競馬予想マスターズ 2026 contest.

Everything here operates on the contest's **virtual points**. Nothing in this
repository buys a real betting ticket.

日本語版 (primary): [README.md](README.md)

**The documentation under `docs/` is written in Japanese**, for the audience
this was built for. This file is a summary; the Japanese documents are the
detailed ones. Source comments are in English.

## What this is

Two halves that meet at a signed JSON file.

**The runtime** (Go) takes a frozen plan for a day's races and submits it: one
submission per race, at a fixed number of seconds before post time, then a
read-back to confirm what the other side actually recorded. It keeps an
append-only journal, survives being killed mid-submission, and runs under
launchd so a crash inside a submission window does not cost the race.

**The model pipeline** (Python) turns quoted odds into that plan: market
probabilities with the overround removed, Harville ordered probabilities from
the win pool, a two-coefficient log-linear blend of the two, and a dynamic
program over `(races remaining, bank)` that decides what price to buy and how
much to stake.

The two halves hash the plan independently and a test pins them together, so a
serialization change fails in CI rather than on a race day.

## What this is not

It is **not a way to beat the market**. The model has two market-derived
features and the shipped blend gives it 5% of the weight. On the synthetic data
in this repository it improves holdout NLL by about 0.015 nats out of 5.8 —
which is to say, barely. The dynamic program assumes *no edge at all*: every
price band pays back less than it takes, and the program only chooses when to
accept variance in exchange for a chance at a target. Its own reported value is
checked against `P(reach) ≤ R_max × initial / goal`, and the solver refuses to
return a table that claims more.

Run `make model` and look at `mean_final_bank` in the verification output. It is
below the starting bank, every time. That is the honest shape of this problem.

## Requirements

macOS (Apple silicon or Intel), Go 1.26+, Python 3.11+, and
[uv](https://github.com/astral-sh/uv).

macOS is the only supported platform. The Go and Python parts are portable, but
the scheduling layer is launchd and there are no systemd or Windows equivalents
here.

## Quick start

```bash
make setup
make model
make demo
```

`make model` needs no data and no network: it generates a synthetic season,
fits the model on the earlier dates, solves the policy table, verifies it by
forward simulation, and backtests it.

`make demo` runs the whole thing end to end — it moves a day's races to start a
few minutes from now, builds a plan bundle, starts `votingd` on the **paper
driver**, arms the day, and lets the daemon submit and reconcile each race on
schedule. No account, no network, no credentials.

## Submitting to the real contest

The live driver (`--driver live`) submits to the official contest endpoint with
your own contest account, read from `KEIBA_LOGIN_ID` and `KEIBA_PASSWORD`.

**It only works while the contest is accepting votes.** The 2026 contest closed
on 21 September 2026; outside a contest window the endpoint rejects every
submission, so `--driver live` has nothing to talk to. The paper driver is what
makes the rest of the repository useful after that date, and it is the default
for exactly that reason.

The runtime will not submit anything unless, all at once: the policy enables
submission, the policy's date matches the plan's date, a date-scoped
environment variable matches the policy, an operator arms the day against the
bundle's exact SHA-256, and no kill-switch file is present. That is five
deliberate steps, and it is not accidental — see [docs/06-safety.md](docs/06-safety.md).

## Data

**This repository ships no race data.** Historical odds and results come with
third-party terms this project cannot pass on, and the captures behind the
original work run to tens of gigabytes.

What it ships instead is the schema
([docs/04-data-contract.md](docs/04-data-contract.md)) and a generator that
produces the same shape with known ground truth. Point the pipeline at your own
panel and everything works the same way.

## Documentation

All in Japanese.

| | |
|---|---|
| [01-quickstart.md](docs/01-quickstart.md) | From clone to a submitted paper vote |
| [02-voting-runtime.md](docs/02-voting-runtime.md) | States, journal, recovery, reconciliation |
| [03-launchd.md](docs/03-launchd.md) | Running it as a macOS agent |
| [04-data-contract.md](docs/04-data-contract.md) | The panel format, and bringing your own data |
| [05-model-pipeline.md](docs/05-model-pipeline.md) | Features, fitting, the dynamic program |
| [06-safety.md](docs/06-safety.md) | Every gate between a plan and a submission |

## Licence

Apache-2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE) and
[DISCLAIMER.md](DISCLAIMER.md).
