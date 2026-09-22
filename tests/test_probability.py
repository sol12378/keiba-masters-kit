import itertools

import pytest

from pykeiba.harville import combination_probability, order_probability, trifecta_surface
from pykeiba.odds import normalized_probabilities, overround, win_probabilities


def test_overround_is_above_one_for_a_real_board():
    # A pool that returns 80% of turnover quotes prices summing to 1/0.8.
    board = {1: 2.0, 2: 4.0, 3: 8.0, 4: 8.0}
    assert overround(board) == pytest.approx(1.0)


def test_win_probabilities_sum_to_one_and_preserve_order():
    board = {1: 2.5, 2: 5.0, 3: 20.0}
    probabilities = win_probabilities(board)
    assert sum(probabilities.values()) == pytest.approx(1.0)
    assert probabilities[1] > probabilities[2] > probabilities[3]


def test_win_probabilities_reject_a_nonpositive_price():
    with pytest.raises(ValueError):
        win_probabilities({1: 2.0, 2: 0.0})


def test_normalized_probabilities_handle_combination_labels():
    probabilities = normalized_probabilities({"1-2-3": 100.0, "1-3-2": 300.0})
    assert sum(probabilities.values()) == pytest.approx(1.0)
    assert probabilities["1-2-3"] == pytest.approx(0.75)


def test_harville_over_all_orders_of_the_whole_field_sums_to_one():
    win_p = win_probabilities({1: 2.0, 2: 4.0, 3: 8.0, 4: 8.0})
    total = sum(order_probability(order, win_p) for order in itertools.permutations(win_p, len(win_p)))
    assert total == pytest.approx(1.0)


def test_harville_trifecta_surface_sums_to_one():
    win_p = win_probabilities({1: 2.0, 2: 3.0, 3: 6.0, 4: 12.0, 5: 12.0})
    assert sum(trifecta_surface(win_p).values()) == pytest.approx(1.0)


def test_unordered_combination_is_the_sum_of_its_orders():
    win_p = win_probabilities({1: 2.0, 2: 3.0, 3: 6.0, 4: 12.0})
    ordered = sum(order_probability(order, win_p) for order in itertools.permutations((1, 2, 3)))
    assert combination_probability((1, 2, 3), win_p, ordered=False) == pytest.approx(ordered)


def test_repeating_a_horse_is_impossible():
    win_p = win_probabilities({1: 2.0, 2: 3.0, 3: 6.0})
    assert order_probability((1, 1, 2), win_p) == 0.0
