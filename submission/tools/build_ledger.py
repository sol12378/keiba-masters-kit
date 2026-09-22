#!/usr/bin/env python3
"""Assemble the published ledger from the daemon snapshots and day ledgers.

PUBLISHED AS A RECORD of how submission/ledger/ was produced. Its inputs are
not in this repository.

The authoritative record of *what was submitted* is the voting daemon's own
snapshot: it holds the plan the operator armed, the payload hash the daemon
recomputed, the state it reached, and the balance the service reported back.
The authoritative record of *what came back* is the per-day ledger, which
carries the settled payout per race.

This joins the two and reports what does not line up rather than smoothing it
over.  Reconciliation gaps are part of the record.
"""
from __future__ import annotations

import hashlib
import json
import os
import sys
from pathlib import Path

# The private research repository, which holds the daemon snapshots and the
# day ledgers.  Neither is in this repository; this script records how the
# published ledger was derived from them.
ROOT = Path(os.environ.get("KEIBA_RESEARCH_ROOT", "../keiba")).expanduser().resolve()
EVIDENCE = ROOT / "outputs/competition-2026"
OUT = Path(sys.argv[1] if len(sys.argv) > 1 else "/tmp/ledger")

#: (snapshot, phase label).  outputs/competition-2026/snapshot.json is the live
#: copy of the 2026-08-29 state directory and is deliberately not listed twice.
SNAPSHOTS = [
    ("votingd-state/20260829/snapshot.json", "phase1"),
    ("votingd-state/20260830/snapshot.json", "phase1"),
    ("sanrentan3-20260905-06/20260905/snapshot.json", "phase1"),
    ("sanrentan3-20260905-06/20260906/snapshot.json", "phase1"),
    ("sanrentan3-20260912-13/20260912/snapshot.json", "phase1"),
    ("sanrentan3-20260913/snapshot.json", "phase1"),
    ("sanrentan3-20260912-13/20260913/snapshot.json", "phase1"),
    ("recovery-20260919/snapshot.json", "phase1"),
    ("sanrentan3-allin-20260919-20/20260919/snapshot.json", "phase1"),
    ("dynamic-20260920/snapshot.json", "phase2"),
]

LEDGERS = [
    "ledger.json",
    "sanrentan3-allin-20260919-20/ledger.json",
    "dynamic-20260920/ledger.json",
]


#: The balance the service itself reported at the close of the window.
OFFICIAL_FINAL_BANK = 5_504_260

RECONCILIATION_COMMENTARY = [
    "The three day ledgers hold 203 races and 1,224,300 points staked. The "
    "daemon snapshots hold 204 and 1,227,300. The difference is one race on "
    "2026-09-13, 202606040404, staking 3,000 points, which the old ledger "
    "never recorded. The snapshot is the authoritative side: it carries the "
    "armed plan, the payload hash the daemon recomputed, and the CONFIRMED "
    "state.",

    "No settlement was captured locally for 202606040404, so its payout is "
    "published as null rather than as zero. The arithmetic bounds it anyway: "
    "official final bank minus (opening - stake + payouts) leaves 6,200 "
    "points unexplained, of which 5,200 are confirmed refunds on scratched "
    "runners. Any payout P on that race would make the residue 6,200 - P, so "
    "P is at most 1,000. A winning trifecta on a 3,000-point stake in the "
    "bands this policy buys pays orders of magnitude more than 1,000, so the "
    "race lost. It is left as null because that is an inference, not a "
    "captured settlement.",

    "1,000 points of the 6,200 remain unattributed. They are not this race's "
    "payout, by the bound above. They are recorded here unresolved rather "
    "than absorbed into a total.",
]


def sha256_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def stake_of(payload: dict) -> int:
    return sum(int(bet["money"]) for bet in payload.get("bet_list", []))


