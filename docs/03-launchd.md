# Running under launchd

macOS only. The Go and Python code is portable; this layer is not, and the kit
does not pretend otherwise.

## Why a supervisor at all

A race has one submission window, a few minutes wide, and it does not come
back. A process that dies inside that window with nothing to restart it simply
misses the race. `KeepAlive` turns a crash into a few seconds of downtime, and
the recovery rule in [02-voting-runtime.md](02-voting-runtime.md) makes the
restart safe: a race found mid-submission is resolved by reading the other
side, never by submitting again.

## Install

```bash
./scripts/launchd.sh install --policy var/policy_20260926.json --driver paper
./scripts/launchd.sh status
./scripts/launchd.sh logs
./scripts/launchd.sh uninstall
```

The script renders `launchd/templates/votingd.plist.template` into
`~/Library/LaunchAgents/`, substituting the absolute project root, the policy
path, the driver and the timezone. It runs `plutil -lint` before loading, then
`launchctl bootout` (ignoring failure) and `launchctl bootstrap`.

`--driver live` prompts for typed confirmation before installing. That is
deliberate friction.

## The agent cannot submit unless you say so

A launchd agent inherits almost nothing from your shell, so the date-scoped
variable the policy requires (`KEIBA_ENABLE_SUBMISSION`) is simply absent. An
agent installed and forgotten will start, serve status, and **refuse to arm a
day**. That is the intended default.

```bash
./scripts/launchd.sh install --policy var/policy_20260926.json --enable-submission
```

`--enable-submission` copies the policy's `required_environment` into the
plist and prints what it set. The value is a date, not a credential — it is
the operator saying "today", and the policy it is copied from is valid for
exactly that day. Re-installing for another day requires another policy and
another explicit flag.

Override the label prefix with `LABEL_PREFIX=...` if you run more than one.

## Credentials are still not in the plist

A plist is world-readable and gets swept into backups. The template sets `TZ`
and nothing else.

The live driver reads `KEIBA_LOGIN_ID` and `KEIBA_PASSWORD` from its
environment. On macOS the reasonable place for them is the Keychain:

```bash
security add-generic-password -U -a "$(id -un)" -s local.keiba.login-id  -w
security add-generic-password -U -a "$(id -un)" -s local.keiba.password  -w
```

Then start the daemon from a wrapper that exports them, rather than putting
them in `EnvironmentVariables`. The paper driver needs neither.

## Sleep will cost you races

This is the failure mode that matters most and launchd does not solve it. A
sleeping Mac runs nothing. `ProcessType Interactive` asks macOS not to throttle
the process under App Nap, but it does not keep the machine awake.

If you intend to be submitting on a schedule, hold a power assertion for the
window:

```bash
caffeinate -i -w $(pgrep -f 'bin/votingd')
```

or `caffeinate -s` while on mains power. Check `pmset -g assertions` to confirm
something is actually holding the machine up. Closing the lid on a laptop
sleeps it regardless.

## If the daemon crashloops immediately

Check `.err.log` and `.out.log` first. One cause is worth naming because the
underlying error is misleading: a Unix socket path is limited to 104 bytes on
macOS, and a project cloned into a deep directory pushes the control socket
past it. `bind()` answers `EINVAL`, which reads as "invalid argument" and
looks like a permissions problem.

The daemon now refuses with an explicit message naming the length and the
limit. The fix is to move the checkout somewhere shorter, or to point
`interfaces.control_socket` at a short absolute path in the policy.

## Logs

`var/log/<label>.out.log` and `.err.log`. The daemon writes structured JSON
lines, so:

```bash
tail -f var/log/local.keiba-masters-kit.votingd.out.log | jq .
```

launchd does not rotate these. Rotate them yourself, or truncate between days.

## Checking it is really running

`launchctl print gui/$(id -u)/<label>` shows the PID, the last exit status, and
the number of restarts. A climbing restart count with no PID means the daemon
is crashlooping — look at `.err.log`, not at `launchctl`.
