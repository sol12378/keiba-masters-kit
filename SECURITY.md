# Security

## Reporting

Report a vulnerability through GitHub's private advisory form on this
repository ("Security" → "Report a vulnerability"). Please do not open a public
issue for anything that affects credentials or submission integrity.

## What is in scope

* Anything that could cause a submission the operator did not arm, or a
  different submission from the one they armed.
* Anything that could leak `KEIBA_LOGIN_ID` or `KEIBA_PASSWORD` into a log,
  a state file, a journal entry or a plist.
* Anything that lets a plan bundle pass verification while differing from the
  hash the operator confirmed.

## What is not

* The live endpoint itself. It belongs to a third party; report problems there
  to them, not here.
* Losing virtual points. The strategy is expected to lose them — see
  `DISCLAIMER.md`.

## Design notes for reviewers

Submission requires five independent conditions, listed in
`docs/06-safety.md`. The paper driver is the default and reaches no network.
Credentials are read from the environment only and follow the selected driver:
the paper driver never reads them.
