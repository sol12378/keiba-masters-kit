package voting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type CredentialsProvider func() (loginID, password string, err error)

var (
	ErrKillSwitchActive = errors.New("kill switch file exists")
	ErrServerKilled     = errors.New("voting server is killed")
	ErrHardDeadline     = errors.New("hard submit deadline has passed")
)

type Service struct {
	config      RuntimeConfig
	store       *Store
	api         API
	credentials CredentialsProvider
	logger      *slog.Logger
	now         func() time.Time
	runMu       sync.Mutex
	controlMu   sync.Mutex
	tokenMu     sync.Mutex
	token       string
	tokenUntil  time.Time
}

func NewService(config RuntimeConfig, store *Store, api API, credentials CredentialsProvider, logger *slog.Logger, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	if credentials == nil {
		credentials = EnvironmentCredentials
	}
	return &Service{config: config, store: store, api: api, credentials: credentials, logger: logger, now: now}
}

func EnvironmentCredentials() (string, string, error) {
	loginID := strings.TrimSpace(os.Getenv("KEIBA_LOGIN_ID"))
	password := strings.TrimSpace(os.Getenv("KEIBA_PASSWORD"))
	if loginID == "" || password == "" {
		return "", "", errors.New("KEIBA_LOGIN_ID and KEIBA_PASSWORD are required")
	}
	return loginID, password, nil
}

