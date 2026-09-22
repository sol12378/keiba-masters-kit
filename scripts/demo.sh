#!/bin/bash
# End-to-end local run: synthetic panel -> model -> policy table -> plan bundle
# -> voting daemon -> paper submission -> reconciliation -> ledger.
#
# Nothing here touches the network or needs an account.  The driver is the
# paper driver, which accepts votes into a local file and answers the same
# confirmation query the live endpoint answers.
set -euo pipefail

cd "$(dirname "$0")/.."
PYTHON="${PYTHON:-.venv/bin/python}"
PANEL="${PANEL:-data/synthetic}"
FIRST_POST_IN="${FIRST_POST_IN:-7}"
EVERY="${EVERY:-2}"

say() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

say "0/8  Reset this demo's runtime state"
# Only this demo's own directory, and only ever under var/.  The runtime is
# append-only by design: it refuses to re-import a plan id whose content
# differs, which is exactly what a second demo run would try to do.
STATE_DIR="var/voting-demo"
case "$STATE_DIR" in var/*) rm -rf -- "$STATE_DIR" ;; *) echo "refusing to clear $STATE_DIR" >&2; exit 1 ;; esac
echo "cleared $STATE_DIR"

say "1/8  Build the binaries"
mkdir -p bin var
go build -o bin/votingd ./cmd/votingd
go build -o bin/votectl ./cmd/votectl

say "2/8  Generate a synthetic season (no external data is used)"
"$PYTHON" -m pykeiba synth --out "$PANEL" --days 6 --races-per-day 12 --seed 7

say "3/8  Fit the two-column model on the earlier dates, hold out the later ones"
"$PYTHON" -m pykeiba train --panel "$PANEL" --out out/model.json

say "4/8  Solve the policy table and check it against the conservation bound"
"$PYTHON" -m pykeiba policy-table --out out/policy_table.json --goal 1900000 --races 72
"$PYTHON" -m pykeiba verify --table out/policy_table.json

say "5/8  Move the last day's races to start in ${FIRST_POST_IN} minutes"
RETIME=$("$PYTHON" -m pykeiba retime --panel "$PANEL" --date 20260509 \
  --first-post-in "$FIRST_POST_IN" --every "$EVERY")
echo "$RETIME" | head -8
RACE_DATE=$(echo "$RETIME" | "$PYTHON" -c 'import json,sys; print(json.load(sys.stdin)["date"])')

say "6/8  Render a policy for ${RACE_DATE} and build the plan bundle"
"$PYTHON" -m pykeiba render-policy --date "$RACE_DATE" --out "var/policy_${RACE_DATE}.json"
BUNDLE="var/plans/bundle_${RACE_DATE}.json"
"$PYTHON" -m pykeiba plan --panel "$PANEL" --date "$RACE_DATE" \
  --model out/model.json --table out/policy_table.json \
  --policy "var/policy_${RACE_DATE}.json" --out "$BUNDLE" --created-now
BUNDLE_SHA=$("$PYTHON" -c "import json,sys;print(json.load(open('$BUNDLE'))['bundle_sha256'])")

say "7/8  Start votingd on the paper driver and arm the bundle"
# Submission is gated on a date-scoped environment variable that the policy
# names.  It is deliberately awkward: arming a day has to be a decision
# someone makes today, not a flag left on from last week.
export KEIBA_ENABLE_SUBMISSION="${RACE_DATE:0:4}-${RACE_DATE:4:2}-${RACE_DATE:6:2}"
export KEIBA_LOGIN_ID="${KEIBA_LOGIN_ID:-local}"
export KEIBA_PASSWORD="${KEIBA_PASSWORD:-local}"
./bin/votingd --policy "var/policy_${RACE_DATE}.json" --driver paper \
  >> "var/votingd.${RACE_DATE}.log" 2>&1 &
VOTINGD_PID=$!
trap 'kill "$VOTINGD_PID" 2>/dev/null || true' EXIT
for _ in $(seq 1 50); do
  if ./bin/votectl --policy "var/policy_${RACE_DATE}.json" status >/dev/null 2>&1; then break; fi
  sleep 0.2
done
./bin/votectl --policy "var/policy_${RACE_DATE}.json" import-day --file "$BUNDLE"
./bin/votectl --policy "var/policy_${RACE_DATE}.json" arm-day --file "$BUNDLE" --confirm-sha256 "$BUNDLE_SHA"

say "8/8  Watch the daemon submit and reconcile"
echo "The first race submits about $((FIRST_POST_IN - 5)) minutes from now."
echo "Follow it with:   ./bin/votectl --policy var/policy_${RACE_DATE}.json status"
echo "Paper ledger:     var/voting-demo/paper_state.json"
echo "Event journal:    var/voting-demo/events.jsonl"
echo "Daemon log:       var/votingd.${RACE_DATE}.log"
echo
echo "Press Ctrl-C to stop the daemon."
wait "$VOTINGD_PID"
