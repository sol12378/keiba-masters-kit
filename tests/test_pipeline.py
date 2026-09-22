import json

import pytest

from pykeiba.allocate import CandidateFilter, allocate, candidates, race_allowance
from pykeiba.dp import PolicySpec, lookup, simulate_table, solve
from pykeiba.model import build_features, train
from pykeiba.panel import load_panel, load_race
from pykeiba.synth import SeasonSpec, generate_season


@pytest.fixture(scope="module")
def panel(tmp_path_factory):
    directory = tmp_path_factory.mktemp("panel")
    generate_season(SeasonSpec(days=4, races_per_day=6, max_runners=9, seed=3), directory)
    return directory


@pytest.fixture(scope="module")
def races(panel):
    return list(load_panel(panel))


@pytest.fixture(scope="module")
def fitted(races):
    features = [build_features(race) for race in races]
    return features, train(features, holdout_from=races[-1].date)


def test_generated_panel_round_trips(panel, races):
    assert len(races) == 24
    for race in races:
        assert race.captured_at < race.post_at
        assert len(race.win_odds) >= 8
        assert race.settled


def test_a_malformed_race_is_rejected(tmp_path):
    path = tmp_path / "bad.json"
    path.write_text(json.dumps({"race_id": "short", "date": "20260404"}), encoding="utf-8")
    with pytest.raises(ValueError):
        load_race(path)


def test_captured_after_post_is_rejected(panel, tmp_path):
    source = next(iter(sorted(panel.glob("*/*.json"))))
    document = json.loads(source.read_text(encoding="utf-8"))
    document["captured_at"] = document["post_at"]
    path = tmp_path / "leaky.json"
    path.write_text(json.dumps(document), encoding="utf-8")
    with pytest.raises(ValueError, match="captured_at"):
        load_race(path)


def test_features_are_two_log_probability_columns(fitted):
    features, _ = fitted
    for item in features[:3]:
        assert item.x.shape == (len(item.combinations), 2)
        assert item.market.sum() == pytest.approx(1.0)
        assert (item.x < 0).all()  # log of a probability


def test_training_stays_within_bounds_and_keeps_a_holdout(fitted):
    _, model = fitted
    assert 0.5 <= model.coefficients[0] <= 1.5
    assert 0.0 <= model.coefficients[1] <= 0.5
    assert model.holdout_races > 0
    assert set(model.training_dates).isdisjoint(model.holdout_dates)


def test_the_market_column_dominates_the_blend(fitted):
    features, model = fitted
    from pykeiba.model import blended_probabilities

    probabilities = blended_probabilities(features[0], model)
    assert probabilities.sum() == pytest.approx(1.0)
    assert abs(probabilities - features[0].market).max() < 0.05


def test_race_allowance_spreads_the_budget_and_weights_later_races():
    early = race_allowance(1_000_000, remaining_slots=24, total_slots=24)
    late = race_allowance(1_000_000, remaining_slots=1, total_slots=24)
    assert early < late
    assert early % 100 == 0 and late % 100 == 0


def test_race_allowance_rejects_an_impossible_slot():
    with pytest.raises(ValueError):
        race_allowance(1_000_000, remaining_slots=0, total_slots=24)


def test_every_allocated_ticket_reaches_the_target_on_a_hit(races, fitted):
    features, model = fitted
    rule = CandidateFilter(min_odds=50.0, max_odds=8000.0)
    checked = 0
    for race, item in zip(races, features, strict=True):
        shortlist = candidates(race, item, model, rule)
        if not shortlist:
            continue
        plan = allocate(shortlist, bank=1_000_000, allowance=50_000, target=1_900_000)
        if not plan.tickets:
            continue
        checked += 1
        assert plan.stake <= plan.allowance
        for ticket in plan.tickets:
            assert ticket.stake % 100 == 0
            bank_on_hit = 1_000_000 - plan.stake + ticket.stake * ticket.odds * 0.80
            assert bank_on_hit >= plan.target - 1e-6
    assert checked > 0


def test_allocation_declines_when_the_target_is_already_met():
    plan = allocate([], bank=2_000_000, allowance=50_000, target=1_900_000)
    assert plan.tickets == [] and plan.reason == "target_already_met"


@pytest.fixture(scope="module")
def table():
    spec = PolicySpec(goal=1_900_000, races=24, bank_grid=10_000)
    return solve(spec)


def test_the_solved_value_respects_the_conservation_bound(table):
    assert table.expected["value_at_initial_bank"] <= table.expected["upper_bound_on_reach"] + 1e-9


def test_the_forward_simulation_is_at_least_the_solved_value(table):
    report = simulate_table(table, paths=4_000, seed=5)
    assert report["simulated_reach"] >= report["solved_value"] - 0.05
    assert report["simulated_reach"] <= report["upper_bound_on_reach"] + 0.05


def test_the_mean_final_bank_is_below_the_start_because_there_is_no_edge(table):
    report = simulate_table(table, paths=4_000, seed=5)
    assert report["mean_final_bank"] < table.spec["initial_bank"]


def test_a_goal_below_the_starting_bank_is_refused():
    with pytest.raises(ValueError):
        PolicySpec(goal=500_000, races=10)


def test_a_band_claiming_an_edge_is_still_bounded_by_its_return_rate():
    spec = PolicySpec(goal=2_000_000, races=12, bank_grid=25_000)
    assert spec.floor_for(1) == 0
    solved = solve(spec)
    assert solved.expected["upper_bound_on_reach"] == pytest.approx(0.80 * 1_000_000 / 2_000_000)


def test_table_lookup_returns_an_action_or_a_pass(table):
    action = lookup(table, races_remaining=24, bank=1_000_000)
    assert action.tickets >= 0
    if not action.is_pass:
        assert action.stake_each > 0
        assert action.total_spend == action.stake_each * action.tickets
