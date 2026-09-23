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

// DefaultOpeningBalance is the contest's starting balance in virtual points.
const DefaultOpeningBalance = 1_000_000

// NewDriver returns the submission driver.
//
//   - "paper" (default): offline, local state file.
//   - "live": the contest API. Needs an account and only works while the
//     contest is accepting votes.
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

// CredentialsFor returns the credentials provider for a driver. Only the live
// driver reads KEIBA_LOGIN_ID and KEIBA_PASSWORD; the paper driver uses fixed
// local values so it also works under launchd.
func CredentialsFor(driverName string) CredentialsProvider {
	if strings.EqualFold(strings.TrimSpace(driverName), DriverLive) {
		return EnvironmentCredentials
	}
	return func() (string, string, error) { return "local", "local", nil }
}
