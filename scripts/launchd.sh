#!/bin/bash
# Install, inspect and remove the launchd agent for votingd (macOS only).
#
#   scripts/launchd.sh install --policy var/policy_20260922.json [--driver paper]
#   scripts/launchd.sh status
#   scripts/launchd.sh logs
#   scripts/launchd.sh uninstall
set -euo pipefail

cd "$(dirname "$0")/.."
PROJECT_ROOT="$(pwd -P)"
LABEL_PREFIX="${LABEL_PREFIX:-local.keiba-masters-kit}"
LABEL="${LABEL_PREFIX}.votingd"
AGENT_DIR="${HOME}/Library/LaunchAgents"
PLIST="${AGENT_DIR}/${LABEL}.plist"
TEMPLATE="launchd/templates/votingd.plist.template"
TIMEZONE="${TZ:-Asia/Tokyo}"
DOMAIN="gui/$(id -u)"

usage() {
  echo "usage: scripts/launchd.sh <install|status|logs|uninstall> [--policy FILE] [--driver paper|live] [--enable-submission]" >&2
  exit 2
}

command="${1:-}"; shift || usage
policy=""
driver="paper"
enable_submission=0
while [ $# -gt 0 ]; do
  case "$1" in
    --policy) policy="${2:-}"; shift 2 ;;
    --driver) driver="${2:-}"; shift 2 ;;
    --enable-submission) enable_submission=1; shift ;;
    *) usage ;;
  esac
done

case "$command" in
  install)
    [ -n "$policy" ] || { echo "--policy is required" >&2; exit 2; }
    [ -f "$policy" ] || { echo "policy not found: $policy" >&2; exit 2; }
    case "$driver" in
      paper) ;;
      live)
        # Ask for confirmation before installing with the live driver.
        echo "The 'live' driver submits votes to the official contest endpoint."
        echo "It only works while the contest is open, and it uses your account."
        printf "Type 'live' to confirm: "
        read -r reply
        [ "$reply" = "live" ] || { echo "aborted" >&2; exit 1; }
        ;;
      *) echo "unknown driver: $driver" >&2; exit 2 ;;
    esac
    [ -x bin/votingd ] || go build -o bin/votingd ./cmd/votingd
    mkdir -p "$AGENT_DIR" var/log

    # launchd agents do not inherit the shell environment, so the day cannot be
    # armed unless --enable-submission copies the policy's required_environment
    # (a date, not a credential) into the plist.
    if [ "$enable_submission" = "0" ]; then
      echo "note: installed without --enable-submission, so the agent will run"
      echo "      but refuse to arm a day. Re-install with --enable-submission"
      echo "      when you actually intend to submit."
    fi
    LABEL="$LABEL" PROJECT_ROOT="$PROJECT_ROOT" POLICY="$policy" DRIVER="$driver" \
    TIMEZONE="$TIMEZONE" ENABLE_SUBMISSION="$enable_submission" TEMPLATE="$TEMPLATE" \
    /usr/bin/python3 - "$PLIST" <<'PYEOF'
import json, os, sys
from xml.sax.saxutils import escape

template = open(os.environ["TEMPLATE"], encoding="utf-8").read()
for key in ("LABEL", "PROJECT_ROOT", "POLICY", "DRIVER", "TIMEZONE"):
    template = template.replace(f"__{key}__", escape(os.environ[key]))

extra = ""
if os.environ["ENABLE_SUBMISSION"] == "1":
    required = json.load(open(os.environ["POLICY"], encoding="utf-8"))["submission"]
    pairs = sorted((required.get("required_environment") or {}).items())
    extra = "\n".join(
        f"    <key>{escape(k)}</key>\n    <string>{escape(str(v))}</string>" for k, v in pairs
    )
    for k, v in pairs:
        print(f"submission environment set in the plist: {k}={v}")

lines = [line for line in template.splitlines() if line != "__EXTRA_ENV__" or extra]
rendered = "\n".join(line if line != "__EXTRA_ENV__" else extra for line in lines)
open(sys.argv[1], "w", encoding="utf-8").write(rendered + "\n")
PYEOF
    plutil -lint "$PLIST" >/dev/null
    launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
    launchctl bootstrap "$DOMAIN" "$PLIST"
    launchctl enable "$DOMAIN/$LABEL"
    echo "installed $LABEL (driver=$driver, policy=$policy)"
    echo "plist: $PLIST"
    ;;
  status)
    launchctl print "$DOMAIN/$LABEL" 2>/dev/null | sed -n '1,25p' || echo "$LABEL is not loaded"
    ;;
  logs)
    tail -n 40 "var/log/${LABEL}.out.log" "var/log/${LABEL}.err.log" 2>/dev/null || echo "no logs yet"
    ;;
  uninstall)
    launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
    rm -f "$PLIST"
    echo "removed $LABEL"
    ;;
  *) usage ;;
esac