def main() -> None:
    races: dict[str, dict] = {}
    duplicates: list[dict] = []
    sources: list[dict] = []

    for relative, phase in SNAPSHOTS:
        path = EVIDENCE / relative
        if not path.exists():
            continue
        sources.append({"path": f"outputs/competition-2026/{relative}",
                        "sha256": sha256_file(path)})
        document = json.loads(path.read_text())
        for plan_id, record in (document.get("races") or {}).items():
            plan = record["plan"]
            payload = plan["payload"]
            race_id = payload["race_id"]
            entry = {
                "race_id": race_id,
                "race_date": plan["race_date"],
                "phase": phase,
                "plan_id": plan_id,
                "policy_id": plan["policy_id"],
                "state": record["state"],
                "marks": payload["mark"],
                "bets": payload["bet_list"],
                "stake": stake_of(payload),
                "payload_sha256": plan["payload_sha256"],
                "plan_sha256": record.get("plan_sha256"),
                "scheduled_post_time": plan["scheduled_post_time"],
                "target_submit_time": plan["target_submit_time"],
                "post_attempts": record.get("post_attempts"),
                "verification_attempts": record.get("verification_attempts"),
                "remaining_money_reported": record.get("remaining_money"),
                "confirmed_at": record.get("updated_at"),
                "adapter_version": (plan.get("provenance") or {}).get("adapter_version"),
                "model_sha256": (plan.get("provenance") or {}).get("model_sha256"),
                "source": f"outputs/competition-2026/{relative}",
            }
            if race_id in races:
                if races[race_id]["payload_sha256"] != entry["payload_sha256"]:
                    duplicates.append({"race_id": race_id,
                                       "kept": races[race_id]["source"],
                                       "also_in": entry["source"],
                                       "payload_differs": True})
                continue
            races[race_id] = entry

    # Settled payouts, joined by race_id from the day ledgers.
    payouts: dict[str, int] = {}
    ledger_sources = []
    for relative in LEDGERS:
        path = EVIDENCE / relative
        if not path.exists():
            continue
        ledger_sources.append({"path": f"outputs/competition-2026/{relative}",
                               "sha256": sha256_file(path)})
        for row in json.loads(path.read_text()).get("history", []):
            if int(row.get("stake", 0)) <= 0:
                continue
            payouts[row["race_id"]] = payouts.get(row["race_id"], 0) + int(row.get("payout", 0))

    unmatched_ledger = sorted(set(payouts) - set(races))
    missing_payout = []
    for race_id, entry in races.items():
        if entry["state"] != "CONFIRMED":
            continue
        if race_id in payouts:
            entry["payout"] = payouts[race_id]
        else:
            entry["payout"] = None
            missing_payout.append(race_id)

    ordered = sorted(races.values(), key=lambda e: (e["race_date"], e["scheduled_post_time"], e["race_id"]))
    confirmed = [e for e in ordered if e["state"] == "CONFIRMED"]

    daily: dict[str, dict] = {}
    for entry in confirmed:
        day = daily.setdefault(entry["race_date"], {
            "races": 0, "stake": 0, "payout": 0, "payout_unknown": 0, "hits": 0,
        })
        day["races"] += 1
        day["stake"] += entry["stake"]
        if entry["payout"] is None:
            day["payout_unknown"] += 1
        else:
            day["payout"] += entry["payout"]
            if entry["payout"] > 0:
                day["hits"] += 1

    total_stake = sum(e["stake"] for e in confirmed)
    total_payout = sum(e["payout"] or 0 for e in confirmed)
    summary = {
        "window": "2026-08-29..2026-09-21",
        "races_confirmed": len(confirmed),
        "races_not_confirmed": [
            {"race_id": e["race_id"], "state": e["state"]} for e in ordered if e["state"] != "CONFIRMED"
        ],
        "total_stake": total_stake,
        "total_payout": total_payout,
        "hits": sum(1 for e in confirmed if (e["payout"] or 0) > 0),
        "implied_final_bank": 1_000_000 - total_stake + total_payout,
        "official_final_bank": OFFICIAL_FINAL_BANK,
        "unexplained_credit": OFFICIAL_FINAL_BANK - (1_000_000 - total_stake + total_payout),
        "reconciliation_notes": {
            "races_with_unknown_payout": missing_payout,
            "ledger_rows_with_no_snapshot": unmatched_ledger,
            "duplicate_race_ids_with_differing_payloads": duplicates,
            "commentary": RECONCILIATION_COMMENTARY,
        },
        "sources": {"snapshots": sources, "ledgers": ledger_sources},
    }

    OUT.mkdir(parents=True, exist_ok=True)
    (OUT / "races.json").write_text(
        json.dumps(ordered, ensure_ascii=False, indent=1, sort_keys=True) + "\n", encoding="utf-8")
    (OUT / "daily.json").write_text(
        json.dumps(daily, ensure_ascii=False, indent=1, sort_keys=True) + "\n", encoding="utf-8")
    (OUT / "summary.json").write_text(
        json.dumps(summary, ensure_ascii=False, indent=1, sort_keys=True) + "\n", encoding="utf-8")

    print(json.dumps({k: v for k, v in summary.items() if k != "sources"},
                     ensure_ascii=False, indent=1)[:2000])


if __name__ == "__main__":
    main()
