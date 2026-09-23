# keiba-masters-kit

A local voting runtime and the model pipeline that feeds it, taken from my entry to the AI 競馬予想マスターズ 2026 contest.

> **Please note**
>
> - This is an **unofficial** release by me as an individual participant. It is not affiliated with, or endorsed by, the contest organiser.
> - Everything here works in the contest's virtual points. Nothing in this repository buys a real betting ticket.
> - In Japan, betting on horse racing is limited to people aged 20 and over. Please read [DISCLAIMER.md](DISCLAIMER.md) (Japanese) before using it.

日本語（主）: [README.md](README.md)

The detailed documentation under `docs/` is written in Japanese, for the audience this was built for. This file is a summary. Source code comments are in English.

## What it is

Two parts, joined by a plan file that carries a SHA-256 of its own content.

**The voting runtime (Go)** takes a frozen plan for a day's races and submits it: one submission per race at a fixed time before post, followed by a read-back that compares what the contest server recorded against what was sent. It keeps an append-only journal, recovers safely if it is killed mid-submission, and runs under launchd.

**The model pipeline (Python)** turns decision-time odds into that plan: overround-free market probabilities, Harville ordered probabilities from the win pool, a two-weight log-linear blend of the two, and a dynamic program over `(races remaining, points held)` that decides which price band to buy and how much to stake.

Go and Python compute the plan hash independently, and a test pins them together, so a serialisation change fails during development rather than on a race day.

## Limits

It is not a way to beat the market.

- The model has two market-derived inputs and, by default, contributes 5% of the final probability. On the bundled synthetic data it improves holdout NLL by about 0.015 out of 5.8.
- The dynamic program assumes every price band returns less than it takes. It chooses when to accept risk, not how to raise expected value.
- Its result is checked against `P(reach) ≤ R_max × initial / goal`; the solver refuses to return a table that exceeds this bound.

`make model` prints the mean and median final points. The mean is below the starting amount and, with the default settings, the median is close to zero.

## Requirements

macOS (Apple silicon or Intel), Go 1.26+, Python 3.11+, and [uv](https://github.com/astral-sh/uv). The scheduling layer is launchd; there are no systemd or Windows equivalents.

## Quick start

```bash
make setup
make model
make demo
```

`make model` needs no data and no network. `make demo` runs the whole loop locally on the **paper driver** — plan, approve, submit on schedule, reconcile — with no account and no credentials.

## Submitting to the real contest

The live driver (`--driver live`) submits to the contest's voting API using your own account from `KEIBA_LOGIN_ID` and `KEIBA_PASSWORD`. It only works while the contest is accepting votes. The 2026 contest ended on 22 September 2026, so there is nothing for it to submit to today; the paper driver is the default for that reason.

A submission needs five separate, deliberate steps, plus the absence of an emergency-stop file. See `docs/06-safety.md`.

## Data

No race data is included. Odds and results belong to their providers and cannot be redistributed from here. What ships instead is the data format (`docs/04-data-contract.md`) and a generator that produces synthetic data in the same shape.

## The code that actually ran

`pykeiba/` is a generalised rewrite of the method I used. The scripts I actually ran during the contest are in [submission/](submission/), kept as they were: the policies and planners for 190 races from 29 August to 19 September (of which the 29 races on 29 August consulted the policy table directly), the learning code and fitted weights for the 14 races on 20 September together with a check that those weights reproduce, and a ledger of all 204 races including every ticket bought.

I confirmed publication with the contest organiser after the contest ended.

## Documentation (Japanese)

| | |
|---|---|
| [01-quickstart.md](docs/01-quickstart.md) | From clone to a submitted paper vote |
| [02-voting-runtime.md](docs/02-voting-runtime.md) | States, journal, recovery, reconciliation |
| [03-launchd.md](docs/03-launchd.md) | Running it as a macOS agent |
| [04-data-contract.md](docs/04-data-contract.md) | The data format, and bringing your own |
| [05-model-pipeline.md](docs/05-model-pipeline.md) | Features, fitting, the dynamic program |
| [06-safety.md](docs/06-safety.md) | Every safeguard between a plan and a submission |
| [glossary.md](docs/glossary.md) | Glossary |

## Acknowledgements

My thanks to everyone who organised and ran AI 競馬予想マスターズ 2026, and for agreeing to the publication of this repository. I hope it is useful to anyone taking part in future contests.

## Author

Ryuichi Sato ([@sol12378](https://github.com/sol12378))

## Licence

Apache-2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE) and [DISCLAIMER.md](DISCLAIMER.md).
