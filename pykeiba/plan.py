"""Build plan bundles for votingd.

JSON is serialised with sorted keys and no whitespace so the hashes match
``internal/voting``.
"""

from __future__ import annotations

import hashlib
import json
from collections.abc import Sequence
from datetime import UTC, datetime, timedelta
from pathlib import Path
from zoneinfo import ZoneInfo

__all__ = [
    "BET_TYPE_TRIFECTA",
    "bet_id",
    "marks_from_win_odds",
    "payload_sha256",
    "build_plan",
    "build_bundle",
    "write_bundle",
]

SCHEMA_VERSION = 1

#: Pool numbers used in a bet id.  Ordered pools keep the finishing order;
#: unordered pools use one canonical ascending representation.
BET_TYPE_WIN = 1
BET_TYPE_PLACE = 2
BET_TYPE_BRACKET_QUINELLA = 3
BET_TYPE_QUINELLA = 4
BET_TYPE_WIDE = 5
BET_TYPE_EXACTA = 6
BET_TYPE_TRIO = 7
BET_TYPE_TRIFECTA = 8

_ORDERED = {BET_TYPE_EXACTA, BET_TYPE_TRIFECTA}
_ASCENDING = {BET_TYPE_QUINELLA, BET_TYPE_WIDE, BET_TYPE_TRIO}
_EXPECTED_NUMBERS = {1: 1, 2: 1, 3: 2, 4: 2, 5: 2, 6: 2, 7: 3, 8: 3}


def bet_id(bet_type: int, selection: Sequence[int]) -> str:
    """Canonical bet id, e.g. ``b8_c0_5_2_7`` for a 5-2-7 trifecta."""
    numbers = [int(number) for number in selection]
    expected = _EXPECTED_NUMBERS.get(bet_type)
    if expected is None:
        raise ValueError(f"unknown bet type {bet_type}")
    if len(numbers) != expected:
        raise ValueError(f"b{bet_type} requires exactly {expected} number(s), got {len(numbers)}")
    maximum = 8 if bet_type == BET_TYPE_BRACKET_QUINELLA else 18
    for number in numbers:
        if not 1 <= number <= maximum:
            raise ValueError(f"b{bet_type} numbers must be within 1..{maximum}, got {number}")
    if bet_type == BET_TYPE_BRACKET_QUINELLA:
        numbers = sorted(numbers)
    elif bet_type in _ASCENDING:
        if len(set(numbers)) != len(numbers):
            raise ValueError(f"b{bet_type} numbers must be distinct")
        numbers = sorted(numbers)
    elif bet_type in _ORDERED and len(set(numbers)) != len(numbers):
        raise ValueError(f"b{bet_type} numbers must be distinct")
    return f"b{bet_type}_c0_" + "_".join(str(number) for number in numbers)


def marks_from_win_odds(win_odds: dict[int, float]) -> dict[str, int]:
    """Fill marks from market rank.

    The contest requires a mark per runner. This is not a prediction.
    """
    if not win_odds:
        raise ValueError("no win prices to rank")
    ordered = sorted(win_odds.items(), key=lambda item: (item[1], item[0]))
    marks: dict[str, int] = {}
    for rank, (horse, _) in enumerate(ordered[:18], start=1):
        marks[str(int(horse))] = min(rank, 4)
    return marks


def _canonical(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")


def _digest(value: object) -> str:
    return hashlib.sha256(_canonical(value)).hexdigest()


def payload_sha256(payload: dict) -> str:
    """Hash of a race payload, matching ``voting.PayloadSHA256``."""
    return _digest({
        "race_id": payload["race_id"],
        "mark": payload["mark"],
        "bet_list": payload["bet_list"],
    })


def _rfc3339(moment: datetime) -> str:
    return moment.astimezone(UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


def build_plan(
    *,
    plan_id: str,
    policy_id: str,
    race_date: str,
    race_id: str,
    post_at: datetime,
    marks: dict[str, int],
    bets: Sequence[tuple[str, int]],
    forecast_path: str,
    forecast_sha256: str,
    adapter_version: str,
    model_sha256: str | None = None,
    created_at: datetime | None = None,
    target_submit_seconds_before_post: int = 300,
    hard_cutoff_seconds_before_post: int = 240,
) -> dict:
    """Build the plan for one race.

    ``bets`` is a list of ``(bet_id, points)``. Points must be multiples of 100.
    """
    if not bets:
        raise ValueError(f"{race_id}: a plan must carry at least one bet")
    bet_list = []
    for identifier, points in bets:
        points = int(points)
        if points <= 0 or points % 100 != 0:
            raise ValueError(f"{race_id}: {identifier} stake must be a positive multiple of 100, got {points}")
        bet_list.append({"bet_id": identifier, "money": str(points)})

    payload = {"race_id": race_id, "mark": dict(marks), "bet_list": bet_list}
    target = post_at - timedelta(seconds=target_submit_seconds_before_post)
    deadline = post_at - timedelta(seconds=hard_cutoff_seconds_before_post)
    created = created_at or (target - timedelta(minutes=5))
    if created >= deadline:
        raise ValueError(f"{race_id}: created_at must precede the hard submit deadline")

    provenance = {
        "forecast_path": forecast_path,
        "forecast_sha256": forecast_sha256,
        "adapter_version": adapter_version,
    }
    if model_sha256:
        provenance["model_sha256"] = model_sha256

    return {
        "schema_version": SCHEMA_VERSION,
        "plan_id": plan_id,
        "policy_id": policy_id,
        "race_date": race_date,
        "created_at": _rfc3339(created),
        "scheduled_post_time": _rfc3339(post_at),
        "target_submit_time": _rfc3339(target),
        "hard_submit_deadline": _rfc3339(deadline),
        "payload": payload,
        "payload_sha256": payload_sha256(payload),
        "provenance": provenance,
    }


def build_bundle(
    *,
    bundle_id: str,
    policy_id: str,
    race_date: str,
    plans: Sequence[dict],
    source_path: str,
    source_sha256: str,
    created_at: datetime | None = None,
) -> dict:
    """Wrap per-race plans, hashing the set so the daemon can verify it."""
    if not plans:
        raise ValueError("a bundle must carry at least one plan")
    entries = [
        {
            "plan_id": plan["plan_id"],
            "payload_sha256": plan["payload_sha256"],
            "scheduled_post_time": plan["scheduled_post_time"],
            "target_submit_time": plan["target_submit_time"],
        }
        for plan in plans
    ]
    bundle_sha256 = _digest({"policy_id": policy_id, "race_date": race_date, "plans": entries})
    return {
        "schema_version": SCHEMA_VERSION,
        "bundle_id": bundle_id,
        "policy_id": policy_id,
        "race_date": race_date,
        "created_at": _rfc3339(created_at or datetime.now(UTC)),
        "plans": list(plans),
        "bundle_sha256": bundle_sha256,
        "source_path": source_path,
        "source_sha256": source_sha256,
    }


def write_bundle(path: Path, bundle: dict) -> Path:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(bundle, ensure_ascii=False, indent=1, sort_keys=True) + "\n", encoding="utf-8")
    return path


def race_date_for(post_at: datetime, timezone_name: str) -> str:
    """The policy's race date for a post time, in the policy's own timezone."""
    return post_at.astimezone(ZoneInfo(timezone_name)).strftime("%Y-%m-%d")


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 16), b""):
            digest.update(chunk)
    return digest.hexdigest()
