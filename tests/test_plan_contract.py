"""The planner's half of the cross-language hash contract.

``internal/voting/contract_test.go`` reads the same fixture and recomputes the
same digests in Go.  If either side changes how it serializes a plan, one of
the two tests fails immediately instead of a bundle being rejected on a race
day.
"""

import json
from datetime import UTC, datetime
from pathlib import Path

import pytest

from pykeiba.plan import (
    BET_TYPE_TRIFECTA,
    BET_TYPE_TRIO,
    bet_id,
    build_bundle,
    build_plan,
    marks_from_win_odds,
    payload_sha256,
)

FIXTURE = Path(__file__).resolve().parents[1] / "fixtures" / "plan_bundle_contract.json"


def rebuild_fixture() -> dict:
    post = datetime(2026, 9, 26, 5, 30, tzinfo=UTC)
    plan = build_plan(
        plan_id="contract-202609260411",
        policy_id="CONTRACT-POLICY-V1",
        race_date="2026-09-26",
        race_id="202609260411",
        post_at=post,
        marks={"1": 1, "2": 2, "3": 3, "4": 4, "5": 4, "10": 4},
        bets=[(bet_id(BET_TYPE_TRIFECTA, (5, 2, 10)), 300), (bet_id(BET_TYPE_TRIFECTA, (10, 1, 3)), 100)],
        forecast_path="fixtures/contract_race.json",
        forecast_sha256="a" * 64,
        adapter_version="pykeiba/0.1.0",
        model_sha256="b" * 64,
        created_at=datetime(2026, 9, 26, 5, 10, tzinfo=UTC),
    )
    return build_bundle(
        bundle_id="contract-bundle",
        policy_id="CONTRACT-POLICY-V1",
        race_date="2026-09-26",
        plans=[plan],
        source_path="fixtures/contract_race.json",
        source_sha256="c" * 64,
        created_at=datetime(2026, 9, 26, 5, 10, tzinfo=UTC),
    )


def test_the_committed_fixture_still_matches_what_the_planner_produces():
    stored = json.loads(FIXTURE.read_text(encoding="utf-8"))
    assert rebuild_fixture() == stored


def test_the_fixture_hashes_are_self_consistent():
    stored = json.loads(FIXTURE.read_text(encoding="utf-8"))
    plan = stored["plans"][0]
    assert payload_sha256(plan["payload"]) == plan["payload_sha256"]


def test_trifecta_bet_ids_keep_the_finishing_order():
    assert bet_id(BET_TYPE_TRIFECTA, (5, 2, 10)) == "b8_c0_5_2_10"


def test_unordered_pools_are_written_in_one_canonical_order():
    assert bet_id(BET_TYPE_TRIO, (10, 2, 5)) == "b7_c0_2_5_10"


def test_a_repeated_horse_is_refused():
    with pytest.raises(ValueError):
        bet_id(BET_TYPE_TRIFECTA, (5, 5, 2))


def test_marks_carry_exactly_one_first_choice():
    marks = marks_from_win_odds({3: 2.0, 1: 9.9, 7: 4.4, 2: 30.0})
    assert sorted(marks.values()).count(1) == 1
    assert marks["3"] == 1
    assert set(marks.values()) <= {1, 2, 3, 4}


def test_a_stake_that_is_not_a_hundred_point_unit_is_refused():
    with pytest.raises(ValueError, match="multiple of 100"):
        build_plan(
            plan_id="x",
            policy_id="p",
            race_date="2026-09-26",
            race_id="202609260411",
            post_at=datetime(2026, 9, 26, 5, 30, tzinfo=UTC),
            marks={"1": 1},
            bets=[(bet_id(BET_TYPE_TRIFECTA, (1, 2, 3)), 150)],
            forecast_path="f",
            forecast_sha256="a" * 64,
            adapter_version="v",
        )
