# Every gate between a plan and a submission

The default configuration submits nothing anywhere. Getting a real vote out
takes five separate, deliberate acts. This document lists them so you can
check them, and so you can see what you are switching off if you remove one.

## The five gates

**1. The driver.** `--driver paper` is the default and writes to a local file.
Reaching the network needs `--driver live`, typed explicitly. Installing the
launchd agent with `live` prompts for a typed confirmation first.

**2. The policy enables submission.** `submission.enabled`, plus per-race and
per-day caps, the stake unit, and the set of bet-id patterns allowed. A policy
must be `frozen_for_implementation` to load at all.

**3. The policy's date matches the plan's date.** A rendered policy is valid
for exactly one day. `plan.race_date` must equal `policy.connection_date`, and
the scheduled post time must fall on that date in the policy's timezone.
Yesterday's bundle cannot be replayed today.

**4. A date-scoped environment variable matches.** The policy names it —
`KEIBA_ENABLE_SUBMISSION=2026-09-26`. It is awkward on purpose: arming a day
should be a decision someone makes that day, not a flag left set from last
week.

**5. An operator arms the exact bundle.** `arm-day --confirm-sha256 <digest>`,
where the digest must match the bundle's own hash, recomputed by the daemon
from the content. A plan that changed by one point fails here.

And one that can stop everything at any moment:

**The kill switch.** A file at the path the policy names (`var/STOP_VOTING` by
default). Present, and nothing is submitted. Create it with `touch`; no process
has to be signalled.

```bash
touch var/STOP_VOTING          # stop
./bin/votectl --policy <p> kill --reason "..."   # stop and record why
```

## Limits worth setting deliberately

| Policy field | What it bounds |
|---|---|
| `max_stake_per_race` | The most one race can cost |
| `max_daily_total_stake` | The most a day can cost |
| `max_bets_per_race` | Tickets per race |
| `max_races` | Races in a day |
| `max_post_requests` | Total submissions, ever, for this policy |
| `allowed_bet_id_patterns` | Which pools may be bought |
| `stake_unit` | Must be 100 |

The runtime checks these against the bundle at import, before anything is
armed. A bundle that would breach a cap is rejected whole rather than
partially submitted.

## Behaviours that are not configurable

**One submission per race.** Not a retry policy — a hard rule.

**An ambiguous submission is never retried.** Timeout, dropped connection,
unparseable response: the daemon marks the race `AMBIGUOUS`, halts the day, and
waits for a human. Retrying could double-vote a race, which cannot be undone.

**A race found in `POSTING` after a restart is GET-only.** It asks what
happened; it does not re-send to find out.

**Confirmation is a read-back, not a status code.** The daemon compares the
recorded vote field by field against what it sent, and any difference is a
`CONFLICT`.

## Credentials

`KEIBA_LOGIN_ID` and `KEIBA_PASSWORD`, from the environment only. They are
never written to the state directory, never logged, and deliberately absent
from the launchd template — a plist is world-readable and ends up in backups.
Keep them in the Keychain and export them from a wrapper.

The paper driver ignores them entirely.

## After the contest closes

The live endpoint only accepts votes while a contest is running. The 2026
contest closed on 21 September 2026. Running `--driver live` now fails at
submission regardless of how the five gates are set — that is the service's
behaviour, not this code's.

Everything else still works. The paper driver exercises the identical state
machine, ledger and reconciliation path, which is what makes the runtime
useful to read and to build on after the fact.
