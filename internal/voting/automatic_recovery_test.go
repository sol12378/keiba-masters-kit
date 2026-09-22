package voting

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAutomaticReadRecoveryRefreshesAndSubmitsOnce(t *testing.T) {
	api := &fakeAPI{checkErrors: []error{&APIError{Operation: "check_bet", HTTPStatus: 401}}}
	service, store, clock, plan := newTestService(t, api)
	service.config.Policy.AutomaticRecovery = true
	clock.Set(plan.TargetSubmitTime)
	service.Tick(context.Background())
	r := store.Snapshot().Races[plan.PlanID]
	if r.LastErrorOperation != "check_bet" || r.LastHTTPStatus != 401 {
		t.Fatalf("recovery diagnosis metadata missing: %+v", r)
	}
	if store.Snapshot().Killed || r.State != StateArmed || r.POSTAttempts != 0 || api.submitCalls != 0 {
		t.Fatalf("unsafe recovery: %+v", r)
	}
	clock.Set(*r.NextActionAt)
	service.Tick(context.Background())
	r = store.Snapshot().Races[plan.PlanID]
	if r.State != StatePendingConfirmation || r.POSTAttempts != 1 || api.loginCalls != 2 || api.submitCalls != 1 {
		t.Fatalf("recovery did not refresh and submit once: %+v", r)
	}
}

func TestDynamicWireFailuresRestartAndExactlyOnePOST(t *testing.T) {
	service, store, clock, plan := newTestService(t, &fakeAPI{})
	service.config.Policy.AutomaticRecovery = true
	client, err := NewMastersClient("http://127.0.0.1", service.config.Policy)
	if err != nil {
		t.Fatal(err)
	}
	logins, gets, posts := 0, 0, 0
	client.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/ai2026_student/api/login" {
			logins++
			if logins <= 3 || logins == 5 {
				return jsonResponse(401, `{"status":"NG"}`), nil
			}
			return jsonResponse(200, `{"status":"OK","data":{"access_token":"fixture"}}`), nil
		}
		if request.Method == http.MethodPost {
			posts++
			// Server accepted the vote but its response was lost.
			return nil, io.ErrUnexpectedEOF
		}
		gets++
		if gets == 1 {
			return jsonResponse(200, `{"status":"OK","data":[]}`), nil
		}
		if gets == 2 {
			return jsonResponse(503, `{"status":"NG"}`), nil
		}
		body, _ := json.Marshal(map[string]any{"status": "OK", "data": []CheckedVote{{
			RaceID: plan.Payload.RaceID, Mark: plan.Payload.Mark, Bet: plan.Payload.BetList}}})
		return jsonResponse(200, string(body)), nil
	})
	service.api = client
	clock.Set(plan.TargetSubmitTime)
	for i := 0; i < 4; i++ {
		service.Tick(context.Background())
		r := store.Snapshot().Races[plan.PlanID]
		if i < 3 && (r.POSTAttempts != 0 || r.LastErrorOperation != "login" || r.LastHTTPStatus != 401) {
			t.Fatalf("login failure crossed POST boundary: %+v", r)
		}
		clock.Set(*r.NextActionAt)
	}
	if posts != 1 || store.Snapshot().Races[plan.PlanID].State != StateAmbiguous {
		t.Fatal("lost POST must be ambiguous")
	}
	// Re-open the actual disk journal, not just the in-memory service.
	reopened, err := OpenStore(service.config, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(service.config, reopened, client, service.credentials, service.logger, clock.Now)
	if err := restarted.Recover(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		clock.Set(*reopened.Snapshot().Races[plan.PlanID].NextActionAt)
		restarted.Tick(context.Background())
	}
	r := reopened.Snapshot().Races[plan.PlanID]
	if posts != 1 || r.POSTAttempts != 1 || r.State != StateConfirmed || reopened.Snapshot().Killed {
		t.Fatalf("recovery changed POST history: posts=%d record=%+v", posts, r)
	}
}

