"""Command line entry point: ``python -m pykeiba <command>``."""

from __future__ import annotations

import argparse
import json
import sys
from datetime import UTC, datetime, timedelta
from pathlib import Path
from zoneinfo import ZoneInfo

from . import __version__
from .allocate import CandidateFilter
from .backtest import run_policy_table_strategy, run_target_strategy
from .dp import DEFAULT_BANDS, PolicySpec, PolicyTable, simulate_table, solve
from .model import Model, build_features, train
from .panel import load_panel, race_dates, write_race
from .plan import (
    BET_TYPE_TRIFECTA,
    bet_id,
    build_bundle,
    build_plan,
    file_sha256,
    marks_from_win_odds,
    race_date_for,
    write_bundle,
)
from .select import pick_near_odds
from .synth import SeasonSpec, generate_season


def _emit(document: dict) -> None:
    json.dump(document, sys.stdout, ensure_ascii=False, indent=2, sort_keys=True)
    sys.stdout.write("\n")


def command_synth(args: argparse.Namespace) -> int:
    spec = SeasonSpec(
        days=args.days,
        races_per_day=args.races_per_day,
        max_runners=args.max_runners,
        seed=args.seed,
    )
    summary = generate_season(spec, Path(args.out))
    summary["out"] = str(args.out)
    _emit(summary)
    return 0


