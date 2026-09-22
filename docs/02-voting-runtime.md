# The voting runtime

`votingd` is a single-writer daemon that owns one policy, one state directory,
and one day's races. Its job is narrow on purpose: it does not decide anything.
It receives a plan that was frozen and hashed before it started, submits each
race once, and then finds out what the other side recorded.

## Why the decision is frozen before the daemon sees it

Everything interesting — probabilities, stakes, which combinations — happens in
Python and ends in a JSON bundle with a SHA-256 over its own content. The
daemon verifies that hash, an operator arms the day by typing the same hash
back, and only then can a submission happen.

This is what makes a run auditable after the fact. The plan cannot have changed
between "what the model said" and "what was submitted", because both sides hold
the same digest and the runtime re-derives it rather than trusting the field.

## States

```
DISCOVERED ──► VALIDATED ──► ARMED ──► POSTING ──► PENDING_CONFIRMATION ──► CONFIRMED
                   │            │          │                │                  │
                   │            │          │                ├──► CONFIRMED_EXISTING
                   │            │          │                ├──► CONFLICT
                   │            │          │                └──► AMBIGUOUS
                   │            │          └──► REJECTED
                   ├──► EXPIRED └──► KILLED
```

The states worth understanding are the unhappy ones.

**AMBIGUOUS** means the submission was sent and the outcome is unknown: a
timeout, a dropped connection, a response that did not parse. The daemon never
retries an ambiguous submission. Retrying risks a second vote on a race that
already has one, and on a platform where one race takes one vote that is
unrecoverable. It stops the day instead and leaves the decision to a human.

**CONFIRMED_EXISTING** means the read-back found a vote that the daemon did not
knowingly place — almost always its own submission from before a crash. This is
the state that makes restarts safe.

**CONFLICT** means the read-back found something different from what was sent.
That is a hard stop.

## Importing and arming are two steps, and both are checked

`import-day` validates a bundle and stores each plan as `VALIDATED`.
`arm-day` requires the bundle's SHA-256 typed back, and requires every plan in
it to match what was imported — both the payload hash **and** the schedule.

The schedule check is there because the payload hash does not cover it. A
payload is `race_id`, marks and bets; re-planning the same selections for a
different post time leaves that hash identical while the bundle hash moves.
Without the schedule comparison an operator could confirm today's bundle and
arm yesterday's timings, which is how four races were once armed straight into
`EXPIRED`. Re-import after re-planning.

## Crash recovery

The journal is append-only and fsynced per event, and `POSTING` is written
*before* the network call, not after. So after a crash the daemon knows a
submission may have been in flight, and the recovery rule is: a race found in
`POSTING` is **GET-only**. It queries, and it accepts whatever the answer is. It
never re-submits to find out.

This is why `KeepAlive` in the launchd agent is safe. A restarted daemon
converges on what actually happened rather than on what it intended.

## Reconciliation

Confirmation is not "the POST returned 200". The daemon reads the vote back and
compares it, field by field, against the payload it holds. With
`require_exact_get_reconciliation` (the default), anything that differs is a
`CONFLICT`.

An empty read-back before submission is treated as "no vote yet" — and only in
a deliberately narrow case: HTTP 200, the expected operation, status `NG`, and
no reason text. Any other shape stays fail-closed.

## Interfaces

| | |
|---|---|
| Control | Unix socket, mode 0600, in the state directory. `votectl` speaks to this. |
| Status | Read-only HTTP on loopback. Non-loopback binds are refused unless the policy says otherwise. |
| Journal | `events.jsonl`, append-only, fsynced per event |
| Snapshot | `snapshot.json`, atomically renamed |

The control socket doubles as the process lock. A second daemon on the same
state directory cannot start, so there is never more than one writer.

## The driver interface

```go
type API interface {
	Login(context.Context, string, string) (string, error)
	Check(context.Context, string, string) ([]CheckedVote, error)
	Submit(context.Context, string, RacePayload) (SubmitReceipt, error)
}
```

Two implementations ship:

**`paper`** (the default) keeps votes in a local JSON file with a simulated
balance. It answers the same empty-precheck error the live endpoint does, so
the runtime's fail-closed logic is exercised rather than bypassed. It needs no
account and no network, and it is the only driver that works outside a contest
window.

**`live`** talks to the official contest endpoint. It requires a contest
account and only functions while the contest is accepting votes. The 2026
contest closed on 21 September 2026, so today it will fail at submission no
matter how the rest is configured. That is upstream behaviour, not a bug here.

Selection is `--driver`, and it is recorded in the daemon's first log line.
