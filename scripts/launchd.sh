#!/bin/bash
# Install, inspect and remove the macOS launchd agent for votingd.
#
# macOS only, by design.  The kit does not ship systemd units or Windows
# services: scheduling is the part most tied to the platform, and shipping
# three half-tested variants would be worse than shipping one that works.
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
  echo "usage: scripts/launchd.sh <install|status|logs|uninstall> [--policy FILE] [--driver paper|live]" >&2
  exit 2
}

command="${1:-}"; shift || usage
policy=""
driver="paper"
while [ $# -gt 0 ]; do
  case "$1" in
    --policy) policy="${2:-}"; shift 2 ;;
    --driver) driver="${2:-}"; shift 2 ;;
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
        # The live driver submits real contest votes.  Make that a decision
        # someone types out, not a default anyone drifts into.
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
    sed -e "s#__LABEL__#${LABEL}#g" \
        -e "s#__PROJECT_ROOT__#${PROJECT_ROOT}#g" \
        -e "s#__POLICY__#${policy}#g" \
        -e "s#__DRIVER__#${driver}#g" \
        -e "s#__TIMEZONE__#${TIMEZONE}#g" \
        "$TEMPLATE" > "$PLIST"
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
