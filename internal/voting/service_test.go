package voting

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) Set(value time.Time) {
	clock.mu.Lock()
	clock.now = value
	clock.mu.Unlock()
}

type fakeAPI struct {
	mu             sync.Mutex
	checkResponses [][]CheckedVote
	checkErrors    []error
	submitError    error
	loginCalls     int
	checkCalls     int
	submitCalls    int
}

func (api *fakeAPI) Login(context.Context, string, string) (string, error) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.loginCalls++
	return "token", nil
}

func (api *fakeAPI) Check(context.Context, string, string) ([]CheckedVote, error) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.checkCalls++
	if len(api.checkErrors) > 0 {
		err := api.checkErrors[0]
		api.checkErrors = api.checkErrors[1:]
		return nil, err
	}
	if len(api.checkResponses) == 0 {
		return nil, nil
	}
	result := api.checkResponses[0]
	api.checkResponses = api.checkResponses[1:]
	return result, nil
}

func (api *fakeAPI) Submit(context.Context, string, RacePayload) (SubmitReceipt, error) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.submitCalls++
	if api.submitError != nil {
		return SubmitReceipt{}, api.submitError
	}
	return SubmitReceipt{SuccessCount: 1, RemainingMoney: "999900"}, nil
}

func testPolicy(t *testing.T) Policy {
	t.Helper()
	root, err := FindProjectRoot()
	if err != nil {
		t.Fatal(err)
	}
	config, err := LoadRuntimeConfig(root, "configs/voting_policy.json")
	if err != nil {
		t.Fatal(err)
	}
	return config.Policy
}

func fullDayPolicy(t *testing.T) Policy {
	t.Helper()
	root, err := FindProjectRoot()
	if err != nil {
		t.Fatal(err)
	}
	config, err := LoadRuntimeConfig(root, "configs/voting_policy_fullday.json")
	if err != nil {
		t.Fatal(err)
	}
	return config.Policy
}

func testRuntime(t *testing.T, clock *fakeClock) RuntimeConfig {
	t.Helper()
	policy := testPolicy(t)
	policy.Timing.VerificationInitialWait = 1
	policy.Timing.VerificationInterval = 1
	temporary := t.TempDir()
	return RuntimeConfig{
		Policy:     policy,
		Root:       temporary,
		StateDir:   filepath.Join(temporary, "state"),
		Journal:    filepath.Join(temporary, "state", "events.jsonl"),
		Snapshot:   filepath.Join(temporary, "state", "snapshot.json"),
		Socket:     filepath.Join(temporary, "state", "votingd.sock"),
		KillSwitch: filepath.Join(temporary, "STOP"),
	}
}

