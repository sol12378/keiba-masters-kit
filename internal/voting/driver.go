package voting

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Driver names accepted by NewDriver.
const (
	DriverPaper = "paper"
	DriverLive  = "live"
)

// DefaultOpeningBalance is the contest's starting bankroll in virtual points.
// The paper driver uses it so a local run starts from the same state a contest
// entry does.
const DefaultOpeningBalance = 1_000_000

// NewDriver builds the submission driver named by driverName.
//
//   - "paper" (default) runs entirely offline against a local state file.  It
//     needs no account and no network, and it is the only driver that works
//     after the contest has closed.
//   - "live" talks to the official contest endpoint.  It requires a contest
//     account and only works while the contest is accepting votes; outside the
//     contest period every submission is rejected upstream.
func NewDriver(driverName string, config RuntimeConfig) (API, error) {
	switch strings.ToLower(strings.TrimSpace(driverName)) {
	case "", DriverPaper:
		return NewPaperDriver(filepath.Join(config.StateDir, "paper_state.json"), DefaultOpeningBalance)
	case DriverLive:
		return NewMastersClient(OfficialAPIBaseURL, config.Policy)
	default:
		return nil, fmt.Errorf("unknown driver %q (want %q or %q)", driverName, DriverPaper, DriverLive)
	}
}

// CredentialsFor returns the credentials provider that matches a driver.
//
// The paper driver authenticates nothing, so requiring KEIBA_LOGIN_ID and
// KEIBA_PASSWORD from it would be theatre -- and worse than theatre under
// launchd, where an agent inherits neither and every race fails on a login
// that was never going to reach a network.  The live driver keeps reading them
// from the environment, which is the only place they belong.
func CredentialsFor(driverName string) CredentialsProvider {
	if strings.EqualFold(strings.TrimSpace(driverName), DriverLive) {
		return EnvironmentCredentials
	}
	return func() (string, string, error) { return "local", "local", nil }
}
