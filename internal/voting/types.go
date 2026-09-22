package voting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	StateDiscovered          = "DISCOVERED"
	StateValidated           = "VALIDATED"
	StateArmed               = "ARMED"
	StatePrecheckedEmpty     = "PRECHECKED_EMPTY"
	StatePosting             = "POSTING"
	StatePendingConfirmation = "PENDING_CONFIRMATION"
	StateConfirmed           = "CONFIRMED"
	StateConfirmedExisting   = "CONFIRMED_EXISTING"
	StateConflict            = "CONFLICT"
	StateAmbiguous           = "AMBIGUOUS"
	StateRejected            = "REJECTED"
	StateExpired             = "EXPIRED"
	StateKilled              = "KILLED"
)

var (
	raceIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{12}$`)
	betIDPattern  = regexp.MustCompile(`^b([1-8])_c([0-9]+)_([0-9_]+)$`)
	hex64Pattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type Bet struct {
	BetID string `json:"bet_id"`
	Money string `json:"money"`
}

type RacePayload struct {
	RaceID  string         `json:"race_id"`
	Mark    map[string]int `json:"mark"`
	BetList []Bet          `json:"bet_list"`
}

type Provenance struct {
	ForecastPath   string  `json:"forecast_path"`
	ForecastSHA256 string  `json:"forecast_sha256"`
	ModelSHA256    *string `json:"model_sha256,omitempty"`
	AdapterVersion string  `json:"adapter_version"`
}

type PlanEnvelope struct {
	SchemaVersion      int         `json:"schema_version"`
	PlanID             string      `json:"plan_id"`
	PolicyID           string      `json:"policy_id"`
	RaceDate           string      `json:"race_date"`
	CreatedAt          time.Time   `json:"created_at"`
	ScheduledPostTime  time.Time   `json:"scheduled_post_time"`
	TargetSubmitTime   time.Time   `json:"target_submit_time"`
	HardSubmitDeadline time.Time   `json:"hard_submit_deadline"`
	Payload            RacePayload `json:"payload"`
	PayloadSHA256      string      `json:"payload_sha256"`
	Provenance         Provenance  `json:"provenance"`
}

type PlanBundle struct {
	SchemaVersion int            `json:"schema_version"`
	BundleID      string         `json:"bundle_id"`
	PolicyID      string         `json:"policy_id"`
	RaceDate      string         `json:"race_date"`
	CreatedAt     time.Time      `json:"created_at"`
	Plans         []PlanEnvelope `json:"plans"`
	BundleSHA256  string         `json:"bundle_sha256"`
	SourcePath    string         `json:"source_path"`
	SourceSHA256  string         `json:"source_sha256"`
}

// PlanEnvelopeSHA256 is the cross-language hash of the complete PlanEnvelope
// as serialized by the competition adapter. It uses maps at every object
// boundary so encoding/json's sorted map keys match Python sort_keys=True.
func PlanEnvelopeSHA256(plan PlanEnvelope) (string, error) {
	payloadBytes, err := CanonicalPayload(plan.Payload)
	if err != nil {
		return "", err
	}
	var payload any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return "", err
	}
	provenance := map[string]any{
		"forecast_path":   plan.Provenance.ForecastPath,
		"forecast_sha256": plan.Provenance.ForecastSHA256,
		"adapter_version": plan.Provenance.AdapterVersion,
	}
	if plan.Provenance.ModelSHA256 != nil {
		provenance["model_sha256"] = *plan.Provenance.ModelSHA256
	}
	material := map[string]any{
		"schema_version":       plan.SchemaVersion,
		"plan_id":              plan.PlanID,
		"policy_id":            plan.PolicyID,
		"race_date":            plan.RaceDate,
		"created_at":           plan.CreatedAt.UTC().Format(time.RFC3339Nano),
		"scheduled_post_time":  plan.ScheduledPostTime.UTC().Format(time.RFC3339Nano),
		"target_submit_time":   plan.TargetSubmitTime.UTC().Format(time.RFC3339Nano),
		"hard_submit_deadline": plan.HardSubmitDeadline.UTC().Format(time.RFC3339Nano),
		"payload":              payload,
		"payload_sha256":       plan.PayloadSHA256,
		"provenance":           provenance,
	}
	data, err := json.Marshal(material)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func CanonicalPayload(payload RacePayload) ([]byte, error) {
	if err := ValidatePayload(payload); err != nil {
		return nil, err
	}
	// A map is intentional here: encoding/json sorts string map keys, which
	// matches Python's sort_keys=True, separators=(",", ":") contract at the
	// payload root as well as within mark.
	return json.Marshal(map[string]any{
		"race_id":  payload.RaceID,
		"mark":     payload.Mark,
		"bet_list": payload.BetList,
	})
}

func PayloadSHA256(payload RacePayload) (string, error) {
	data, err := CanonicalPayload(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func BundleSHA256(bundle PlanBundle) (string, error) {
	entries := make([]map[string]string, 0, len(bundle.Plans))
	for _, plan := range bundle.Plans {
		entries = append(entries, map[string]string{
			"plan_id":             plan.PlanID,
			"payload_sha256":      plan.PayloadSHA256,
			"scheduled_post_time": plan.ScheduledPostTime.UTC().Format(time.RFC3339Nano),
			"target_submit_time":  plan.TargetSubmitTime.UTC().Format(time.RFC3339Nano),
		})
	}
	data, err := json.Marshal(map[string]any{
		"policy_id": bundle.PolicyID,
		"race_date": bundle.RaceDate,
		"plans":     entries,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func PayloadStake(payload RacePayload) int {
	total := 0
	for _, bet := range payload.BetList {
		money, err := strconv.Atoi(bet.Money)
		if err == nil {
			total += money
		}
	}
	return total
}

func ValidatePayload(payload RacePayload) error {
	if !raceIDPattern.MatchString(payload.RaceID) {
		return errors.New("race_id must be exactly 12 ASCII letters/digits")
	}
	if len(payload.Mark) == 0 || len(payload.Mark) > 18 {
		return errors.New("mark must contain 1 through 18 horses")
	}
	counts := map[int]int{}
	for horse, code := range payload.Mark {
		n, err := strconv.Atoi(horse)
		if err != nil || n < 1 || n > 18 || strconv.Itoa(n) != horse {
			return fmt.Errorf("invalid mark horse number %q", horse)
		}
		if code < 1 || code > 4 {
			return fmt.Errorf("invalid mark code for horse %s", horse)
		}
		counts[code]++
	}
	if counts[1] != 1 {
		return errors.New("mark must contain exactly one code 1")
	}
	if counts[2] > 1 || counts[3] > 1 {
		return errors.New("mark codes 2 and 3 may appear at most once")
	}
	if len(payload.BetList) == 0 || len(payload.BetList) > 200 {
		return errors.New("bet_list must contain 1 through 200 entries")
	}
	seen := make(map[string]struct{}, len(payload.BetList))
	for index, bet := range payload.BetList {
		if err := ValidateBetID(bet.BetID); err != nil {
			return fmt.Errorf("bet_list[%d].bet_id: %w", index, err)
		}
		if _, exists := seen[bet.BetID]; exists {
			return fmt.Errorf("duplicate bet_id %q", bet.BetID)
		}
		seen[bet.BetID] = struct{}{}
		money, err := strconv.Atoi(bet.Money)
		if err != nil || money <= 0 || money%100 != 0 || strconv.Itoa(money) != bet.Money {
			return fmt.Errorf("bet_list[%d].money must be a positive 100-point unit string", index)
		}
	}
	return nil
}

// ValidateBetID enforces the official c0 straight-bet contract:
// b1 win, b2 place, b3 bracket quinella, b4 quinella, b5 wide,
// b6 exacta, b7 trio, and b8 trifecta.
func ValidateBetID(betID string) error {
	match := betIDPattern.FindStringSubmatch(betID)
	if match == nil || match[2] != "0" {
		return errors.New("has an invalid format")
	}
	betType, _ := strconv.Atoi(match[1])
	rawNumbers := strings.Split(match[3], "_")
	expected := map[int]int{1: 1, 2: 1, 3: 2, 4: 2, 5: 2, 6: 2, 7: 3, 8: 3}[betType]
	if len(rawNumbers) != expected {
		return fmt.Errorf("b%d requires exactly %d number(s)", betType, expected)
	}
	numbers := make([]int, len(rawNumbers))
	maximum := 18
	if betType == 3 {
		maximum = 8
	}
	seen := make(map[int]struct{}, len(rawNumbers))
	for index, raw := range rawNumbers {
		number, err := strconv.Atoi(raw)
		if err != nil || number < 1 || number > maximum || strconv.Itoa(number) != raw {
			return fmt.Errorf("b%d numbers must be canonical integers from 1 through %d", betType, maximum)
		}
		if _, duplicate := seen[number]; duplicate && betType != 3 {
			return fmt.Errorf("b%d numbers must be distinct", betType)
		}
		seen[number] = struct{}{}
		numbers[index] = number
	}
	// Unordered pools use one canonical representation. Ordered b6/b8 preserve
	// finishing order, but still reject repeated horses above.
	if betType == 3 {
		if numbers[0] > numbers[1] {
			return errors.New("b3 combination must be nondecreasing")
		}
	} else if betType == 4 || betType == 5 || betType == 7 {
		for index := 1; index < len(numbers); index++ {
			if numbers[index-1] >= numbers[index] {
				return fmt.Errorf("b%d combination must be strictly ascending", betType)
			}
		}
	}
	return nil
}

func ValidateTestPayload(payload RacePayload, policy Policy) error {
	if err := ValidatePayload(payload); err != nil {
		return err
	}
	if len(payload.BetList) > policy.Submission.MaxBetsPerRace {
		return errors.New("payload exceeds max_bets_per_race")
	}
	total := 0
	for _, bet := range payload.BetList {
		money, _ := strconv.Atoi(bet.Money)
		total += money
		if !matchesAnyPattern(bet.BetID, policy.Submission.AllowedBetIDPatterns) {
			return fmt.Errorf("bet_id %q is not allowed by policy", bet.BetID)
		}
		// ValidatePayload is the authoritative ordering/distinctness contract for
		// every pool. Do not duplicate it here: b3 is nondecreasing (same-frame
		// bracket quinella is valid), while b4/b5/b7 are strictly ascending.
	}
	maximum := MaxStakePerRace(policy)
	if total > maximum {
		return fmt.Errorf("race stake %d exceeds policy maximum %d", total, maximum)
	}
	return nil
}

func matchesAnyPattern(value string, patterns []string) bool {
	for _, raw := range patterns {
		pattern, err := regexp.Compile(raw)
		if err == nil && pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func ValidatePlan(plan PlanEnvelope, policy Policy) error {
	if plan.SchemaVersion != 1 {
		return errors.New("plan schema_version must be 1")
	}
	if plan.PlanID == "" || len(plan.PlanID) > 128 {
		return errors.New("plan_id is required and must be at most 128 characters")
	}
	for _, char := range plan.PlanID {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && !strings.ContainsRune("._-", char) {
			return errors.New("plan_id contains unsupported characters")
		}
	}
	if plan.PolicyID != policy.PolicyID {
		return errors.New("plan policy_id does not match active policy")
	}
	if plan.RaceDate != policy.ConnectionDate {
		return errors.New("plan race_date does not match connection date")
	}
	if plan.CreatedAt.IsZero() || plan.ScheduledPostTime.IsZero() || plan.TargetSubmitTime.IsZero() || plan.HardSubmitDeadline.IsZero() {
		return errors.New("all plan timestamps are required")
	}
	if !plan.TargetSubmitTime.Before(plan.HardSubmitDeadline) || !plan.HardSubmitDeadline.Before(plan.ScheduledPostTime) {
		return errors.New("plan timestamps are not in target < deadline < post order")
	}
	if !plan.CreatedAt.Before(plan.HardSubmitDeadline) {
		return errors.New("plan was created after its hard submit deadline")
	}
	location, err := time.LoadLocation(policy.Timezone)
	if err != nil || plan.ScheduledPostTime.In(location).Format("2006-01-02") != plan.RaceDate {
		return errors.New("scheduled post time does not match race_date and policy timezone")
	}
	expectedTarget := plan.ScheduledPostTime.Add(-time.Duration(policy.Timing.TargetSubmitSecondsBeforePost) * time.Second)
	expectedDeadline := plan.ScheduledPostTime.Add(-time.Duration(policy.Timing.HardCutoffSecondsBeforePost) * time.Second)
	if !plan.TargetSubmitTime.Equal(expectedTarget) || !plan.HardSubmitDeadline.Equal(expectedDeadline) {
		return errors.New("plan target/deadline do not match policy offsets")
	}
	if err := ValidateTestPayload(plan.Payload, policy); err != nil {
		return err
	}
	digest, err := PayloadSHA256(plan.Payload)
	if err != nil {
		return err
	}
	if !hex64Pattern.MatchString(plan.PayloadSHA256) || digest != plan.PayloadSHA256 {
		return errors.New("payload_sha256 mismatch")
	}
	if plan.Provenance.ForecastPath == "" || !hex64Pattern.MatchString(plan.Provenance.ForecastSHA256) || plan.Provenance.AdapterVersion == "" {
		return errors.New("plan provenance is incomplete")
	}
	if plan.Provenance.ModelSHA256 != nil && !hex64Pattern.MatchString(*plan.Provenance.ModelSHA256) {
		return errors.New("model_sha256 must be 64 lowercase hex characters")
	}
	if gate, required := EffectiveModelGate(policy); required {
		if plan.Provenance.ModelSHA256 == nil || *plan.Provenance.ModelSHA256 != gate.ModelSHA256 {
			return errors.New("plan model_sha256 does not match the approved model gate")
		}
	}
	return nil
}

func ValidateBundle(bundle PlanBundle, policy Policy) error {
	if bundle.SchemaVersion != 1 {
		return errors.New("bundle schema_version must be 1")
	}
	if bundle.BundleID == "" || len(bundle.BundleID) > 128 {
		return errors.New("bundle_id is required and must be at most 128 characters")
	}
	if bundle.PolicyID != policy.PolicyID || bundle.RaceDate != policy.ConnectionDate {
		return errors.New("bundle policy_id or race_date does not match active policy")
	}
	if bundle.CreatedAt.IsZero() || bundle.SourcePath == "" || !hex64Pattern.MatchString(bundle.SourceSHA256) {
		return errors.New("bundle creation time and source provenance are required")
	}
	if len(bundle.Plans) == 0 || len(bundle.Plans) > policy.Submission.MaxRaces {
		return fmt.Errorf("bundle must contain 1 through %d plans", policy.Submission.MaxRaces)
	}
	planIDs := make(map[string]struct{}, len(bundle.Plans))
	raceIDs := make(map[string]struct{}, len(bundle.Plans))
	totalStake := 0
	for index, plan := range bundle.Plans {
		if err := ValidatePlan(plan, policy); err != nil {
			return fmt.Errorf("plans[%d]: %w", index, err)
		}
		if !plan.CreatedAt.Equal(bundle.CreatedAt) {
			return fmt.Errorf("plans[%d] created_at does not match bundle", index)
		}
		if _, exists := planIDs[plan.PlanID]; exists {
			return fmt.Errorf("duplicate plan_id %q", plan.PlanID)
		}
		if _, exists := raceIDs[plan.Payload.RaceID]; exists {
			return fmt.Errorf("duplicate race_id %q", plan.Payload.RaceID)
		}
		planIDs[plan.PlanID] = struct{}{}
		raceIDs[plan.Payload.RaceID] = struct{}{}
		totalStake += PayloadStake(plan.Payload)
		if index > 0 {
			previous := bundle.Plans[index-1]
			if plan.TargetSubmitTime.Before(previous.TargetSubmitTime) ||
				(plan.TargetSubmitTime.Equal(previous.TargetSubmitTime) && plan.Payload.RaceID < previous.Payload.RaceID) {
				return errors.New("bundle plans must be sorted by target time and race_id")
			}
		}
	}
	if totalStake > MaxDailyStake(policy) {
		return fmt.Errorf("bundle daily stake %d exceeds policy maximum %d", totalStake, MaxDailyStake(policy))
	}
	digest, err := BundleSHA256(bundle)
	if err != nil {
		return err
	}
	if !hex64Pattern.MatchString(bundle.BundleSHA256) || digest != bundle.BundleSHA256 {
		return errors.New("bundle_sha256 mismatch")
	}
	return nil
}

type CheckedVote struct {
	RaceID string         `json:"race_id"`
	Mark   map[string]int `json:"mark"`
	Bet    []Bet          `json:"bet"`
}

func CheckedVoteMatches(payload RacePayload, votes []CheckedVote) bool {
	matches := make([]CheckedVote, 0, 1)
	for _, vote := range votes {
		if vote.RaceID == payload.RaceID {
			matches = append(matches, vote)
		}
	}
	if len(matches) != 1 {
		return false
	}
	actual := RacePayload{RaceID: matches[0].RaceID, Mark: matches[0].Mark, BetList: matches[0].Bet}
	expectedJSON, expectedErr := CanonicalPayload(payload)
	actualJSON, actualErr := CanonicalPayload(actual)
	return expectedErr == nil && actualErr == nil && string(expectedJSON) == string(actualJSON)
}

func SortVotes(votes []CheckedVote) {
	sort.Slice(votes, func(i, j int) bool { return votes[i].RaceID < votes[j].RaceID })
}
