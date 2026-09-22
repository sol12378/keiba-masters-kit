"""The submission package has to keep working, and keep saying the same thing.

These are not tests of the strategy. They check that the frozen record stays
executable and stays consistent with the numbers it claims, so a refactor
somewhere else cannot quietly invalidate what was handed to the organiser.
"""

import json
import subprocess
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
SUBMISSION = ROOT / "submission"


@pytest.fixture(scope="module")
def ledger():
    return json.loads((SUBMISSION / "ledger" / "summary.json").read_text(encoding="utf-8"))


@pytest.fixture(scope="module")
def races():
    return json.loads((SUBMISSION / "ledger" / "races.json").read_text(encoding="utf-8"))


def test_phase2_coefficients_are_recoverable_from_the_published_surface():
    result = subprocess.run(
        [sys.executable, str(SUBMISSION / "phase2" / "reproduce.py")],
        capture_output=True, text=True, check=False,
    )
    assert result.returncode == 0, result.stdout + result.stderr
    report = json.loads(result.stdout[: result.stdout.rindex("}") + 1])
    assert report["verdict"] == "reproduced"
    assert report["coefficient_difference"][0] < 1e-6
    assert report["recorded_coefficients"] == [0.9265476143601139, 0.0]


def test_the_holdout_improvement_is_stated_as_the_small_number_it_is():
    model = json.loads((SUBMISSION / "phase2" / "model.json").read_text(encoding="utf-8"))
    improvement = model["market_holdout_nll"] - model["model_holdout_nll"]
    assert 0 < improvement < 0.01, "an improvement this size is not evidence of an edge"
    assert model["coefficients"][1] == 0.0, "the Harville term sat at its bound"


def test_phase1_runs_the_real_planner_on_synthetic_odds(tmp_path):
    panel = ROOT / "data" / "synthetic"
    if not panel.is_dir():
        pytest.skip("run `python -m pykeiba synth` first")
    surface = tmp_path / "surface.json"
    subprocess.run(
        [sys.executable, str(SUBMISSION / "tools" / "synthetic_surface.py"),
         "--panel", str(panel), "--out", str(surface)],
        capture_output=True, text=True, check=True,
    )
    race_id = json.loads(surface.read_text(encoding="utf-8"))["race"]["race_id"]

    state = tmp_path / "state.json"
    state.write_text(json.dumps(
        {"races_remaining": 264, "bank": 1_000_000, "turnover": 0, "front_races_done": 0}))

    from datetime import datetime, timedelta, timezone

    post = (datetime.now(timezone(timedelta(hours=9))) + timedelta(minutes=30)).replace(microsecond=0)
    plan_path = tmp_path / "plan.json"
    result = subprocess.run(
        [sys.executable, str(SUBMISSION / "phase1" / "build_vote_plan.py"),
         "--race-id", race_id, "--odds", str(surface), "--state", str(state),
         "--post-time", post.isoformat(), "--out", str(plan_path)],
        capture_output=True, text=True, check=False,
    )
    assert result.returncode == 0, result.stdout + result.stderr
    plan = json.loads(plan_path.read_text(encoding="utf-8"))
    assert plan["policy_id"] == "COMPETITION-2026-V17"
    assert plan["payload"]["bet_list"], "the planner produced no bets"
    for bet in plan["payload"]["bet_list"]:
        assert int(bet["money"]) % 100 == 0


def test_the_ledger_totals_match_what_the_submission_claims(ledger, races):
    confirmed = [r for r in races if r["state"] == "CONFIRMED"]
    assert len(confirmed) == 204
    assert ledger["races_confirmed"] == 204
    assert ledger["total_stake"] == 1_227_300
    assert ledger["total_payout"] == 5_725_360
    assert sum(r["stake"] for r in confirmed) == ledger["total_stake"]


def test_the_unexplained_credit_is_carried_rather_than_absorbed(ledger):
    assert ledger["unexplained_credit"] == 6_200
    assert ledger["official_final_bank"] - ledger["implied_final_bank"] == 6_200
    assert ledger["reconciliation_notes"]["commentary"], "the gap must be explained in words too"


def test_every_race_carries_its_own_payload_hash(races):
    for race in races:
        assert len(race["payload_sha256"]) == 64
        assert race["stake"] == sum(int(bet["money"]) for bet in race["bets"])


def test_no_absolute_home_path_leaks_into_the_submission():
    for path in SUBMISSION.rglob("*"):
        if not path.is_file() or path.suffix == ".npz":
            continue
        text = path.read_text(encoding="utf-8", errors="ignore")
        assert "/Users/" not in text, f"{path} still carries an absolute home path"


def test_the_policy_breakdown_matches_the_races(ledger, races):
    """The README claims a specific split of decision mechanisms. Check it."""
    confirmed = [r for r in races if r["state"] == "CONFIRMED"]
    by_policy = {}
    for race in confirmed:
        by_policy[race["policy_id"]] = by_policy.get(race["policy_id"], 0) + 1
    assert {k: v["races"] for k, v in ledger["by_policy"].items()} == by_policy

    mechanism = ledger["decision_mechanism"]
    assert set(mechanism) >= set(by_policy), "a policy has no recorded mechanism"

    table_driven = sum(count for policy, count in by_policy.items()
                       if mechanism[policy] == "policy_table_lookup")
    fitted_model = sum(count for policy, count in by_policy.items()
                       if "fitted_model" in mechanism[policy])
    assert table_driven == 29, "only the first day consulted the policy table"
    assert fitted_model == 14, "only 2026-09-20 read a fitted model"
