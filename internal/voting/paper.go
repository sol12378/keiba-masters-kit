package voting

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// PaperDriver is a local, offline implementation of API.  It accepts votes,
// stores them on disk, and replays them through the same confirmation path the
// live driver uses, so the whole runtime (arm, precheck, submit, GET
// reconciliation, ledger) can be exercised without an account and without any
// network access.
//
// It is the default driver.  The live competition endpoint only accepts votes
// while the contest is running, so after the contest closes this driver is the
// only way to run the system end to end.
type PaperDriver struct {
	path    string
	mutex   sync.Mutex
	balance int
	votes   map[string]CheckedVote
}

const paperAccessToken = "paper-driver-local-token"

type paperState struct {
	Balance int                    `json:"balance"`
	Votes   map[string]CheckedVote `json:"votes"`
}

// NewPaperDriver opens (or creates) a paper-driver state file.  The file holds
// only accepted votes and the simulated balance; it never holds credentials.
func NewPaperDriver(path string, openingBalance int) (*PaperDriver, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("paper driver requires a state path")
	}
	driver := &PaperDriver{
		path:    path,
		balance: openingBalance,
		votes:   map[string]CheckedVote{},
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		var state paperState
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, err
		}
		if state.Votes != nil {
			driver.votes = state.Votes
		}
		driver.balance = state.Balance
	case errors.Is(err, os.ErrNotExist):
		if err := driver.persistLocked(); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return driver, nil
}

// Login accepts any non-empty credential pair.  The paper driver authenticates
// nothing: it exists so the caller can exercise the token lifecycle.
func (driver *PaperDriver) Login(_ context.Context, loginID, password string) (string, error) {
	if strings.TrimSpace(loginID) == "" || strings.TrimSpace(password) == "" {
		return "", errors.New("login credentials are required")
	}
	return paperAccessToken, nil
}

// Check reports the stored vote for raceID.  When no vote exists it returns the
// same empty-precheck error shape the live API produces, so the runtime's
// fail-closed precheck logic is exercised unchanged.
func (driver *PaperDriver) Check(_ context.Context, token, raceID string) ([]CheckedVote, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("access token is required")
	}
	if !raceIDPattern.MatchString(raceID) {
		return nil, errors.New("race_id must be exactly 12 ASCII letters/digits")
	}
	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	vote, ok := driver.votes[raceID]
	if !ok {
		return nil, &APIError{
			Operation:  "check_bet",
			Message:    "no vote recorded for this race",
			HTTPStatus: http.StatusOK,
			APIStatus:  "NG",
		}
	}
	votes := []CheckedVote{vote}
	SortVotes(votes)
	return votes, nil
}

// Submit records a vote.  A race may be voted exactly once, matching the live
// contest rule the runtime is built around.
func (driver *PaperDriver) Submit(_ context.Context, token string, payload RacePayload) (SubmitReceipt, error) {
	if strings.TrimSpace(token) == "" {
		return SubmitReceipt{}, errors.New("access token is required")
	}
	if err := ValidatePayload(payload); err != nil {
		return SubmitReceipt{}, err
	}
	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	if _, exists := driver.votes[payload.RaceID]; exists {
		return SubmitReceipt{}, &APIError{
			Operation:  "bet",
			Message:    "race already voted",
			HTTPStatus: http.StatusOK,
			APIStatus:  "NG",
			APIReason:  "duplicate race",
		}
	}
	stake := 0
	bets := make([]Bet, 0, len(payload.BetList))
	for _, bet := range payload.BetList {
		money, err := strconv.Atoi(bet.Money)
		if err != nil || money <= 0 {
			return SubmitReceipt{}, errors.New("bet money must be a positive integer")
		}
		stake += money
		bets = append(bets, bet)
	}
	if stake > driver.balance {
		return SubmitReceipt{}, &APIError{
			Operation:  "bet",
			Message:    "insufficient balance",
			HTTPStatus: http.StatusOK,
			APIStatus:  "NG",
			APIReason:  "insufficient balance",
		}
	}
	mark := make(map[string]int, len(payload.Mark))
	for horse, value := range payload.Mark {
		mark[horse] = value
	}
	// Record the bets exactly as submitted, in the submitted order.
	// Reconciliation compares the canonical JSON of the whole payload, and a
	// JSON array is ordered, so re-sorting here would make every read-back
	// disagree with the plan and halt the day on a CONFLICT the operator did
	// not cause.
	driver.votes[payload.RaceID] = CheckedVote{RaceID: payload.RaceID, Mark: mark, Bet: bets}
	driver.balance -= stake
	if err := driver.persistLocked(); err != nil {
		delete(driver.votes, payload.RaceID)
		driver.balance += stake
		return SubmitReceipt{}, err
	}
	return SubmitReceipt{
		SuccessCount:   len(bets),
		RemainingMoney: strconv.Itoa(driver.balance),
	}, nil
}

// Balance reports the simulated remaining points.
func (driver *PaperDriver) Balance() int {
	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	return driver.balance
}

func (driver *PaperDriver) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(driver.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(paperState{Balance: driver.balance, Votes: driver.votes}, "", "  ")
	if err != nil {
		return err
	}
	temporary := driver.path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, driver.path)
}
