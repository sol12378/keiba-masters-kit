#!/usr/bin/env python3
"""One-race, fail-closed recovery for the 2026-09-19 Nakayama 12R."""
from __future__ import annotations

import json
import os
import sys
from datetime import datetime
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE
sys.path[:0] = [str(HERE)]
import allin_plan as P  # noqa: E402
import competition_autovote_2026 as A
import competition_plan_watch as W
import competition_weekend_sanrentan3 as C

DAY = "20260919"
RACE_KEY = "2026091906040512"
RACE_ID = P.B.official_race_id(RACE_KEY)
BASE = ROOT / "outputs/competition-2026/recovery-20260919"
POLICY_PATH = BASE / "policy.json"
PLAN_PATH = BASE / "plan.json"


def policy() -> dict:
    value = C.make_policy(DAY, 24)
    value["policy_id"] = "COMPETITION-2026-ALLIN-RECOVERY-20260919-N12"
    value["interfaces"].update(
        status_listen="127.0.0.1:18776",
        control_socket="outputs/competition-2026/recovery-20260919.sock",
        state_directory=str(BASE.relative_to(ROOT)))
    value["storage"].update(
        journal=str((BASE / "events.jsonl").relative_to(ROOT)),
        snapshot=str((BASE / "snapshot.json").relative_to(ROOT)))
    value["submission"].update(max_arms=1, max_post_requests=1, max_races=1,
                               max_total_stake=33_300, max_daily_total_stake=33_300)
    value["planner"]["strategy_policy"] = str((BASE / "strategy.json").relative_to(ROOT))
    return value


def prepare() -> None:
    if C.LEDGER.exists():
        ledger = json.loads(C.LEDGER.read_text())
        if int(ledger["turnover"]) > 532_800:
            raise ValueError("original daily budget is exceeded")
    old = json.loads(C.policy_path(DAY).read_text())
    old_state = json.loads((C.base_for(DAY) / DAY / "snapshot.json").read_text())
    if old["policy_id"] != C.PREFIX + "-" + DAY or not old_state.get("killed"):
        raise ValueError("original daemon is not in its expected stopped state")
    if any((row.get("plan") or {}).get("payload", {}).get("race_id") == RACE_ID
           for row in (old_state.get("races") or {}).values()):
        raise ValueError("target race already exists in original voting journal")
    C.create_once(POLICY_PATH, policy())
    C.create_once(BASE / "strategy.json", {"recovery_only": RACE_KEY,
        "no_retry": True, "maximum_stake": 33_300,
        "original_kill_reason": old_state.get("kill_reason")})
    print("RECOVERY_PREPARED", RACE_KEY)


def tick() -> None:
    now = datetime.now(P.JST)
    if now.strftime("%Y%m%d") != DAY or not (16 <= now.hour < 17):
        return
    if any(path.exists() for path in A.KILL_FILES):
        return
    if json.loads(POLICY_PATH.read_text()) != policy():
        raise ValueError("recovery policy changed")
    old_state = json.loads((C.base_for(DAY) / DAY / "snapshot.json").read_text())
    if not old_state.get("killed") or any(
        (row.get("plan") or {}).get("payload", {}).get("race_id") == RACE_ID
        for row in (old_state.get("races") or {}).values()):
        raise ValueError("original daemon state changed")
    status = A.votectl(POLICY_PATH, "status") or {}
    if status.get("killed") or status.get("races"):
        return
    if PLAN_PATH.exists():
        plan = json.loads(PLAN_PATH.read_text())
    else:
        manifest, _ = W.newest_usable(W.PANEL / DAY / "races" / RACE_KEY, now)
        if manifest is None:
            return
        score, _ = P.score_manifest(manifest)
        race_class = P.proposed_class(score)
        plan, debug = P.build(RACE_KEY, manifest, now, race_class,
                              active_policy_id=policy()["policy_id"])
        C.validate_plan(plan, DAY, policy(), race_class)
        P.atomic(PLAN_PATH, plan)
        P.atomic(BASE / "plan.debug.json", debug)
    if plan["payload"]["race_id"] != RACE_ID:
        raise ValueError("recovery plan race mismatch")
    if plan["payload_sha256"] != P.B.canonical_sha256(plan["payload"]):
        raise ValueError("recovery plan hash mismatch")
    if now >= datetime.fromisoformat(plan["hard_submit_deadline"]) - A.timedelta(seconds=A.DEADLINE_MARGIN):
        return
    stake = sum(int(bet["money"]) for bet in plan["payload"]["bet_list"])
    if stake > 33_300:
        raise ValueError("recovery stake exceeds isolated cap")
    A.votectl(POLICY_PATH, "import-plan", "--file", str(PLAN_PATH))
    A.votectl(POLICY_PATH, "arm-test", "--plan-id", plan["plan_id"],
              "--confirm-sha256", plan["payload_sha256"])
    print("RECOVERY_ARMED", RACE_ID, "stake", stake)


if __name__ == "__main__":
    try:
        if sys.argv[1:] == ["prepare"]:
            prepare()
        elif sys.argv[1:] == ["tick"]:
            tick()
        else:
            raise ValueError("usage: recovery {prepare|tick}")
    except Exception as exc:
        print("RECOVERY_BLOCKED", str(exc), file=sys.stderr)
        raise SystemExit(1)