func TestPersistentFreezeMarkerBlocksPOSTBeforeControlKill(t *testing.T) {
	api := &fakeAPI{}
	service, store, clock, plan := newTestService(t, api)
	service.config.Policy.AutomaticRecovery = true
	if err := os.WriteFile(filepath.Join(service.config.StateDir, "FREEZE.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	clock.Set(plan.TargetSubmitTime)
	service.Tick(context.Background())
	if api.submitCalls != 0 || store.Snapshot().Races[plan.PlanID].POSTAttempts != 0 {
		t.Fatal("freeze marker must block POST even before journaled global kill")
	}
}

func TestAutomaticReadRecoveryExhaustionSkipsUnpostedRace(t *testing.T) {
	e := &APIError{Operation: "check_bet", HTTPStatus: 503}
	api := &fakeAPI{checkErrors: []error{e, e, e, e}}
	service, store, clock, plan := newTestService(t, api)
	service.config.Policy.AutomaticRecovery = true
	clock.Set(plan.TargetSubmitTime)
	for i := 0; i < 4; i++ {
		service.Tick(context.Background())
		clock.Set(clock.Now().Add(10 * time.Second))
	}
	r := store.Snapshot().Races[plan.PlanID]
	if store.Snapshot().Killed || r.State != StateRejected || r.POSTAttempts != 0 || api.submitCalls != 0 || r.PrecheckAttempts != 4 {
		t.Fatalf("unposted race not safely retired: %+v", r)
	}
}

func TestAutomaticReconciliationBlocksNewRaceUntilExactMatch(t *testing.T) {
	api := &fakeAPI{submitError: &APIError{Operation: "submit_bet", Ambiguous: true}}
	service, store, clock, plan := newTestService(t, api)
	service.config.Policy.AutomaticRecovery = true
	service.config.Policy.Submission.MaxRaces = 2
	service.config.Policy.Submission.MaxArms = 2
	service.config.Policy.Submission.MaxPOSTRequests = 2
	service.config.Policy.Submission.MaxTotalStake = 200
	service.config.Policy.Submission.MaxDailyTotalStake = 200
	second := plan
	second.PlanID += "-second"
	second.Payload.RaceID = "202604020102"
	second.PayloadSHA256, _ = PayloadSHA256(second.Payload)
	data, _ := json.Marshal(second)
	if _, err := service.ImportPlan(data); err != nil {
		t.Fatal(err)
	}
	clock.Set(plan.TargetSubmitTime)
	service.Tick(context.Background())
	if store.Snapshot().Killed || api.submitCalls != 1 {
		t.Fatal("POST uncertainty should pause, not reset the journal")
	}
	if _, err := service.Arm(second.PlanID, second.PayloadSHA256); err == nil {
		t.Fatal("armed before exact reconciliation")
	}
	api.checkErrors = []error{&APIError{Operation: "check_bet", HTTPStatus: 503}}
	clock.Set(*store.Snapshot().Races[plan.PlanID].NextActionAt)
	service.Tick(context.Background())
	if store.Snapshot().Killed || api.submitCalls != 1 {
		t.Fatal("transient GET must remain read-only")
	}
	api.checkResponses = [][]CheckedVote{{{RaceID: plan.Payload.RaceID, Mark: plan.Payload.Mark, Bet: plan.Payload.BetList}}}
	clock.Set(*store.Snapshot().Races[plan.PlanID].NextActionAt)
	service.Tick(context.Background())
	if store.Snapshot().Races[plan.PlanID].State != StateConfirmed || api.submitCalls != 1 {
		t.Fatal("exact match did not resolve without POST retry")
	}
	if _, err := service.Arm(second.PlanID, second.PayloadSHA256); err != nil {
		t.Fatalf("next race did not resume: %v", err)
	}
}

func TestDynamicBudgetIsOnlyAuthorizedForOneDateAndPolicy(t *testing.T) {
	p := fullDayPolicy(t)
	p.PolicyID = "COMPETITION-2026-DYNAMIC-20260920"
	p.ConnectionDate = "2026-09-20"
	p.AutomaticRecovery = true
	p.Submission.MaxStakePerRace = 1268500
	p.Submission.MaxTotalStake = 1268500
	p.Submission.MaxDailyTotalStake = 1268500
	if err := ValidatePolicy(p); err != nil {
		t.Fatal(err)
	}
	p.ConnectionDate = "2026-09-21"
	if err := ValidatePolicy(p); err == nil {
		t.Fatal("full-bank policy escaped date boundary")
	}
	p.ConnectionDate = "2026-09-20"
	p.Submission.MaxDailyTotalStake += 100
	if err := ValidatePolicy(p); err == nil {
		t.Fatal("opening balance ceiling was exceeded")
	}
}

func TestTargetPauseCanCancelOnlyBeforePOST(t *testing.T) {
	api := &fakeAPI{}
	service, store, clock, plan := newTestService(t, api)
	service.config.Policy.AutomaticRecovery = true
	if err := service.CancelUnposted(plan.PlanID); err != nil {
		t.Fatal(err)
	}
	clock.Set(plan.TargetSubmitTime)
	service.Tick(context.Background())
	if api.submitCalls != 0 || store.Snapshot().Killed || store.Snapshot().Races[plan.PlanID].State != StateKilled {
		t.Fatal("target pause must cancel the arm without clearing or killing the whole journal")
	}
	service2, store2, clock2, plan2 := newTestService(t, &fakeAPI{})
	service2.config.Policy.AutomaticRecovery = true
	clock2.Set(plan2.TargetSubmitTime)
	service2.Tick(context.Background())
	if err := service2.CancelUnposted(plan2.PlanID); err == nil {
		t.Fatal("cancelled a posted race")
	}
	if store2.Snapshot().Races[plan2.PlanID].POSTAttempts != 1 {
		t.Fatal("POST checkpoint changed")
	}
}