func newTestService(t *testing.T, api *fakeAPI) (*Service, *Store, *fakeClock, PlanEnvelope) {
	t.Helper()
	location, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Date(2026, 8, 15, 11, 54, 0, 0, location)}
	config := testRuntime(t, clock)
	store, err := OpenStore(config, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(
		config,
		store,
		api,
		func() (string, string, error) { return "student", "password", nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock.Now,
	)
	t.Setenv("KEIBA_ENABLE_TEST_SUBMISSION", "2026-08-15")
	plan := testPlan(t, config.Policy, clock.Now())
	data, _ := json.Marshal(plan)
	if _, err := service.ImportPlan(data); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Arm(plan.PlanID, plan.PayloadSHA256); err != nil {
		t.Fatal(err)
	}
	return service, store, clock, plan
}

func TestServiceSubmitsOnceAndConfirmsByGET(t *testing.T) {
	payload := minimalPayload()
	api := &fakeAPI{checkResponses: [][]CheckedVote{
		{},
		{{RaceID: payload.RaceID, Mark: payload.Mark, Bet: payload.BetList}},
	}}
	service, store, clock, plan := newTestService(t, api)
	clock.Set(plan.TargetSubmitTime)
	service.Tick(context.Background())
	record := store.Snapshot().Races[plan.PlanID]
	if record.State != StatePendingConfirmation || record.POSTAttempts != 1 || api.submitCalls != 1 {
		t.Fatalf("after submit record=%+v submit_calls=%d", record, api.submitCalls)
	}
	clock.Set(record.NextActionAt.Add(0))
	service.Tick(context.Background())
	record = store.Snapshot().Races[plan.PlanID]
	if record.State != StateConfirmed || api.submitCalls != 1 {
		t.Fatalf("after reconcile record=%+v submit_calls=%d", record, api.submitCalls)
	}
}

func TestServiceTreatsNarrowHTTP200NGAsEmptyPrecheck(t *testing.T) {
	api := &fakeAPI{checkErrors: []error{&APIError{
		Operation: "check_bet", HTTPStatus: 200, APIStatus: "NG",
	}}}
	service, store, clock, plan := newTestService(t, api)
	clock.Set(plan.TargetSubmitTime)
	service.Tick(context.Background())
	record := store.Snapshot().Races[plan.PlanID]
	if record.State != StatePendingConfirmation || record.POSTAttempts != 1 || api.submitCalls != 1 {
		t.Fatalf("record=%+v submit_calls=%d", record, api.submitCalls)
	}
}

func TestAmbiguousPOSTNeverRetriesAndCanConfirmByGET(t *testing.T) {
	payload := minimalPayload()
	api := &fakeAPI{
		checkResponses: [][]CheckedVote{
			{},
			{{RaceID: payload.RaceID, Mark: payload.Mark, Bet: payload.BetList}},
		},
		submitError: &APIError{Operation: "submit_bet", Message: "transport failure", Ambiguous: true},
	}
	service, store, clock, plan := newTestService(t, api)
	clock.Set(plan.TargetSubmitTime)
	service.Tick(context.Background())
	record := store.Snapshot().Races[plan.PlanID]
	if record.State != StateAmbiguous || api.submitCalls != 1 {
		t.Fatalf("record=%+v submit_calls=%d", record, api.submitCalls)
	}
	clock.Set(*record.NextActionAt)
	service.Tick(context.Background())
	record = store.Snapshot().Races[plan.PlanID]
	if record.State != StateConfirmed || record.POSTAttempts != 1 || api.submitCalls != 1 {
		t.Fatalf("record=%+v submit_calls=%d", record, api.submitCalls)
	}
}

func TestRejectedPOSTPersistsBoundedAPIReason(t *testing.T) {
	api := &fakeAPI{
		checkResponses: [][]CheckedVote{{}},
		submitError: &APIError{
			Operation: "submit_bet", HTTPStatus: 200, APIStatus: "NG",
			APIReason: strings.Repeat("受付対象外 ", 40),
		},
	}
	service, store, clock, plan := newTestService(t, api)
	clock.Set(plan.TargetSubmitTime)
	service.Tick(context.Background())
	record := store.Snapshot().Races[plan.PlanID]
	if record.State != StateRejected || record.LastHTTPStatus != 200 || record.LastAPIStatus != "NG" {
		t.Fatalf("record=%+v", record)
	}
	if record.LastAPIReason == "" || len(record.LastAPIReason) > 160 {
		t.Fatalf("unsafe or missing API reason: %q", record.LastAPIReason)
	}
}

func TestKillAfterAmbiguousPOSTStillAllowsGETOnlyReconciliation(t *testing.T) {
	payload := minimalPayload()
	api := &fakeAPI{
		checkResponses: [][]CheckedVote{{}, {{RaceID: payload.RaceID, Mark: payload.Mark, Bet: payload.BetList}}},
		submitError:    &APIError{Operation: "submit_bet", Message: "network lost", Ambiguous: true},
	}
	service, store, clock, plan := newTestService(t, api)
	clock.Set(plan.TargetSubmitTime)
	service.Tick(context.Background())
	if err := service.Kill("operator stop after uncertain POST"); err != nil {
		t.Fatal(err)
	}
	record := store.Snapshot().Races[plan.PlanID]
	clock.Set(*record.NextActionAt)
	service.Tick(context.Background())
	record = store.Snapshot().Races[plan.PlanID]
	if record.State != StateConfirmed || api.submitCalls != 1 {
		t.Fatalf("record=%+v submit_calls=%d", record, api.submitCalls)
	}
}

func TestDayRolloverCheckpointMakesNoNetworkCall(t *testing.T) {
	api := &fakeAPI{}
	service, store, clock, plan := newTestService(t, api)
	if err := store.UpdateRecord(plan.PlanID, StatePosting, "test_midnight_checkpoint", "", func(record *RaceRecord) error {
		record.POSTAttempts = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	location, _ := time.LoadLocation("Asia/Tokyo")
	clock.Set(time.Date(2026, 8, 16, 0, 1, 0, 0, location))
	if err := service.Recover(); err != nil {
		t.Fatal(err)
	}
	service.Tick(context.Background())
	record := store.Snapshot().Races[plan.PlanID]
	if record.State != StateAmbiguous || api.loginCalls != 0 || api.checkCalls != 0 || api.submitCalls != 0 {
		t.Fatalf("record=%+v calls login=%d check=%d submit=%d", record, api.loginCalls, api.checkCalls, api.submitCalls)
	}
}

func TestExistingDifferentVoteBlocksPOST(t *testing.T) {
	payload := minimalPayload()
	different := payload.BetList
	different[0].Money = "200"
	api := &fakeAPI{checkResponses: [][]CheckedVote{{{
		RaceID: payload.RaceID, Mark: payload.Mark, Bet: different,
	}}}}
	service, store, clock, plan := newTestService(t, api)
	clock.Set(plan.TargetSubmitTime)
	service.Tick(context.Background())
	record := store.Snapshot().Races[plan.PlanID]
	if record.State != StateConflict || api.submitCalls != 0 {
		t.Fatalf("record=%+v submit_calls=%d", record, api.submitCalls)
	}
}

func TestRestartFromPostingIsGETOnly(t *testing.T) {
	api := &fakeAPI{}
	service, store, clock, plan := newTestService(t, api)
	if err := store.UpdateRecord(plan.PlanID, StatePosting, "test_crash_boundary", "", func(record *RaceRecord) error {
		record.POSTAttempts = 1
		record.NextActionAt = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	api.checkResponses = [][]CheckedVote{{{
		RaceID: plan.Payload.RaceID, Mark: plan.Payload.Mark, Bet: plan.Payload.BetList,
	}}}
	if err := service.Recover(); err != nil {
		t.Fatal(err)
	}
	clock.Set(*store.Snapshot().Races[plan.PlanID].NextActionAt)
	service.Tick(context.Background())
	record := store.Snapshot().Races[plan.PlanID]
	if record.State != StateConfirmed || api.submitCalls != 0 || record.POSTAttempts != 1 {
		t.Fatalf("record=%+v submit_calls=%d", record, api.submitCalls)
	}
}

func TestRestartBeforePOSTRepeatsSafePrecheck(t *testing.T) {
	api := &fakeAPI{checkResponses: [][]CheckedVote{{}}}
	service, store, clock, plan := newTestService(t, api)
	if err := store.UpdateRecord(plan.PlanID, StatePrecheckedEmpty, "test_crash_before_posting", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := service.Recover(); err != nil {
		t.Fatal(err)
	}
	recovered := store.Snapshot().Races[plan.PlanID]
	if recovered.State != StateArmed || recovered.POSTAttempts != 0 || recovered.NextActionAt == nil {
		t.Fatalf("recovered=%+v", recovered)
	}
	clock.Set(*recovered.NextActionAt)
	service.Tick(context.Background())
	result := store.Snapshot().Races[plan.PlanID]
	if result.POSTAttempts != 1 || api.submitCalls != 1 {
		t.Fatalf("result=%+v submit_calls=%d", result, api.submitCalls)
	}
}

func TestConcurrentTicksCannotDuplicatePOST(t *testing.T) {
	api := &fakeAPI{checkResponses: [][]CheckedVote{{}}}
	service, store, clock, plan := newTestService(t, api)
	clock.Set(plan.TargetSubmitTime)
	var group sync.WaitGroup
	for range 100 {
		group.Add(1)
		go func() {
			defer group.Done()
			service.Tick(context.Background())
		}()
	}
	group.Wait()
	record := store.Snapshot().Races[plan.PlanID]
	if record.POSTAttempts != 1 || api.submitCalls != 1 {
		t.Fatalf("record=%+v submit_calls=%d", record, api.submitCalls)
	}
}

func TestHardCutoffAndKillSwitchBlockPOST(t *testing.T) {
	t.Run("hard cutoff", func(t *testing.T) {
		api := &fakeAPI{}
		service, store, clock, plan := newTestService(t, api)
		clock.Set(plan.HardSubmitDeadline)
		service.Tick(context.Background())
		record := store.Snapshot().Races[plan.PlanID]
		if record.State != StateExpired || api.submitCalls != 0 {
			t.Fatalf("record=%+v submit_calls=%d", record, api.submitCalls)
		}
	})
	t.Run("kill switch file", func(t *testing.T) {
		api := &fakeAPI{}
		service, store, clock, plan := newTestService(t, api)
		if err := os.WriteFile(service.config.KillSwitch, []byte("stop\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		clock.Set(plan.TargetSubmitTime)
		service.Tick(context.Background())
		record := store.Snapshot().Races[plan.PlanID]
		if record.State != StateKilled || api.submitCalls != 0 {
			t.Fatalf("record=%+v submit_calls=%d", record, api.submitCalls)
		}
	})
}

func TestAuthCheckOutsideConnectionDateMakesNoAPICall(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Tokyo")
	clock := &fakeClock{now: time.Date(2026, 8, 14, 12, 0, 0, 0, location)}
	config := testRuntime(t, clock)
	store, err := OpenStore(config, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{}
	service := NewService(config, store, api, func() (string, string, error) { return "x", "y", nil }, slog.Default(), clock.Now)
	if err := service.AuthCheck(context.Background()); err == nil {
		t.Fatal("expected connection-date guard")
	}
	if api.loginCalls != 0 {
		t.Fatal("API must not be called outside the connection date")
	}
}

func TestStoreReloadRetainsPOSTAttempt(t *testing.T) {
	api := &fakeAPI{}
	service, store, clock, plan := newTestService(t, api)
	if err := store.UpdateRecord(plan.PlanID, StatePosting, "persist_before_post", "", func(record *RaceRecord) error {
		record.POSTAttempts++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := OpenStore(service.config, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	record := reloaded.Snapshot().Races[plan.PlanID]
	if record.State != StatePosting || record.POSTAttempts != 1 {
		t.Fatalf("reloaded record=%+v", record)
	}
}

func TestMissingJournalWithNonemptySnapshotFailsClosed(t *testing.T) {
	api := &fakeAPI{}
	service, store, clock, plan := newTestService(t, api)
	if store.Snapshot().Sequence == 0 {
		t.Fatal("expected persisted state")
	}
	if err := os.Remove(service.config.Journal); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(service.config, clock.Now); err == nil {
		t.Fatalf("expected missing journal failure for plan %s", plan.PlanID)
	}
}

func TestJournalEventIdentityTamperFailsClosed(t *testing.T) {
	api := &fakeAPI{}
	service, _, clock, plan := newTestService(t, api)
	data, err := os.ReadFile(service.config.Journal)
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`"event_id":"evt-00000000000000000001"`)
	tampered := []byte(`"event_id":"evt-00000000000000000002"`)
	if !bytes.Contains(data, original) {
		t.Fatalf("journal did not contain expected first event for %s", plan.PlanID)
	}
	data = bytes.Replace(data, original, tampered, 1)
	if err := os.WriteFile(service.config.Journal, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(service.config, clock.Now); err == nil || !strings.Contains(err.Error(), "event_id") {
		t.Fatalf("expected event identity failure, got %v", err)
	}
}

func TestJournalRecordPlanIDTamperFailsClosed(t *testing.T) {
	api := &fakeAPI{}
	service, _, clock, plan := newTestService(t, api)
	data, err := os.ReadFile(service.config.Journal)
	if err != nil {
		t.Fatal(err)
	}
	needle := []byte(`"plan_id":"` + plan.PlanID + `"`)
	replacement := []byte(`"plan_id":"tampered-plan-id"`)
	if !bytes.Contains(data, needle) {
		t.Fatalf("journal did not contain plan identity for %s", plan.PlanID)
	}
	data = bytes.Replace(data, needle, replacement, 1)
	if err := os.WriteFile(service.config.Journal, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(service.config, clock.Now); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("expected plan identity failure, got %v", err)
	}
}

func TestJournalRecordPlanHashTamperFailsClosed(t *testing.T) {
	api := &fakeAPI{}
	service, _, clock, plan := newTestService(t, api)
	record, ok := service.store.Snapshot().Races[plan.PlanID]
	if !ok {
		t.Fatal("plan record missing")
	}
	data, err := os.ReadFile(service.config.Journal)
	if err != nil {
		t.Fatal(err)
	}
	needle := []byte(`"plan_sha256":"` + record.PlanSHA256 + `"`)
	replacement := []byte(`"plan_sha256":"` + strings.Repeat("0", 64) + `"`)
	if !bytes.Contains(data, needle) {
		t.Fatal("journal did not contain plan hash")
	}
	data = bytes.Replace(data, needle, replacement, 1)
	if err := os.WriteFile(service.config.Journal, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(service.config, clock.Now); err == nil || !strings.Contains(err.Error(), "plan hash") {
		t.Fatalf("expected plan hash failure, got %v", err)
	}
}

func TestSnapshotAheadOfJournalFailsClosed(t *testing.T) {
	api := &fakeAPI{}
	service, store, clock, _ := newTestService(t, api)
	snapshot := store.Snapshot()
	snapshot.Sequence++
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(service.config.Snapshot, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(service.config, clock.Now); err == nil || !strings.Contains(err.Error(), "ahead") {
		t.Fatalf("expected snapshot-ahead failure, got %v", err)
	}
}

func TestArmRejectsWrongHash(t *testing.T) {
	api := &fakeAPI{}
	location, _ := time.LoadLocation("Asia/Tokyo")
	clock := &fakeClock{now: time.Date(2026, 8, 15, 11, 54, 0, 0, location)}
	config := testRuntime(t, clock)
	store, _ := OpenStore(config, clock.Now)
	service := NewService(config, store, api, func() (string, string, error) { return "x", "y", nil }, slog.Default(), clock.Now)
	plan := testPlan(t, config.Policy, clock.Now())
	data, _ := json.Marshal(plan)
	if _, err := service.ImportPlan(data); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Arm(plan.PlanID, strings.Repeat("0", 64)); err == nil {
		t.Fatal("expected hash mismatch")
	}
}

func TestRealStoreSnapshotPersistsPlanEnvelopeHash(t *testing.T) {
	api := &fakeAPI{}
	service, store, clock, plan := newTestService(t, api)
	record := store.Snapshot().Races[plan.PlanID]
	expected, err := PlanEnvelopeSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	if record.PlanSHA256 != expected {
		t.Fatalf("store plan_sha256=%q expected %q", record.PlanSHA256, expected)
	}
	raw, err := os.ReadFile(service.config.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Snapshot
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Races[plan.PlanID].PlanSHA256 != expected {
		t.Fatalf("snapshot plan_sha256=%q expected %q", persisted.Races[plan.PlanID].PlanSHA256, expected)
	}
	reopened, err := OpenStore(service.config, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Snapshot().Races[plan.PlanID].PlanSHA256 != expected {
		t.Fatal("reopened votingd store lost plan envelope hash")
	}
}

func TestImportRejectsDuplicateRaceEvenWhenPlanIDDiffers(t *testing.T) {
	api := &fakeAPI{}
	service, store, _, plan := newTestService(t, api)
	duplicate := plan
	duplicate.PlanID = "duplicate-plan-id"
	data, err := json.Marshal(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportPlan(data); err == nil || !strings.Contains(err.Error(), "race_id") {
		t.Fatalf("expected duplicate race rejection, got %v", err)
	}
	if len(store.Snapshot().Races) != 1 {
		t.Fatal("duplicate import must not persist another race record")
	}
}

func TestFullDayBundleSubmitsEveryRaceOnce(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Tokyo")
	clock := &fakeClock{now: time.Date(2026, 8, 15, 8, 30, 0, 0, location)}
	policy := fullDayPolicy(t)
	policy.Timing.VerificationInitialWait = 1
	policy.Timing.VerificationInterval = 1
	temporary := t.TempDir()
	config := RuntimeConfig{
		Policy:     policy,
		Root:       temporary,
		StateDir:   filepath.Join(temporary, "state"),
		Journal:    filepath.Join(temporary, "state", "events.jsonl"),
		Snapshot:   filepath.Join(temporary, "state", "snapshot.json"),
		Socket:     filepath.Join(temporary, "state", "votingd.sock"),
		KillSwitch: filepath.Join(temporary, "STOP"),
	}
	payload1 := minimalPayload()
	payload1.BetList[0].Money = "20000"
	payload2 := RacePayload{
		RaceID:  "202604020101",
		Mark:    map[string]int{"9": 1},
		BetList: []Bet{{BetID: "b3_c0_1_7", Money: "100"}},
	}
	plan1 := fullDayPlan(t, policy, clock.Now(), payload1, time.Date(2026, 8, 15, 10, 0, 0, 0, location))
	plan2 := fullDayPlan(t, policy, clock.Now(), payload2, time.Date(2026, 8, 15, 10, 30, 0, 0, location))
	bundle := fullDayBundle(t, policy, clock.Now(), []PlanEnvelope{plan1, plan2})
	api := &fakeAPI{checkResponses: [][]CheckedVote{
		{},
		{{RaceID: payload1.RaceID, Mark: payload1.Mark, Bet: payload1.BetList}},
		{},
		{{RaceID: payload2.RaceID, Mark: payload2.Mark, Bet: payload2.BetList}},
	}}
	store, err := OpenStore(config, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(config, store, api, func() (string, string, error) { return "student", "password", nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), clock.Now)
	t.Setenv("KEIBA_ENABLE_FULL_DAY_SUBMISSION", "FULL-DAY-2026-08-15")
	if records, err := service.ImportBundle(bundle); err != nil || len(records) != 2 {
		t.Fatalf("import records=%d err=%v", len(records), err)
	}
	if records, err := service.ArmBundle(bundle, bundle.BundleSHA256); err != nil || len(records) != 2 {
		t.Fatalf("arm records=%d err=%v", len(records), err)
	}

	clock.Set(plan1.TargetSubmitTime)
	service.Tick(context.Background())
	record1 := store.Snapshot().Races[plan1.PlanID]
	if record1.State != StatePendingConfirmation || api.submitCalls != 1 {
		t.Fatalf("first submit record=%+v calls=%d", record1, api.submitCalls)
	}
	clock.Set(*record1.NextActionAt)
	service.Tick(context.Background())
	if store.Snapshot().Races[plan1.PlanID].State != StateConfirmed {
		t.Fatal("first race was not confirmed")
	}

	clock.Set(plan2.TargetSubmitTime)
	service.Tick(context.Background())
	record2 := store.Snapshot().Races[plan2.PlanID]
	if record2.State != StatePendingConfirmation || api.submitCalls != 2 {
		t.Fatalf("second submit record=%+v calls=%d", record2, api.submitCalls)
	}
	clock.Set(*record2.NextActionAt)
	service.Tick(context.Background())
	if store.Snapshot().Races[plan2.PlanID].State != StateConfirmed || api.submitCalls != 2 {
		t.Fatal("second race was not confirmed exactly once")
	}
	if api.loginCalls != 2 {
		t.Fatalf("expected token reuse within each 4-minute window, login_calls=%d", api.loginCalls)
	}
}

func TestFullDayAmbiguousPOSTHaltsFutureRacesButReconcilesPostedRace(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Tokyo")
	clock := &fakeClock{now: time.Date(2026, 8, 15, 8, 30, 0, 0, location)}
	policy := fullDayPolicy(t)
	policy.Timing.VerificationInitialWait = 1
	temporary := t.TempDir()
	config := RuntimeConfig{
		Policy:     policy,
		Root:       temporary,
		StateDir:   filepath.Join(temporary, "state"),
		Journal:    filepath.Join(temporary, "state", "events.jsonl"),
		Snapshot:   filepath.Join(temporary, "state", "snapshot.json"),
		Socket:     filepath.Join(temporary, "state", "votingd.sock"),
		KillSwitch: filepath.Join(temporary, "STOP"),
	}
	payload1 := minimalPayload()
	payload2 := RacePayload{
		RaceID:  "202604020101",
		Mark:    map[string]int{"9": 1},
		BetList: []Bet{{BetID: "b3_c0_1_7", Money: "100"}},
	}
	plan1 := fullDayPlan(t, policy, clock.Now(), payload1, time.Date(2026, 8, 15, 10, 0, 0, 0, location))
	plan2 := fullDayPlan(t, policy, clock.Now(), payload2, time.Date(2026, 8, 15, 10, 30, 0, 0, location))
	bundle := fullDayBundle(t, policy, clock.Now(), []PlanEnvelope{plan1, plan2})
	api := &fakeAPI{
		checkResponses: [][]CheckedVote{
			{},
			{{RaceID: payload1.RaceID, Mark: payload1.Mark, Bet: payload1.BetList}},
		},
		submitError: &APIError{Operation: "submit_bet", Message: "transport failure", Ambiguous: true},
	}
	store, err := OpenStore(config, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(config, store, api, func() (string, string, error) { return "student", "password", nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), clock.Now)
	t.Setenv("KEIBA_ENABLE_FULL_DAY_SUBMISSION", "FULL-DAY-2026-08-15")
	if _, err := service.ImportBundle(bundle); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ArmBundle(bundle, bundle.BundleSHA256); err != nil {
		t.Fatal(err)
	}

	clock.Set(plan1.TargetSubmitTime)
	service.Tick(context.Background())
	snapshot := store.Snapshot()
	if !snapshot.Killed || snapshot.Races[plan1.PlanID].State != StateAmbiguous || snapshot.Races[plan2.PlanID].State != StateKilled {
		t.Fatalf("global halt snapshot=%+v", snapshot)
	}
	if api.submitCalls != 1 {
		t.Fatalf("expected exactly one POST, got %d", api.submitCalls)
	}

	clock.Set(*snapshot.Races[plan1.PlanID].NextActionAt)
	service.Tick(context.Background())
	if store.Snapshot().Races[plan1.PlanID].State != StateConfirmed || api.submitCalls != 1 {
		t.Fatalf("posted race was not safely reconciled: record=%+v calls=%d", store.Snapshot().Races[plan1.PlanID], api.submitCalls)
	}
	clock.Set(plan2.TargetSubmitTime)
	service.Tick(context.Background())
	if api.submitCalls != 1 {
		t.Fatalf("future race was submitted after global halt: calls=%d", api.submitCalls)
	}
}

func fullDayPlan(t *testing.T, policy Policy, created time.Time, payload RacePayload, post time.Time) PlanEnvelope {
	t.Helper()
	digest, err := PayloadSHA256(payload)
	if err != nil {
		t.Fatal(err)
	}
	return PlanEnvelope{
		SchemaVersion:      1,
		PlanID:             "day-" + payload.RaceID,
		PolicyID:           policy.PolicyID,
		RaceDate:           policy.ConnectionDate,
		CreatedAt:          created.UTC(),
		ScheduledPostTime:  post,
		TargetSubmitTime:   post.Add(-time.Duration(policy.Timing.TargetSubmitSecondsBeforePost) * time.Second),
		HardSubmitDeadline: post.Add(-time.Duration(policy.Timing.HardCutoffSecondsBeforePost) * time.Second),
		Payload:            payload,
		PayloadSHA256:      digest,
		Provenance: Provenance{
			ForecastPath:   "outputs/vote_plan_20260815.json",
			ForecastSHA256: strings.Repeat("a", 64),
			AdapterVersion: "votectl-prepare-day/v1",
		},
	}
}

func fullDayBundle(t *testing.T, policy Policy, created time.Time, plans []PlanEnvelope) PlanBundle {
	t.Helper()
	bundle := PlanBundle{
		SchemaVersion: 1,
		BundleID:      "full-day-test",
		PolicyID:      policy.PolicyID,
		RaceDate:      policy.ConnectionDate,
		CreatedAt:     created.UTC(),
		Plans:         plans,
		SourcePath:    "outputs/vote_plan_20260815.json",
		SourceSHA256:  strings.Repeat("b", 64),
	}
	digest, err := BundleSHA256(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundle.BundleSHA256 = digest
	if err := ValidateBundle(bundle, policy); err != nil {
		t.Fatal(err)
	}
	return bundle
}
