#!/usr/bin/env python3
"""Build submission/ledger/ from the votingd snapshots and the day ledgers.

The inputs are in the private research repository (KEIBA_RESEARCH_ROOT,
default ../keiba). Kept here as a record of how the ledger was produced.
"""
from __future__ import annotations

import hashlib
import json
import os
import sys
from pathlib import Path

# Private research repository with the snapshots and day ledgers.
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

#: How each policy decided the bet. ``policy_table_lookup`` means
#: build_vote_plan.lookup() was called; the others use fixed bands.
DECISION_MECHANISM = {
    "COMPETITION-2026-V17": "policy_table_lookup",
    "COMPETITION-2026-V18-TAIL133": "fixed_band_with_target_backsolve",
    "COMPETITION-2026-V20-SANRENTAN3-1000": "fixed_band_fixed_stake",
    "COMPETITION-2026-V20-SANRENTAN3-1000-20260905": "fixed_band_fixed_stake",
    "COMPETITION-2026-V20-SANRENTAN3-1000-20260906": "fixed_band_fixed_stake",
    "COMPETITION-2026-V20-SANRENTAN3-1000-20260912": "fixed_band_fixed_stake",
    "COMPETITION-2026-V20-SANRENTAN3-1000-20260913": "fixed_band_fixed_stake",
    "COMPETITION-2026-ALLIN-SANRENTAN-20260919": "band_filter_with_class_allocation",
    "COMPETITION-2026-ALLIN-RECOVERY-20260919-N12": "band_filter_with_class_allocation",
    "COMPETITION-2026-DYNAMIC-20260920": "target_backsolve_with_knapsack_and_fitted_model",
}

RECONCILIATION_COMMENTARY = [
    "以前の台帳（3ファイル）は203レース・1,224,300pt、投票デーモンのスナップショットは"
    "204レース・1,227,300ptです。差は2026-09-13のレース202606040404（3,000pt）で、"
    "以前の台帳に記録漏れがありました。スナップショットには送信を許可した計画、"
    "デーモンが再計算したハッシュ値、確定（CONFIRMED）状態が記録されているため、"
    "本台帳ではスナップショットを採用しています。",

    "202606040404は払戻の記録を手元で取得できていないため、payoutをnullとしています。"
    "ただし、大会の最終残高と計算上の残高の差は6,200ptで、そのうち5,200ptは"
    "出走取消・競走除外による返還と確認済みです。このレースの払戻をPとすると"
    "差は6,200−Pとなるため、Pは最大でも1,000ptです。この価格帯の三連単が"
    "3,000ptの投票で的中した場合の払戻はこれより桁違いに大きいので、このレースは"
    "外れと考えられます。推定であり実際の払戻記録ではないため、nullのままにしています。",

    "差額6,200ptのうち1,000ptは内訳を特定できていません。上記のとおり"
    "202606040404の払戻ではありません。合計に含めず、未特定のまま記録しています。",
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

    # Totals per policy.
    by_policy: dict[str, dict] = {}
    for entry in confirmed:
        row = by_policy.setdefault(entry["policy_id"], {
            "races": 0, "stake": 0, "payout": 0, "dates": set(),
        })
        row["races"] += 1
        row["stake"] += entry["stake"]
        row["payout"] += entry["payout"] or 0
        row["dates"].add(entry["race_date"])
    for row in by_policy.values():
        row["dates"] = sorted(row["dates"])

    total_stake = sum(e["stake"] for e in confirmed)
    total_payout = sum(e["payout"] or 0 for e in confirmed)
    summary = {
        # First and last race date in the ledger.
        "race_date_range": [confirmed[0]["race_date"], confirmed[-1]["race_date"]] if confirmed else None,
        "races_confirmed": len(confirmed),
        "races_not_confirmed": [
            {"race_id": e["race_id"], "state": e["state"]} for e in ordered if e["state"] != "CONFIRMED"
        ],
        "total_stake": total_stake,
        "total_payout": total_payout,
        "hits": sum(1 for e in confirmed if (e["payout"] or 0) > 0),
        "implied_final_bank": 1_000_000 - total_stake + total_payout,
        "by_policy": by_policy,
        "decision_mechanism": DECISION_MECHANISM,
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
