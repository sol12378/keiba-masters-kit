package voting

import (
	"context"
	"path/filepath"
	"testing"
)

func paperPayload() RacePayload {
	return RacePayload{
		RaceID:  "202606040411",
		Mark:    map[string]int{"1": 1, "2": 2, "3": 3, "4": 4},
		BetList: []Bet{{BetID: "b1_c0_1", Money: "100"}},
	}
}

func newTestPaperDriver(t *testing.T) *PaperDriver {
	t.Helper()
	driver, err := NewPaperDriver(filepath.Join(t.TempDir(), "paper_state.json"), DefaultOpeningBalance)
	if err != nil {
		t.Fatalf("NewPaperDriver: %v", err)
	}
	return driver
}

func TestPaperDriverRejectsEmptyCredentials(t *testing.T) {
	driver := newTestPaperDriver(t)
	if _, err := driver.Login(context.Background(), "", ""); err == nil {
		t.Fatal("expected empty credentials to be rejected")
	}
}

func TestPaperDriverCheckBeforeSubmitIsRecognizedAsEmptyPrecheck(t *testing.T) {
	driver := newTestPaperDriver(t)
	token, err := driver.Login(context.Background(), "local", "local")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	_, err = driver.Check(context.Background(), token, paperPayload().RaceID)
	if err == nil {
		t.Fatal("expected an error before any vote exists")
	}
	if !IsEmptyPrecheckResponse(err) {
		t.Fatalf("empty precheck was not recognized: %v", err)
	}
}

func TestPaperDriverSubmitIsVisibleToCheckAndDebitsBalance(t *testing.T) {
	driver := newTestPaperDriver(t)
	token, _ := driver.Login(context.Background(), "local", "local")
	payload := paperPayload()
	receipt, err := driver.Submit(context.Background(), token, payload)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if receipt.SuccessCount != 1 {
		t.Fatalf("success_count = %d, want 1", receipt.SuccessCount)
	}
	if want := DefaultOpeningBalance - 100; driver.Balance() != want {
		t.Fatalf("balance = %d, want %d", driver.Balance(), want)
	}
	votes, err := driver.Check(context.Background(), token, payload.RaceID)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(votes) != 1 || votes[0].RaceID != payload.RaceID || len(votes[0].Bet) != 1 {
		t.Fatalf("Check returned %+v", votes)
	}
	if votes[0].Bet[0].Money != "100" {
		t.Fatalf("money = %q, want \"100\"", votes[0].Bet[0].Money)
	}
}

func TestPaperDriverRejectsSecondVoteOnSameRace(t *testing.T) {
	driver := newTestPaperDriver(t)
	token, _ := driver.Login(context.Background(), "local", "local")
	if _, err := driver.Submit(context.Background(), token, paperPayload()); err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	if _, err := driver.Submit(context.Background(), token, paperPayload()); err == nil {
		t.Fatal("expected the second vote on the same race to be rejected")
	}
}

func TestPaperDriverStateSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paper_state.json")
	first, err := NewPaperDriver(path, DefaultOpeningBalance)
	if err != nil {
		t.Fatalf("NewPaperDriver: %v", err)
	}
	token, _ := first.Login(context.Background(), "local", "local")
	if _, err := first.Submit(context.Background(), token, paperPayload()); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	second, err := NewPaperDriver(path, DefaultOpeningBalance)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if second.Balance() != first.Balance() {
		t.Fatalf("reopened balance = %d, want %d", second.Balance(), first.Balance())
	}
	votes, err := second.Check(context.Background(), token, paperPayload().RaceID)
	if err != nil {
		t.Fatalf("Check after reopen: %v", err)
	}
	if len(votes) != 1 {
		t.Fatalf("reopened driver lost the vote: %+v", votes)
	}
}

func TestPaperDriverRejectsStakeAboveBalance(t *testing.T) {
	driver, err := NewPaperDriver(filepath.Join(t.TempDir(), "paper_state.json"), 100)
	if err != nil {
		t.Fatalf("NewPaperDriver: %v", err)
	}
	token, _ := driver.Login(context.Background(), "local", "local")
	payload := paperPayload()
	payload.BetList = []Bet{{BetID: "b1_c0_1", Money: "200"}}
	if _, err := driver.Submit(context.Background(), token, payload); err == nil {
		t.Fatal("expected a stake above the balance to be rejected")
	}
	if driver.Balance() != 100 {
		t.Fatalf("balance changed on a rejected submit: %d", driver.Balance())
	}
}

// The runtime confirms a submission by comparing the canonical JSON of the
// whole payload, and a JSON array is ordered.  A driver that tidies the bet
// list turns every race into a CONFLICT and halts the day, which is what
// happened the first time this ran under launchd.
func TestPaperDriverReadsBackTheBetListInTheSubmittedOrder(t *testing.T) {
	driver := newTestPaperDriver(t)
	token, _ := driver.Login(context.Background(), "local", "local")
	payload := paperPayload()
	payload.BetList = []Bet{
		{BetID: "b8_c0_2_8_5", Money: "10000"},
		{BetID: "b8_c0_2_8_3", Money: "10000"},
	}
	if _, err := driver.Submit(context.Background(), token, payload); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	votes, err := driver.Check(context.Background(), token, payload.RaceID)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !CheckedVoteMatches(payload, votes) {
		t.Fatalf("read-back does not reconcile with the payload:\n  sent %+v\n  got  %+v", payload.BetList, votes[0].Bet)
	}
}