def command_train(args: argparse.Namespace) -> int:
    races = list(load_panel(Path(args.panel)))
    features = []
    skipped = 0
    for race in races:
        try:
            features.append(build_features(race))
        except ValueError:
            skipped += 1
    dates = race_dates(Path(args.panel))
    holdout_from = args.holdout_from or (dates[-max(1, len(dates) // 3)] if len(dates) > 2 else None)
    model = train(features, holdout_from=holdout_from, ridge=args.ridge)
    path = model.to_json(Path(args.out))
    _emit({
        "model_path": str(path),
        "coefficients": model.coefficients,
        "training_races": model.training_races,
        "holdout_races": model.holdout_races,
        "holdout_from": holdout_from,
        "market_holdout_nll": model.market_holdout_nll,
        "model_holdout_nll": model.model_holdout_nll,
        "improvement": (
            None
            if model.market_holdout_nll is None
            else round(model.market_holdout_nll - model.model_holdout_nll, 6)
        ),
        "races_skipped_incomplete_market": skipped,
        "caveat": model.caveat,
    })
    return 0


def command_policy_table(args: argparse.Namespace) -> int:
    spec = PolicySpec(
        goal=args.goal,
        races=args.races,
        initial_bank=args.initial_bank,
        bank_grid=args.bank_grid,
        per_race_cap=args.per_race_cap,
        max_tickets=args.max_tickets,
        objective=args.objective,
        bands=DEFAULT_BANDS,
    )
    table = solve(spec, report_every=args.report_every)
    path = table.to_json(Path(args.out))
    _emit({
        "table_path": str(path),
        "states": len(table.rows),
        "expected": table.expected,
        "bands": [band["label"] for band in table.spec["bands"]],
    })
    return 0


def command_retime(args: argparse.Namespace) -> int:
    """Move one date's races to start a few minutes from now.

    The runtime schedules by wall clock: it submits at ``post - 300s`` and
    refuses anything past its hard cutoff.  A panel generated with historical
    post times therefore has nothing left to do.  Retiming lets a demo run
    against the real scheduler instead of a stubbed clock, which is the part
    worth seeing.
    """
    directory = Path(args.panel) / args.date
    if not directory.is_dir():
        raise SystemExit(f"no races for date {args.date} under {args.panel}")
    now = datetime.now(UTC).replace(second=0, microsecond=0)
    paths = sorted(directory.glob("*.json"))
    moved = []
    target_date = None
    for index, path in enumerate(paths):
        document = json.loads(path.read_text(encoding="utf-8"))
        post = now + timedelta(minutes=args.first_post_in + index * args.every)
        # The race date has to follow the post time in the policy's own
        # timezone, because the runtime cross-checks the two and refuses a plan
        # whose date and post time disagree.
        target_date = post.astimezone(ZoneInfo(args.timezone)).strftime("%Y%m%d")
        document["date"] = target_date
        document["post_at"] = post.strftime("%Y-%m-%dT%H:%M:%SZ")
        document["captured_at"] = (post - timedelta(minutes=10)).strftime("%Y-%m-%dT%H:%M:%SZ")
        write_race(Path(args.panel), document)
        if target_date != args.date:
            path.unlink()
        moved.append({"race_id": document["race_id"], "post_at": document["post_at"]})
    if target_date != args.date and not any(directory.iterdir()):
        directory.rmdir()

    _emit({"date": target_date, "races": len(moved), "now": now.strftime("%Y-%m-%dT%H:%M:%SZ"), "schedule": moved})
    return 0


def command_render_policy(args: argparse.Namespace) -> int:
    """Stamp a race date into the policy template.

    The runtime deliberately refuses a plan whose date does not match the
    policy it was armed with, so a policy is valid for exactly one day.  That
    is the mechanism that stops yesterday's bundle being replayed today.
    """
    template = Path(args.template).read_text(encoding="utf-8")
    iso_date = f"{args.date[:4]}-{args.date[4:6]}-{args.date[6:]}"
    rendered = template.replace("__RACE_DATE__", iso_date)
    document = json.loads(rendered)
    document["policy_id"] = f"{document['policy_id']}-{args.date}"
    out = Path(args.out or f"var/policy_{args.date}.json")
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(document, ensure_ascii=False, indent=1, sort_keys=True) + "\n", encoding="utf-8")
    _emit({
        "policy_path": str(out),
        "policy_id": document["policy_id"],
        "connection_date": document["connection_date"],
        "required_environment": document["submission"]["required_environment"],
    })
    return 0


def command_verify(args: argparse.Namespace) -> int:
    table = PolicyTable.from_json(Path(args.table))
    report = simulate_table(table, paths=args.paths, seed=args.seed)
    gap = report["solved_value"] - report["simulated_reach"]
    report["solved_minus_simulated"] = round(gap, 6)
    report["verdict"] = (
        "solver value is a lower bound, as intended"
        if gap <= 1e-6
        else "solver reports MORE than the forward simulation delivers; investigate"
    )
    _emit(report)
    return 0


def command_backtest(args: argparse.Namespace) -> int:
    races = list(load_panel(Path(args.panel)))
    model = Model.from_json(Path(args.model))
    if args.table:
        result = run_policy_table_strategy(
            races, model, PolicyTable.from_json(Path(args.table)), initial_bank=args.initial_bank
        )
    else:
        result = run_target_strategy(
            races,
            model,
            initial_bank=args.initial_bank,
            goal=args.goal,
            rule=CandidateFilter(min_odds=args.min_odds, max_odds=args.max_odds),
            odds_factor=args.odds_factor,
        )
    summary = result.summary()
    if args.out:
        Path(args.out).parent.mkdir(parents=True, exist_ok=True)
        Path(args.out).write_text(
            json.dumps(
                {"summary": summary, "races": [outcome.__dict__ for outcome in result.outcomes]},
                ensure_ascii=False,
                indent=1,
                sort_keys=True,
            )
            + "\n",
            encoding="utf-8",
        )
        summary["detail_path"] = args.out
    _emit(summary)
    return 0


def command_plan(args: argparse.Namespace) -> int:
    policy = json.loads(Path(args.policy).read_text(encoding="utf-8"))
    model = Model.from_json(Path(args.model))
    table = PolicyTable.from_json(Path(args.table))
    races = [race for race in load_panel(Path(args.panel)) if race.date == args.date]
    if not races:
        raise SystemExit(f"no races in the panel for date {args.date}")

    timing = policy["timing"]
    grid = int(table.spec["bank_grid"])
    horizon = int(table.spec["races"])
    bank = args.bank
    plans = []
    for index, race in enumerate(races):
        remaining = max(1, min(horizon, horizon - index))
        bucket = min(bank // grid * grid, max(table.banks))
        row = next(
            (candidate for candidate in table.rows
             if candidate["races_remaining"] == remaining and candidate["bank"] == bucket),
            None,
        )
        if row is None or row["action"] != "bet":
            continue
        features = build_features(race)
        picks = pick_near_odds(
            features, race.trifecta_odds, model,
            target_odds=float(row["target_odds"]), count=int(row["tickets"]),
        )
        stake_each = int(row["stake_each"])
        if not picks or stake_each < 100:
            continue
        bets = [(bet_id(BET_TYPE_TRIFECTA, pick.combination), stake_each) for pick in picks]
        plans.append(
            build_plan(
                plan_id=f"{race.race_id}-{args.date}",
                policy_id=policy["policy_id"],
                race_date=race_date_for(race.post_at, policy["timezone"]),
                race_id=race.race_id,
                post_at=race.post_at,
                marks=marks_from_win_odds(race.win_odds),
                bets=bets,
                forecast_path=str(race.source_path),
                forecast_sha256=file_sha256(race.source_path),
                adapter_version=f"pykeiba/{__version__}",
                model_sha256=file_sha256(Path(args.model)),
                created_at=datetime.now(UTC) if args.created_now else None,
                target_submit_seconds_before_post=timing["target_submit_seconds_before_post"],
                hard_cutoff_seconds_before_post=timing["hard_cutoff_seconds_before_post"],
            )
        )
        bank -= stake_each * len(picks)

    if not plans:
        raise SystemExit("the policy table instructed no bet for any race on this date")
    bundle = build_bundle(
        bundle_id=f"bundle-{args.date}",
        policy_id=policy["policy_id"],
        race_date=plans[0]["race_date"],
        plans=plans,
        source_path=str(args.panel),
        source_sha256=file_sha256(Path(args.table)),
    )
    path = write_bundle(Path(args.out), bundle)
    _emit({
        "bundle_path": str(path),
        "races": len(plans),
        "total_stake": sum(int(bet["money"]) for plan in plans for bet in plan["payload"]["bet_list"]),
        "bundle_sha256": bundle["bundle_sha256"],
    })
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="pykeiba", description="Model building for keiba-masters-kit")
    parser.add_argument("--version", action="version", version=f"pykeiba {__version__}")
    sub = parser.add_subparsers(dest="command", required=True)

    synth = sub.add_parser("synth", help="generate a synthetic panel")
    synth.add_argument("--out", default="data/synthetic")
    synth.add_argument("--days", type=int, default=6)
    synth.add_argument("--races-per-day", type=int, default=12)
    synth.add_argument("--max-runners", type=int, default=12)
    synth.add_argument("--seed", type=int, default=7)
    synth.set_defaults(handler=command_synth)

    fit = sub.add_parser("train", help="fit the two-column log-linear model")
    fit.add_argument("--panel", default="data/synthetic")
    fit.add_argument("--out", default="out/model.json")
    fit.add_argument("--holdout-from", default=None, help="YYYYMMDD; races on or after are held out")
    fit.add_argument("--ridge", type=float, default=0.1)
    fit.set_defaults(handler=command_train)

    table = sub.add_parser("policy-table", help="solve the state-dependent policy table")
    table.add_argument("--out", default="out/policy_table.json")
    table.add_argument("--goal", type=int, default=1_900_000)
    table.add_argument("--races", type=int, default=72)
    table.add_argument("--initial-bank", type=int, default=1_000_000)
    table.add_argument("--bank-grid", type=int, default=5_000)
    table.add_argument("--per-race-cap", type=int, default=20_000)
    table.add_argument("--max-tickets", type=int, default=10)
    table.add_argument("--objective", choices=("reach", "bank"), default="reach")
    table.add_argument("--report-every", type=int, default=0)
    table.set_defaults(handler=command_policy_table)

    retime = sub.add_parser("retime", help="move one date's races to start shortly from now")
    retime.add_argument("--panel", default="data/synthetic")
    retime.add_argument("--date", required=True, help="YYYYMMDD")
    retime.add_argument("--first-post-in", type=int, default=7, help="minutes until the first race posts")
    retime.add_argument("--every", type=int, default=2, help="minutes between races")
    retime.add_argument("--timezone", default="Asia/Tokyo")
    retime.set_defaults(handler=command_retime)

    render = sub.add_parser("render-policy", help="stamp a race date into the policy template")
    render.add_argument("--template", default="configs/voting_policy_demo.json.template")
    render.add_argument("--date", required=True, help="YYYYMMDD")
    render.add_argument("--out", default=None)
    render.set_defaults(handler=command_render_policy)

    verify = sub.add_parser("verify", help="forward-simulate a solved table and compare")
    verify.add_argument("--table", default="out/policy_table.json")
    verify.add_argument("--paths", type=int, default=20_000)
    verify.add_argument("--seed", type=int, default=11)
    verify.set_defaults(handler=command_verify)

    back = sub.add_parser("backtest", help="replay a panel through a strategy")
    back.add_argument("--panel", default="data/synthetic")
    back.add_argument("--model", default="out/model.json")
    back.add_argument("--table", default=None, help="use the policy table instead of the target strategy")
    back.add_argument("--initial-bank", type=int, default=1_000_000)
    back.add_argument("--goal", type=int, default=1_900_000)
    back.add_argument("--min-odds", type=float, default=375.0)
    back.add_argument("--max-odds", type=float, default=8000.0)
    back.add_argument("--odds-factor", type=float, default=0.80)
    back.add_argument("--out", default=None)
    back.set_defaults(handler=command_backtest)

    make_plan = sub.add_parser("plan", help="write a vote plan bundle for votingd")
    make_plan.add_argument("--panel", default="data/synthetic")
    make_plan.add_argument("--date", required=True, help="YYYYMMDD")
    make_plan.add_argument("--model", default="out/model.json")
    make_plan.add_argument("--table", default="out/policy_table.json")
    make_plan.add_argument("--policy", default="configs/voting_policy_demo.json")
    make_plan.add_argument("--bank", type=int, default=1_000_000)
    make_plan.add_argument("--out", default="var/plans/bundle.json")
    make_plan.add_argument("--created-now", action="store_true")
    make_plan.set_defaults(handler=command_plan)

    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    return args.handler(args)


if __name__ == "__main__":
    raise SystemExit(main())
