#!/usr/bin/env python3
"""Re-derive the 2026-09-20 model coefficients from nll_surface.npz.

The training odds cannot be redistributed, so the objective values on a grid
of the two coefficients are published instead. This checks the optimisation,
not the feature construction (model.json lists the SHA-256 of the inputs).

    python3 submission/phase2/reproduce.py
"""

from __future__ import annotations

import json
from pathlib import Path

import numpy as np
from scipy.interpolate import RectBivariateSpline
from scipy.optimize import minimize

HERE = Path(__file__).resolve().parent

#: Allowed difference from the recorded run (grid step is 0.002 in c0).
COEFFICIENT_TOLERANCE = 1e-6
NLL_TOLERANCE = 1e-6


def load() -> tuple[dict, dict, dict]:
    metadata = json.loads((HERE / "nll_surface.json").read_text(encoding="utf-8"))
    model = json.loads((HERE / "model.json").read_text(encoding="utf-8"))
    arrays = np.load(HERE / "nll_surface.npz")
    return metadata, model, arrays


def refit(metadata: dict, arrays) -> tuple[np.ndarray, RectBivariateSpline, RectBivariateSpline]:
    c0, c1 = arrays["c0"], arrays["c1"]
    training = RectBivariateSpline(c0, c1, arrays["training_nll"], kx=3, ky=3)
    holdout = RectBivariateSpline(c0, c1, arrays["holdout_nll"], kx=3, ky=3)

    ridge = metadata["ridge"]["lambda"]
    centre = metadata["ridge"]["centre"]

    def objective(coefficients: np.ndarray) -> float:
        penalty = ridge * ((coefficients[0] - centre[0]) ** 2 + (coefficients[1] - centre[1]) ** 2)
        return float(training(coefficients[0], coefficients[1])[0, 0]) + penalty

    # Start from the best grid point, not from the recorded coefficients.
    penalties = ridge * ((c0[:, None] - centre[0]) ** 2 + (c1[None, :] - centre[1]) ** 2)
    row, column = np.unravel_index(np.argmin(arrays["training_nll"] + penalties),
                                   arrays["training_nll"].shape)
    bounds = [tuple(metadata["bounds"]["c0"]), tuple(metadata["bounds"]["c1"])]
    fit = minimize(objective, [c0[row], c1[column]], bounds=bounds, method="L-BFGS-B")
    if not fit.success:
        raise SystemExit(f"re-fit failed: {fit.message}")
    return fit.x, training, holdout


def main() -> int:
    metadata, model, arrays = load()
    recovered, _, holdout = refit(metadata, arrays)
    recorded = model["coefficients"]

    market_nll = float(holdout(1.0, 0.0)[0, 0])
    model_nll = float(holdout(recovered[0], recovered[1])[0, 0])

    report = {
        "recorded_coefficients": recorded,
        "recovered_coefficients": [float(v) for v in recovered],
        "coefficient_difference": [abs(float(a) - float(b)) for a, b in zip(recovered, recorded)],
        "recorded_market_holdout_nll": model["market_holdout_nll"],
        "recovered_market_holdout_nll": market_nll,
        "recorded_model_holdout_nll": model["model_holdout_nll"],
        "recovered_model_holdout_nll": model_nll,
        "training_races": model["training_races"],
        "holdout_races": model["holdout_races"],
        "holdout_improvement_nats": model["market_holdout_nll"] - model["model_holdout_nll"],
    }

    failures = []
    for index, difference in enumerate(report["coefficient_difference"]):
        if difference > COEFFICIENT_TOLERANCE:
            failures.append(f"coefficient {index} differs by {difference:.3e}")
    if abs(market_nll - model["market_holdout_nll"]) > NLL_TOLERANCE:
        failures.append("market holdout NLL differs")
    if abs(model_nll - model["model_holdout_nll"]) > NLL_TOLERANCE:
        failures.append("model holdout NLL differs")

    report["verdict"] = "reproduced" if not failures else "MISMATCH: " + "; ".join(failures)
    print(json.dumps(report, ensure_ascii=False, indent=2))

    if failures:
        return 1
    print(
        "\nReproduced. The Harville coefficient is at its lower bound and the holdout "
        f"improvement is {report['holdout_improvement_nats']:.6f} nats out of "
        f"{model['market_holdout_nll']:.3f}, so this is not evidence of an edge."
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