func (service *Service) ImportPlan(data []byte) (RaceRecord, error) {
	service.controlMu.Lock()
	defer service.controlMu.Unlock()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var plan PlanEnvelope
	if err := decoder.Decode(&plan); err != nil {
		return RaceRecord{}, fmt.Errorf("decode plan: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return RaceRecord{}, errors.New("plan contains multiple JSON values")
	}
	if err := ValidatePlan(plan, service.config.Policy); err != nil {
		return RaceRecord{}, fmt.Errorf("validate plan: %w", err)
	}
	if err := service.validateAdditionalPlans([]PlanEnvelope{plan}); err != nil {
		return RaceRecord{}, err
	}
	if err := service.store.ImportPlan(plan); err != nil {
		return RaceRecord{}, err
	}
	return *service.store.Snapshot().Races[plan.PlanID], nil
}

func (service *Service) ImportBundle(bundle PlanBundle) ([]RaceRecord, error) {
	service.controlMu.Lock()
	defer service.controlMu.Unlock()
	if err := ValidateBundle(bundle, service.config.Policy); err != nil {
		return nil, fmt.Errorf("validate bundle: %w", err)
	}
	if err := service.validateAdditionalPlans(bundle.Plans); err != nil {
		return nil, err
	}
	result := make([]RaceRecord, 0, len(bundle.Plans))
	for _, plan := range bundle.Plans {
		snapshot := service.store.Snapshot()
		if existing, ok := snapshot.Races[plan.PlanID]; ok {
			if existing.Plan.PayloadSHA256 != plan.PayloadSHA256 || !existing.Plan.TargetSubmitTime.Equal(plan.TargetSubmitTime) {
				return nil, fmt.Errorf("existing plan_id %s differs from bundle", plan.PlanID)
			}
			result = append(result, *existing)
			continue
		}
		if err := service.store.ImportPlan(plan); err != nil {
			return nil, err
		}
		result = append(result, *service.store.Snapshot().Races[plan.PlanID])
	}
	return result, nil
}

func (service *Service) validateAdditionalPlans(plans []PlanEnvelope) error {
	snapshot := service.store.Snapshot()
	newCount := 0
	totalStake := 0
	knownPlanIDs := make(map[string]struct{}, len(snapshot.Races))
	knownRaceIDs := make(map[string]string, len(snapshot.Races))
	for planID, record := range snapshot.Races {
		knownPlanIDs[planID] = struct{}{}
		knownRaceIDs[record.Plan.Payload.RaceID] = record.Plan.PayloadSHA256
		if !(service.config.Policy.AutomaticRecovery && record.POSTAttempts == 0 && isTerminal(record.State)) {
			totalStake += PayloadStake(record.Plan.Payload)
		}
	}
	for _, plan := range plans {
		if _, exists := knownPlanIDs[plan.PlanID]; exists {
			continue
		}
		if digest, exists := knownRaceIDs[plan.Payload.RaceID]; exists {
			if digest != plan.PayloadSHA256 {
				return fmt.Errorf("a different plan already exists for race_id %s", plan.Payload.RaceID)
			}
			return fmt.Errorf("a plan already exists for race_id %s", plan.Payload.RaceID)
		}
		knownPlanIDs[plan.PlanID] = struct{}{}
		knownRaceIDs[plan.Payload.RaceID] = plan.PayloadSHA256
		newCount++
		totalStake += PayloadStake(plan.Payload)
	}
	if len(snapshot.Races)+newCount > service.config.Policy.Submission.MaxRaces {
		return fmt.Errorf("plan count exceeds daily maximum %d", service.config.Policy.Submission.MaxRaces)
	}
	if totalStake > MaxDailyStake(service.config.Policy) {
		return fmt.Errorf("planned daily stake %d exceeds maximum %d", totalStake, MaxDailyStake(service.config.Policy))
	}
	return nil
}

func (service *Service) Arm(planID, confirmation string) (RaceRecord, error) {
	service.controlMu.Lock()
	defer service.controlMu.Unlock()
	confirmation = strings.ToLower(strings.TrimSpace(confirmation))
	snapshot := service.store.Snapshot()
	if snapshot.Killed {
		return RaceRecord{}, ErrServerKilled
	}
	record, ok := snapshot.Races[planID]
	if !ok {
		return RaceRecord{}, errors.New("unknown plan_id")
	}
	if record.State != StateValidated {
		return RaceRecord{}, fmt.Errorf("plan state %s cannot be armed", record.State)
	}
	if confirmation != record.Plan.PayloadSHA256 {
		return RaceRecord{}, errors.New("payload confirmation hash mismatch")
	}
	if err := service.guard(record.Plan, true); err != nil {
		return RaceRecord{}, err
	}
	totalArms := 0
	for _, existing := range snapshot.Races {
		totalArms += existing.ArmCount
		if service.config.Policy.AutomaticRecovery && unresolvedPOST(existing) {
			return RaceRecord{}, errors.New("previous POST is awaiting exact reconciliation")
		}
		if existing.Armed {
			return RaceRecord{}, errors.New("another plan is already armed")
		}
	}
	if totalArms >= service.config.Policy.Submission.MaxArms {
		return RaceRecord{}, errors.New("policy maximum arm count has been reached")
	}
	err := service.store.UpdateRecord(planID, StateArmed, "arm_test", "", func(value *RaceRecord) error {
		value.Armed = true
		value.ArmCount++
		when := value.Plan.TargetSubmitTime
		value.NextActionAt = &when
		return nil
	})
	if err != nil {
		return RaceRecord{}, err
	}
	return *service.store.Snapshot().Races[planID], nil
}

func (service *Service) ArmBundle(bundle PlanBundle, confirmation string) ([]RaceRecord, error) {
	service.controlMu.Lock()
	defer service.controlMu.Unlock()
	confirmation = strings.ToLower(strings.TrimSpace(confirmation))
	if err := ValidateBundle(bundle, service.config.Policy); err != nil {
		return nil, fmt.Errorf("validate bundle: %w", err)
	}
	if confirmation != bundle.BundleSHA256 {
		return nil, errors.New("bundle confirmation hash mismatch")
	}
	snapshot := service.store.Snapshot()
	if snapshot.Killed {
		return nil, ErrServerKilled
	}
	if len(bundle.Plans) > service.config.Policy.Submission.MaxArms {
		return nil, errors.New("bundle exceeds policy arm limit")
	}
	totalArms := 0
	for _, record := range snapshot.Races {
		totalArms += record.ArmCount
		if record.Armed {
			return nil, errors.New("one or more plans are already armed")
		}
	}
	if totalArms+len(bundle.Plans) > service.config.Policy.Submission.MaxArms {
		return nil, errors.New("policy maximum arm count would be exceeded")
	}
	for _, plan := range bundle.Plans {
		record, ok := snapshot.Races[plan.PlanID]
		if !ok || record.Plan.PayloadSHA256 != plan.PayloadSHA256 {
			return nil, fmt.Errorf("bundle plan %s has not been imported exactly", plan.PlanID)
		}
		// The payload hash covers race_id, marks and bets -- not the schedule.
		// Re-planning the same selections for a different post time therefore
		// leaves the payload hash unchanged while the bundle hash moves, so an
		// operator can confirm a new bundle and arm the previously imported
		// schedule. Compare the times too, and make them say so.
		if !record.Plan.TargetSubmitTime.Equal(plan.TargetSubmitTime) ||
			!record.Plan.ScheduledPostTime.Equal(plan.ScheduledPostTime) ||
			!record.Plan.HardSubmitDeadline.Equal(plan.HardSubmitDeadline) {
			return nil, fmt.Errorf(
				"bundle plan %s carries a different schedule than the imported plan "+
					"(imported post %s, bundle post %s); re-import before arming",
				plan.PlanID,
				record.Plan.ScheduledPostTime.UTC().Format(time.RFC3339),
				plan.ScheduledPostTime.UTC().Format(time.RFC3339))
		}
		if record.State != StateValidated {
			return nil, fmt.Errorf("bundle plan %s state %s cannot be armed", plan.PlanID, record.State)
		}
		if err := service.guard(plan, true); err != nil {
			return nil, fmt.Errorf("bundle plan %s: %w", plan.PlanID, err)
		}
	}
	result := make([]RaceRecord, 0, len(bundle.Plans))
	for _, plan := range bundle.Plans {
		err := service.store.UpdateRecord(plan.PlanID, StateArmed, "arm_full_day", "", func(value *RaceRecord) error {
			value.Armed = true
			value.ArmCount++
			when := value.Plan.TargetSubmitTime
			value.NextActionAt = &when
			return nil
		})
		if err != nil {
			_ = service.store.SetKilled("partial full-day arm persistence failure")
			return nil, err
		}
		result = append(result, *service.store.Snapshot().Races[plan.PlanID])
	}
	return result, nil
}

func (service *Service) Kill(reason string) error {
	service.controlMu.Lock()
	defer service.controlMu.Unlock()
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("kill reason is required")
	}
	if len(reason) > 256 {
		return errors.New("kill reason is too long")
	}
	if err := service.store.SetKilled(reason); err != nil {
		return err
	}
	snapshot := service.store.Snapshot()
	for planID, record := range snapshot.Races {
		if record.Armed && record.POSTAttempts == 0 {
			if err := service.store.UpdateRecord(planID, StateKilled, "kill_armed_plan", "kill_switch", nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// Cancel a target-seeking arm without resetting any journal or POST history.
// A posted race can only proceed through the existing GET reconciliation path.
func (service *Service) CancelUnposted(planID string) error {
	service.controlMu.Lock()
	defer service.controlMu.Unlock()
	if !service.config.Policy.AutomaticRecovery {
		return errors.New("plan cancellation requires the dynamic policy")
	}
	record := service.store.Snapshot().Races[planID]
	if record == nil || record.POSTAttempts != 0 ||
		(record.State != StateValidated && record.State != StateArmed && record.State != StatePrecheckedEmpty) {
		return errors.New("only an unposted active plan can be cancelled")
	}
	return service.store.UpdateRecord(planID, StateKilled, "target_pause_cancel_unposted", "", nil)
}

func (service *Service) AuthCheck(ctx context.Context) error {
	if err := service.guardConnectionDate(); err != nil {
		return err
	}
	_, err := service.getToken(ctx, true)
	return err
}

func (service *Service) CheckRace(ctx context.Context, raceID string) ([]CheckedVote, error) {
	if !raceIDPattern.MatchString(raceID) {
		return nil, errors.New("race_id must be exactly 12 ASCII letters/digits")
	}
	if err := service.guardConnectionDate(); err != nil {
		return nil, err
	}
	token, err := service.getToken(ctx, false)
	if err != nil {
		return nil, err
	}
	checkContext, cancelCheck := context.WithTimeout(ctx, time.Duration(service.config.Policy.HTTP.LoginGETRequestTimeoutSeconds)*time.Second)
	defer cancelCheck()
	return service.api.Check(checkContext, token, raceID)
}

func (service *Service) Recover() error {
	service.controlMu.Lock()
	defer service.controlMu.Unlock()
	snapshot := service.store.Snapshot()
	for planID, record := range snapshot.Races {
		if record.State == StatePrecheckedEmpty && record.POSTAttempts == 0 {
			next := service.now().UTC()
			if err := service.store.UpdateRecord(planID, StateArmed, "restart_repeat_precheck", "", func(value *RaceRecord) error {
				value.Armed = true
				value.NextActionAt = &next
				return nil
			}); err != nil {
				return err
			}
			continue
		}
		if record.State != StatePosting && record.State != StatePendingConfirmation {
			continue
		}
		next := service.now().UTC()
		if err := service.store.UpdateRecord(planID, StateAmbiguous, "restart_get_only", "restart_after_post_boundary", func(value *RaceRecord) error {
			value.NextActionAt = &next
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) Run(ctx context.Context) error {
	if err := service.Recover(); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			service.Tick(ctx)
		}
	}
}

func (service *Service) Tick(ctx context.Context) {
	if !service.runMu.TryLock() {
		return
	}
	defer service.runMu.Unlock()
	service.controlMu.Lock()
	defer service.controlMu.Unlock()

	snapshot := service.store.Snapshot()
	planIDs := make([]string, 0, len(snapshot.Races))
	for planID := range snapshot.Races {
		record := snapshot.Races[planID]
		if service.config.Policy.AutomaticRecovery && record.State == StateValidated &&
			!service.now().Before(record.Plan.HardSubmitDeadline) {
			if err := service.store.UpdateRecord(planID, StateExpired, "expire_unarmed_plan", "deadline", nil); err != nil {
				service.logger.Error("cannot expire unarmed plan", "error", err)
			}
			continue
		}
		planIDs = append(planIDs, planID)
	}
	sort.Slice(planIDs, func(i, j int) bool {
		left := snapshot.Races[planIDs[i]].NextActionAt
		right := snapshot.Races[planIDs[j]].NextActionAt
		if left == nil {
			return false
		}
		if right == nil {
			return true
		}
		if left.Equal(*right) {
			return planIDs[i] < planIDs[j]
		}
		return left.Before(*right)
	})
	now := service.now().UTC()
	for _, planID := range planIDs {
		record := snapshot.Races[planID]
		if record.NextActionAt == nil || now.Before(*record.NextActionAt) {
			continue
		}
		var err error
		switch record.State {
		case StateArmed:
			err = service.executeSubmission(ctx, planID)
		case StatePosting, StatePendingConfirmation, StateAmbiguous:
			err = service.reconcile(ctx, planID)
		}
		if err != nil {
			service.logger.Error("voting action failed", "plan_id", planID, "state", record.State, "error", err)
		}
		// Network operations are globally serialized. Handle at most one due
		// action per tick, then re-evaluate persisted state on the next tick.
		return
	}
}

func (service *Service) executeSubmission(ctx context.Context, planID string) error {
	snapshot := service.store.Snapshot()
	record := snapshot.Races[planID]
	if record == nil || record.State != StateArmed || !record.Armed {
		return errors.New("plan is no longer armed")
	}
	if err := service.guard(record.Plan, true); err != nil {
		state := StateRejected
		if errors.Is(err, ErrKillSwitchActive) || errors.Is(err, ErrServerKilled) {
			state = StateKilled
		} else if errors.Is(err, ErrHardDeadline) {
			state = StateExpired
		}
		_ = service.store.UpdateRecord(planID, state, "submission_guard_failed", "guard", nil)
		return err
	}
	if service.config.Policy.AutomaticRecovery {
		if err := service.store.UpdateRecord(planID, StateArmed, "precheck_attempt", "", func(value *RaceRecord) error {
			value.PrecheckAttempts++
			return nil
		}); err != nil {
			return err
		}
	}
	token, err := service.getToken(ctx, service.config.Policy.AutomaticRecovery)
	if err != nil {
		if service.config.Policy.AutomaticRecovery {
			return service.retryUnpostedRead(planID, err)
		}
		_ = service.store.UpdateRecord(planID, StateRejected, "credentials_failed", "credentials", nil)
		service.haltFuturePlans("authentication failed")
		return err
	}
	checkContext, cancelCheck := context.WithTimeout(ctx, time.Duration(service.config.Policy.HTTP.LoginGETRequestTimeoutSeconds)*time.Second)
	votes, err := service.api.Check(checkContext, token, record.Plan.Payload.RaceID)
	cancelCheck()
	if err != nil && !IsEmptyPrecheckResponse(err) {
		if service.config.Policy.AutomaticRecovery {
			return service.retryUnpostedRead(planID, err)
		}
		// Preserve the safe response classification for operator diagnosis.
		// Previously only "api_contract" was journaled, which made a transient
		// authentication response indistinguishable from a malformed vote body.
		errorClass := classifyError(err)
		_ = service.store.UpdateRecord(planID, StateRejected, "precheck_failed", errorClass, func(value *RaceRecord) error {
			value.LastErrorClass = errorClass
			var apiError *APIError
			if errors.As(err, &apiError) {
				value.LastHTTPStatus = apiError.HTTPStatus
				value.LastAPIStatus = apiError.APIStatus
				value.LastAPIReason = safeAPIReason(apiError.APIReason)
			}
			return nil
		})
		service.haltFuturePlans("precheck failed: " + classifyError(err))
		return err
	}
	if IsEmptyPrecheckResponse(err) {
		votes = nil
	}
	if CheckedVoteMatches(record.Plan.Payload, votes) {
		return service.store.UpdateRecord(planID, StateConfirmedExisting, "precheck_exact_match", "", nil)
	}
	if len(votes) != 0 {
		err := service.store.UpdateRecord(planID, StateConflict, "precheck_existing_vote", "existing_vote", nil)
		service.haltFuturePlans("existing vote conflict")
		return err
	}
	if err := service.store.UpdateRecord(planID, StatePrecheckedEmpty, "precheck_empty", "", nil); err != nil {
		return err
	}
	if err := service.guard(record.Plan, true); err != nil {
		state := StateRejected
		if errors.Is(err, ErrKillSwitchActive) || errors.Is(err, ErrServerKilled) {
			state = StateKilled
		} else if errors.Is(err, ErrHardDeadline) {
			state = StateExpired
		}
		_ = service.store.UpdateRecord(planID, state, "post_guard_failed", "guard", nil)
		return err
	}
	if err := service.guardSubmissionBudget(planID); err != nil {
		_ = service.store.UpdateRecord(planID, StateRejected, "daily_budget_guard_failed", "daily_budget", nil)
		service.haltFuturePlans("daily budget guard failed")
		return err
	}
	if err := service.store.UpdateRecord(planID, StatePosting, "persist_before_post", "", func(value *RaceRecord) error {
		if value.POSTAttempts >= service.config.Policy.HTTP.POSTMaxAttempts {
			return errors.New("POST attempt limit reached")
		}
		value.POSTAttempts++
		value.Armed = false
		value.NextActionAt = nil
		return nil
	}); err != nil {
		return err
	}
	postContext, cancelPOST := context.WithTimeout(ctx, time.Duration(service.config.Policy.HTTP.POSTRequestTimeoutSeconds)*time.Second)
	receipt, err := service.api.Submit(postContext, token, record.Plan.Payload)
	cancelPOST()
	if err != nil {
		state := StateRejected
		var apiError *APIError
		if !errors.As(err, &apiError) || apiError.Ambiguous || service.config.Policy.AutomaticRecovery {
			state = StateAmbiguous
		}
		errorClass := classifyError(err)
		next := service.now().UTC().Add(time.Duration(service.config.Policy.Timing.VerificationInitialWait) * time.Second)
		_ = service.store.UpdateRecord(planID, state, "post_failed", errorClass, func(value *RaceRecord) error {
			value.LastErrorClass = errorClass
			if apiError != nil {
				value.LastHTTPStatus = apiError.HTTPStatus
				value.LastAPIStatus = apiError.APIStatus
				value.LastAPIReason = safeAPIReason(apiError.APIReason)
			}
			if state == StateAmbiguous {
				value.NextActionAt = &next
			}
			return nil
		})
		if !service.config.Policy.AutomaticRecovery {
			service.haltFuturePlans("POST failed: " + classifyError(err))
		}
		return err
	}
	next := service.now().UTC().Add(time.Duration(service.config.Policy.Timing.VerificationInitialWait) * time.Second)
	return service.store.UpdateRecord(planID, StatePendingConfirmation, "post_accepted_async", "", func(value *RaceRecord) error {
		value.RemainingMoney = receipt.RemainingMoney
		value.NextActionAt = &next
		return nil
	})
}

func (service *Service) reconcile(ctx context.Context, planID string) error {
	snapshot := service.store.Snapshot()
	record := snapshot.Races[planID]
	if record == nil || record.POSTAttempts == 0 {
		return errors.New("reconciliation requires a persisted POST attempt")
	}
	// A persisted ambiguous POST may survive a process restart, but a policy is
	// scoped to one connection date. Never turn an old checkpoint into an API
	// call after midnight; a new day's operator must review it offline.
	if err := service.guardConnectionDate(); err != nil {
		return service.deferReconciliation(planID, err)
	}
	token, err := service.getToken(ctx, service.config.Policy.AutomaticRecovery)
	if err != nil {
		// An uncertain POST must never allow later plans to continue while the
		// operator cannot authenticate and reconcile it.  The posted race is
		// still deferred as GET-only, but future POSTs are halted.
		if !service.config.Policy.AutomaticRecovery {
			service.haltFuturePlans("reconciliation authentication failed: " + classifyError(err))
		}
		return service.deferReconciliation(planID, err)
	}
	checkContext, cancelCheck := context.WithTimeout(ctx, time.Duration(service.config.Policy.HTTP.LoginGETRequestTimeoutSeconds)*time.Second)
	votes, err := service.api.Check(checkContext, token, record.Plan.Payload.RaceID)
	cancelCheck()
	if err != nil {
		if IsEmptyPrecheckResponse(err) {
			return service.deferReconciliation(planID, errors.New("vote not visible yet"))
		}
		// Every non-empty reconciliation failure is fail-closed.  HTTP 4xx,
		// transport loss, malformed responses, and rate limits all leave the
		// POST outcome unresolved; do not send another race until an operator
		// has reviewed the checkpoint.
		if !service.config.Policy.AutomaticRecovery {
			service.haltFuturePlans("reconciliation API failed: " + classifyError(err))
		}
		return service.deferReconciliation(planID, err)
	}
	if CheckedVoteMatches(record.Plan.Payload, votes) {
		return service.store.UpdateRecord(planID, StateConfirmed, "get_exact_match", "", func(value *RaceRecord) error {
			value.VerificationAttempts++
			return nil
		})
	}
	if len(votes) != 0 {
		err := service.store.UpdateRecord(planID, StateConflict, "get_mismatch", "reconciliation_mismatch", func(value *RaceRecord) error {
			value.VerificationAttempts++
			return nil
		})
		service.haltFuturePlans("reconciliation mismatch")
		return err
	}
	return service.deferReconciliation(planID, errors.New("vote not visible yet"))
}

func (service *Service) getToken(ctx context.Context, force bool) (string, error) {
	service.tokenMu.Lock()
	defer service.tokenMu.Unlock()
	now := service.now()
	if !force && service.token != "" && now.Before(service.tokenUntil) {
		return service.token, nil
	}
	loginID, password, err := service.credentials()
	if err != nil {
		return "", err
	}
	requestContext, cancel := context.WithTimeout(ctx, time.Duration(service.config.Policy.HTTP.LoginGETRequestTimeoutSeconds)*time.Second)
	defer cancel()
	token, err := service.api.Login(requestContext, loginID, password)
	if err != nil {
		service.token = ""
		service.tokenUntil = time.Time{}
		return "", err
	}
	service.token = token
	service.tokenUntil = now.Add(time.Duration(service.config.Policy.Token.LocalUsableLifetimeSeconds) * time.Second)
	return token, nil
}

func (service *Service) guardSubmissionBudget(planID string) error {
	snapshot := service.store.Snapshot()
	current := snapshot.Races[planID]
	if current == nil {
		return errors.New("unknown plan_id")
	}
	postAttempts := 0
	committedStake := 0
	for _, record := range snapshot.Races {
		if service.config.Policy.AutomaticRecovery && record.Plan.PlanID != planID && unresolvedPOST(record) {
			return errors.New("previous POST is awaiting exact reconciliation")
		}
		if record.POSTAttempts > 0 {
			postAttempts += record.POSTAttempts
			committedStake += PayloadStake(record.Plan.Payload)
		}
	}
	if current.POSTAttempts == 0 {
		if postAttempts >= service.config.Policy.Submission.MaxPOSTRequests {
			return errors.New("daily POST request limit reached")
		}
		committedStake += PayloadStake(current.Plan.Payload)
	}
	if committedStake > MaxDailyStake(service.config.Policy) {
		return fmt.Errorf("daily committed stake %d exceeds maximum %d", committedStake, MaxDailyStake(service.config.Policy))
	}
	return nil
}

func unresolvedPOST(record *RaceRecord) bool {
	return record.POSTAttempts > 0 && record.State != StateConfirmed && record.State != StateConfirmedExisting
}

// Retry only the read/login phase. No POST boundary has been crossed. Each
// attempt obtains fresh credentials; the deadline and original arm survive.
func (service *Service) retryUnpostedRead(planID string, cause error) error {
	record := service.store.Snapshot().Races[planID]
	if record == nil || record.POSTAttempts != 0 {
		return errors.New("read recovery cannot retry a posted race")
	}
	next := service.now().UTC().Add(10 * time.Second)
	state := StateArmed
	if record.PrecheckAttempts >= 4 || !next.Before(record.Plan.HardSubmitDeadline) {
		state = StateRejected
	}
	err := service.store.UpdateRecord(planID, state, "automatic_read_recovery", classifyError(cause), func(value *RaceRecord) error {
		value.LastErrorClass = classifyError(cause)
		value.LastErrorOperation = "local"
		var apiError *APIError
		if errors.As(cause, &apiError) {
			value.LastErrorOperation = apiError.Operation
			value.LastHTTPStatus = apiError.HTTPStatus
			value.LastAPIStatus = apiError.APIStatus
			value.LastAPIReason = safeAPIReason(apiError.APIReason)
		}
		value.NextActionAt = &next
		return nil
	})
	if err != nil {
		return err
	}
	return cause
}

func (service *Service) deferReconciliation(planID string, cause error) error {
	next := service.now().UTC().Add(time.Duration(service.config.Policy.Timing.VerificationInterval) * time.Second)
	err := service.store.UpdateRecord(planID, StateAmbiguous, "get_not_confirmed", classifyError(cause), func(value *RaceRecord) error {
		value.LastErrorClass = classifyError(cause)
		value.LastErrorOperation = "local"
		value.LastHTTPStatus = 0
		var apiError *APIError
		if errors.As(cause, &apiError) {
			value.LastErrorOperation = apiError.Operation
			value.LastHTTPStatus = apiError.HTTPStatus
		}
		value.VerificationAttempts++
		if value.VerificationAttempts < service.config.Policy.Timing.VerificationMaxGETAttempts {
			value.NextActionAt = &next
		} else {
			value.NextActionAt = nil
		}
		return nil
	})
	if err != nil {
		return err
	}
	if record := service.store.Snapshot().Races[planID]; record != nil &&
		record.VerificationAttempts >= service.config.Policy.Timing.VerificationMaxGETAttempts {
		service.haltFuturePlans("reconciliation attempts exhausted")
	}
	return cause
}

func (service *Service) haltFuturePlans(reason string) {
	snapshot := service.store.Snapshot()
	if !snapshot.Killed {
		if err := service.store.SetKilled(reason); err != nil {
			service.logger.Error("failed to persist global halt", "error", err)
			return
		}
		snapshot = service.store.Snapshot()
	}
	for planID, record := range snapshot.Races {
		if record.Armed && record.POSTAttempts == 0 {
			if err := service.store.UpdateRecord(planID, StateKilled, "global_halt_future_plan", "global_halt", nil); err != nil {
				service.logger.Error("failed to halt future plan", "plan_id", planID, "error", err)
			}
		}
	}
}

func isSevereAPIError(err error) bool {
	var apiError *APIError
	if !errors.As(err, &apiError) {
		return false
	}
	return apiError.HTTPStatus == httpStatusTooManyRequests || apiError.HTTPStatus >= 500
}

func (service *Service) guard(plan PlanEnvelope, requireEnable bool) error {
	if service.config.Policy.AutomaticRecovery {
		// Python persists the freeze before asking the daemon to stop. Honor
		// that marker even if the control request or process then crashes.
		if _, err := os.Stat(filepath.Join(service.config.StateDir, "FREEZE.json")); err == nil {
			return ErrKillSwitchActive
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("freeze state cannot be read")
		}
	}
	if _, err := os.Stat(service.config.KillSwitch); err == nil {
		return ErrKillSwitchActive
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("kill switch state cannot be read")
	}
	snapshot := service.store.Snapshot()
	if snapshot.Killed {
		return ErrServerKilled
	}
	now := service.now()
	if err := service.guardConnectionDate(); err != nil {
		return err
	}
	if !now.Before(plan.HardSubmitDeadline) {
		return ErrHardDeadline
	}
	if requireEnable {
		for name, expected := range service.config.Policy.Submission.RequiredEnvironment {
			if os.Getenv(name) != expected {
				return fmt.Errorf("required environment %s does not match policy", name)
			}
		}
	}
	return nil
}

func (service *Service) guardConnectionDate() error {
	location, err := time.LoadLocation(service.config.Policy.Timezone)
	if err != nil {
		return errors.New("policy timezone is unavailable")
	}
	if service.now().In(location).Format("2006-01-02") != service.config.Policy.ConnectionDate {
		return errors.New("current date does not match connection date")
	}
	return nil
}

func classifyError(err error) string {
	if err == nil {
		return ""
	}
	var apiError *APIError
	if errors.As(err, &apiError) {
		if apiError.Ambiguous {
			return "ambiguous_transport_or_response"
		}
		if apiError.HTTPStatus == httpStatusTooManyRequests {
			return "http_429"
		}
		if apiError.HTTPStatus >= 500 {
			return "http_5xx"
		}
		return "api_contract"
	}
	return "local_guard_or_contract"
}

const httpStatusTooManyRequests = 429
