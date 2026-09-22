#!/usr/bin/env python3
"""Build the L2 evidence: the NLL surface of the 2026-09-20 model.

PUBLISHED AS A RECORD of how submission/phase2/nll_surface.npz was produced.
It needs the T-10 snapshots, which are not in this repository.

Runs against the private repository, where the T-10 snapshots live.  It
reproduces exactly what ``competition_target_optimizer_20260920.historical``
and ``.train`` read, then writes out only the objective's *value surface* over
a grid of the two coefficients.

That surface is enough for a third party to re-run the optimization and
recover the fitted coefficients, and it contains no odds: a grid of objective
values cannot be inverted back into the per-combination prices behind it.
"""
from __future__ import annotations

import hashlib
import json
import os
import sys
import time
from pathlib import Path

import numpy as np
from scipy.optimize import minimize
from scipy.special import logsumexp

# The private research repository, where the T-10 snapshots live.  Point this
# at your own checkout; there is nothing to run here without that data.
ROOT = Path(os.environ.get("KEIBA_RESEARCH_ROOT", "../keiba")).expanduser().resolve()
sys.path[:0] = [str(ROOT), str(ROOT / "scripts")]

import competition_target_optimizer_20260920 as OPT  # noqa: E402

OUT = Path(sys.argv[1] if len(sys.argv) > 1 else "/tmp/nll_surface.json")

C0_BOUNDS = (0.5, 1.5)
C1_BOUNDS = (0.0, 0.5)
C0_STEPS = 501   # step 0.002
C1_STEPS = 101   # step 0.005
CHUNK = 4096     # grid columns per matmul


def nll(coefficients, subset) -> float:
    coefficients = np.asarray(coefficients, dtype=float)
    return float(np.mean([
        logsumexp(record["x"] @ coefficients)
        - logsumexp((record["x"] @ coefficients)[record["winner_indexes"]])
        for record in subset
    ]))


def surface(subset, coefficient_grid: np.ndarray) -> np.ndarray:
    """Mean per-race NLL at every column of ``coefficient_grid`` (2 x G).

    One matmul per race per chunk: (n_combinations x 2) @ (2 x chunk).  The
    per-race loop stays because n_combinations differs between races.
    """
    total = np.zeros(coefficient_grid.shape[1])
    started = time.time()
    for index, record in enumerate(subset, start=1):
        x = np.ascontiguousarray(record["x"])
        winners = record["winner_indexes"]
        for start in range(0, coefficient_grid.shape[1], CHUNK):
            block = coefficient_grid[:, start:start + CHUNK]
            logits = x @ block                       # (n_combinations, chunk)
            total[start:start + block.shape[1]] += (
                logsumexp(logits, axis=0) - logsumexp(logits[winners, :], axis=0)
            )
        if index % 25 == 0:
            print(f"    {index}/{len(subset)} races  {time.time() - started:.0f}s", flush=True)
    return total / len(subset)


def main() -> None:
    print("loading the T-10 panel ...", flush=True)
    records = OPT.historical()
    training = [r for r in records if r["date"] <= "20260912"]
    holdout = [r for r in records if r["date"] > "20260912"]
    sizes = [r["x"].shape[0] for r in records]
    print(f"  training {len(training)}  holdout {len(holdout)}  "
          f"combinations per race min={min(sizes)} max={max(sizes)}", flush=True)

    c0_values = np.linspace(*C0_BOUNDS, C0_STEPS)
    c1_values = np.linspace(*C1_BOUNDS, C1_STEPS)
    mesh0, mesh1 = np.meshgrid(c0_values, c1_values, indexing="ij")
    grid = np.vstack([mesh0.ravel(), mesh1.ravel()])

    print(f"evaluating {grid.shape[1]} grid points on the training set ...", flush=True)
    training_surface = surface(training, grid).reshape(C0_STEPS, C1_STEPS)
    print("evaluating the holdout set ...", flush=True)
    holdout_surface = surface(holdout, grid).reshape(C0_STEPS, C1_STEPS)

    fit = minimize(
        lambda c: nll(c, training) + 0.1 * ((c[0] - 1) ** 2 + c[1] ** 2),
        [1.0, 0.0], bounds=[C0_BOUNDS, C1_BOUNDS], method="L-BFGS-B",
    )
    exact = {
        "coefficients": [float(v) for v in fit.x],
        "market_holdout_nll": nll([1.0, 0.0], holdout),
        "model_holdout_nll": nll(fit.x, holdout),
        "training_nll_at_fit": nll(fit.x, training),
    }
    print("  exact fit:", json.dumps(exact, indent=1), flush=True)

    document = {
        "schema": "keiba-masters-kit.nll_surface.v1",
        "objective": (
            "mean per-race conditional NLL of the winning trifecta combination; "
            "features are [log market probability, log Harville probability]"
        ),
        "ridge": {"lambda": 0.1, "centre": [1.0, 0.0]},
        "bounds": {"c0": list(C0_BOUNDS), "c1": list(C1_BOUNDS)},
        "grid": {"c0_steps": C0_STEPS, "c1_steps": C1_STEPS,
                 "c0_step": float(c0_values[1] - c0_values[0]),
                 "c1_step": float(c1_values[1] - c1_values[0])},
        "counts": {"training_races": len(training), "holdout_races": len(holdout),
                   "combinations_per_race": {"min": int(min(sizes)), "max": int(max(sizes))}},
        "training_last_date": "20260912",
        "holdout_dates": ["20260913", "20260919"],
        "exact_reference": exact,
        "note": (
            "Objective values only. A grid of means over races carries no "
            "per-combination price and cannot be inverted into the odds behind it."
        ),
    }

    OUT.parent.mkdir(parents=True, exist_ok=True)
    npz = OUT.with_suffix(".npz")
    np.savez_compressed(npz, c0=c0_values, c1=c1_values,
                        training_nll=training_surface, holdout_nll=holdout_surface)
    document["surface_file"] = npz.name
    document["surface_sha256"] = hashlib.sha256(npz.read_bytes()).hexdigest()
    OUT.write_text(json.dumps(document, ensure_ascii=False, indent=1, sort_keys=True) + "\n",
                   encoding="utf-8")
    print(f"wrote {npz} ({npz.stat().st_size / 1024:.0f} KiB) and {OUT}")


if __name__ == "__main__":
    main()
