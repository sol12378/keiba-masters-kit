"""Race panel: one JSON file per race, stored as ``panel/<date>/<race_id>.json``.

Schema::

    {
      "race_id":  "202606040411",
      "date":     "20260926",
      "post_at":  "2026-09-26T05:00:00Z",
      "captured_at": "2026-09-26T04:50:00Z",
      "win":      {"1": 3.2, "2": 11.4, ...},
      "trifecta": {"1-2-3": 320.4, ...},
      "result": {
        "finish": [5, 2, 7],
        "payout_per_100": {"5-2-7": 4321.0}
      }
    }

``captured_at`` must be before ``post_at``. ``result`` is only used for
settlement and is kept in a separate field.
"""

from __future__ import annotations

import json
import re
from collections.abc import Iterator
from dataclasses import dataclass, field
from datetime import UTC, datetime
from pathlib import Path

__all__ = ["Race", "load_race", "load_panel", "race_dates", "write_race"]

RACE_ID_PATTERN = re.compile(r"^[A-Za-z0-9]{12}$")
DATE_PATTERN = re.compile(r"^\d{8}$")


@dataclass(frozen=True)
class Race:
    """One race at one decision time."""

    race_id: str
    date: str
    post_at: datetime
    captured_at: datetime
    win_odds: dict[int, float]
    trifecta_odds: dict[tuple[int, int, int], float]
    result: dict | None = field(default=None)
    source_path: Path | None = field(default=None)

    @property
    def settled(self) -> bool:
        return bool(self.result and self.result.get("finish"))

    @property
    def winning_triple(self) -> tuple[int, int, int] | None:
        if not self.settled:
            return None
        finish = [int(horse) for horse in self.result["finish"][:3]]
        if len(finish) != 3:
            return None
        return (finish[0], finish[1], finish[2])

    def payout_per_100(self, triple: tuple[int, int, int]) -> float | None:
        """Official return per 100 points, or None if unknown.

        A settled race with no entry for ``triple`` returns 0.0 (the ticket lost).
        """
        if not self.settled:
            return None
        payouts = self.result.get("payout_per_100") or {}
        if not payouts:
            return None
        return float(payouts.get(_triple_label(triple), 0.0))


def _triple_label(triple: tuple[int, int, int]) -> str:
    return "-".join(str(int(horse)) for horse in triple)


def _parse_time(value: str, field_name: str) -> datetime:
    try:
        parsed = datetime.fromisoformat(str(value).replace("Z", "+00:00"))
    except ValueError as error:
        raise ValueError(f"{field_name} is not an ISO-8601 timestamp: {value!r}") from error
    if parsed.tzinfo is None:
        raise ValueError(f"{field_name} must carry a timezone offset: {value!r}")
    return parsed.astimezone(UTC)


def load_race(path: Path) -> Race:
    """Read and validate one race file.  Fails closed on anything malformed."""
    document = json.loads(Path(path).read_text(encoding="utf-8"))
    race_id = str(document.get("race_id", ""))
    if not RACE_ID_PATTERN.match(race_id):
        raise ValueError(f"{path}: race_id must be 12 ASCII letters/digits, got {race_id!r}")
    date = str(document.get("date", ""))
    if not DATE_PATTERN.match(date):
        raise ValueError(f"{path}: date must be YYYYMMDD, got {date!r}")
    post_at = _parse_time(document.get("post_at", ""), "post_at")
    captured_at = _parse_time(document.get("captured_at", document.get("post_at", "")), "captured_at")
    if captured_at >= post_at:
        raise ValueError(f"{path}: captured_at must be strictly before post_at")

    win_odds = {int(horse): float(price) for horse, price in (document.get("win") or {}).items()}
    if len(win_odds) < 3:
        raise ValueError(f"{path}: at least three runners must be quoted")
    if any(price <= 0 for price in win_odds.values()):
        raise ValueError(f"{path}: every win price must be positive")

    trifecta_odds: dict[tuple[int, int, int], float] = {}
    for label, price in (document.get("trifecta") or {}).items():
        parts = tuple(int(part) for part in str(label).split("-"))
        if len(parts) != 3 or len(set(parts)) != 3:
            raise ValueError(f"{path}: {label!r} is not an ordered triple of distinct horses")
        if float(price) <= 0:
            raise ValueError(f"{path}: trifecta price for {label} must be positive")
        trifecta_odds[parts] = float(price)

    return Race(
        race_id=race_id,
        date=date,
        post_at=post_at,
        captured_at=captured_at,
        win_odds=win_odds,
        trifecta_odds=trifecta_odds,
        result=document.get("result"),
        source_path=Path(path),
    )


def load_panel(root: Path, *, dates: list[str] | None = None) -> Iterator[Race]:
    """Yield every race in the panel, ordered by date then post time."""
    root = Path(root)
    if not root.is_dir():
        raise FileNotFoundError(f"panel directory not found: {root}")
    races: list[Race] = []
    for path in sorted(root.glob("*/*.json")):
        if dates is not None and path.parent.name not in dates:
            continue
        races.append(load_race(path))
    races.sort(key=lambda race: (race.date, race.post_at, race.race_id))
    yield from races


def race_dates(root: Path) -> list[str]:
    """Sorted list of dates present in the panel."""
    return sorted(
        child.name for child in Path(root).iterdir() if child.is_dir() and DATE_PATTERN.match(child.name)
    )


def write_race(root: Path, race: dict) -> Path:
    """Write one race file into ``root/<date>/<race_id>.json``."""
    directory = Path(root) / str(race["date"])
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / f"{race['race_id']}.json"
    path.write_text(json.dumps(race, ensure_ascii=False, indent=1, sort_keys=True) + "\n", encoding="utf-8")
    return path
