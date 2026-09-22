package voting

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func minimalPayload() RacePayload {
	return RacePayload{
		RaceID:  "202601010101",
		Mark:    map[string]int{"3": 1},
		BetList: []Bet{{BetID: "b3_c0_2_5", Money: "100"}},
	}
}

func TestFullDayBundleRejectsDailyStakeOver500000(t *testing.T) {
	policy := fullDayPolicy(t)
	location, _ := time.LoadLocation("Asia/Tokyo")
	created := time.Date(2026, 8, 15, 8, 0, 0, 0, location)
	plans := make([]PlanEnvelope, 0, 26)
	for index := range 26 {
		payload := RacePayload{
			RaceID:  fmt.Sprintf("20260101%04d", index+1),
			Mark:    map[string]int{"3": 1},
			BetList: []Bet{{BetID: "b3_c0_2_5", Money: "20000"}},
		}
		post := time.Date(2026, 8, 15, 10, index, 0, 0, location)
		plans = append(plans, fullDayPlan(t, policy, created, payload, post))
	}
	bundle := PlanBundle{
		SchemaVersion: 1,
		BundleID:      "over-budget",
		PolicyID:      policy.PolicyID,
		RaceDate:      policy.ConnectionDate,
		CreatedAt:     created.UTC(),
		Plans:         plans,
		SourcePath:    "outputs/vote_plan.json",
		SourceSHA256:  strings.Repeat("c", 64),
	}
	digest, err := BundleSHA256(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundle.BundleSHA256 = digest
	if err := ValidateBundle(bundle, policy); err == nil || !strings.Contains(err.Error(), "daily stake") {
		t.Fatalf("expected daily stake error, got %v", err)
	}
}

func TestRequiredModelGatePinsEveryPlanToApprovedModel(t *testing.T) {
	policy := fullDayPolicy(t)
	approved := strings.Repeat("d", 64)
	policy.ModelGate.Required = true
	policy.ModelGate.ApprovalStatus = "APPROVED_FOR_COMPETITION_SUBMISSION"
	policy.ModelGate.ApprovalID = "approval-test"
	policy.ModelGate.ModelPath = "models/approved.pkl"
	policy.ModelGate.ModelSHA256 = approved
	policy.ModelGate.AdapterPath = "scripts/approved.py"
	policy.ModelGate.AdapterSHA256 = strings.Repeat("e", 64)
	policy.ModelGate.ValidationPath = "outputs/validation.json"
	policy.ModelGate.ValidationSHA = strings.Repeat("f", 64)
	if err := ValidatePolicy(policy); err != nil {
		t.Fatal(err)
	}
	location, _ := time.LoadLocation("Asia/Tokyo")
	plan := fullDayPlan(t, policy, time.Date(2026, 8, 15, 8, 0, 0, 0, location), minimalPayload(), time.Date(2026, 8, 15, 10, 0, 0, 0, location))
	if err := ValidatePlan(plan, policy); err == nil || !strings.Contains(err.Error(), "approved model gate") {
		t.Fatalf("expected missing model gate error, got %v", err)
	}
	plan.Provenance.ModelSHA256 = &approved
	if err := ValidatePlan(plan, policy); err != nil {
		t.Fatal(err)
	}
}

func TestPayloadSHA256MatchesPythonGoldenVector(t *testing.T) {
	digest, err := PayloadSHA256(minimalPayload())
	if err != nil {
		t.Fatal(err)
	}
	const expected = "75c8271f08d6801e542dd354e4918c52cf14369351a1fc09205da495ca18de4f"
	if digest != expected {
		t.Fatalf("digest=%s expected=%s", digest, expected)
	}
}

func TestValidatePayloadRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RacePayload)
	}{
		{"bad race", func(value *RacePayload) { value.RaceID = "short" }},
		{"no main mark", func(value *RacePayload) { value.Mark = map[string]int{"3": 2} }},
		{"bad money", func(value *RacePayload) { value.BetList[0].Money = "101" }},
		{"leading-zero money", func(value *RacePayload) { value.BetList[0].Money = "0100" }},
		{"whitespace money", func(value *RacePayload) { value.BetList[0].Money = " 100" }},
		{"bad bet id", func(value *RacePayload) { value.BetList[0].BetID = "x" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := minimalPayload()
			test.mutate(&value)
			if err := ValidatePayload(value); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestCheckedVoteMatchesOfficialBetField(t *testing.T) {
	payload := minimalPayload()
	votes := []CheckedVote{{RaceID: payload.RaceID, Mark: map[string]int{"3": 1}, Bet: []Bet{{BetID: "b3_c0_2_5", Money: "100"}}}}
	if !CheckedVoteMatches(payload, votes) {
		t.Fatal("expected exact match")
	}
	votes[0].Bet[0].Money = "200"
	if CheckedVoteMatches(payload, votes) {
		t.Fatal("different amount must not match")
	}
}

func TestValidateBetIDAllOfficialPools(t *testing.T) {
	valid := []string{"b1_c0_18", "b2_c0_1", "b3_c0_1_8", "b3_c0_8_8", "b4_c0_9_18", "b5_c0_3_12", "b6_c0_12_3", "b7_c0_3_8_12", "b8_c0_12_3_8"}
	for _, betID := range valid {
		if err := ValidateBetID(betID); err != nil {
			t.Errorf("%s: %v", betID, err)
		}
	}
	invalid := []string{"b1_c0_0", "b2_c0_19", "b3_c0_8_7", "b4_c0_18_9", "b5_c0_3_3", "b6_c0_12_12", "b7_c0_3_12_8", "b8_c0_12_3_12", "b8_c1_12_3_8", "b8_c0_1_2", "b9_c0_1"}
	for _, betID := range invalid {
		if err := ValidateBetID(betID); err == nil {
			t.Errorf("%s: expected validation error", betID)
		}
	}
}

func TestValidateBetIDExhaustiveArityRangeAndOrder(t *testing.T) {
	for betType := 1; betType <= 8; betType++ {
		for first := 0; first <= 19; first++ {
			for second := 0; second <= 19; second++ {
				for third := 0; third <= 19; third++ {
					var betID string
					var expected bool
					switch betType {
					case 1, 2:
						if second != 0 || third != 0 {
							continue
						}
						betID = fmt.Sprintf("b%d_c0_%d", betType, first)
						expected = 1 <= first && first <= 18
					case 3:
						if third != 0 {
							continue
						}
						betID = fmt.Sprintf("b3_c0_%d_%d", first, second)
						expected = 1 <= first && first <= second && second <= 8
					case 4, 5:
						if third != 0 {
							continue
						}
						betID = fmt.Sprintf("b%d_c0_%d_%d", betType, first, second)
						expected = 1 <= first && first < second && second <= 18
					case 6:
						if third != 0 {
							continue
						}
						betID = fmt.Sprintf("b6_c0_%d_%d", first, second)
						expected = 1 <= first && first <= 18 && 1 <= second && second <= 18 && first != second
					case 7:
						betID = fmt.Sprintf("b7_c0_%d_%d_%d", first, second, third)
						expected = 1 <= first && first < second && second < third && third <= 18
					case 8:
						betID = fmt.Sprintf("b8_c0_%d_%d_%d", first, second, third)
						expected = 1 <= first && first <= 18 && 1 <= second && second <= 18 && 1 <= third && third <= 18 && first != second && first != third && second != third
					}
					if accepted := ValidateBetID(betID) == nil; accepted != expected {
						t.Fatalf("%s accepted=%v expected=%v", betID, accepted, expected)
					}
				}
			}
		}
	}
}

func TestValidatePayloadAcceptsTwoHundredPoints(t *testing.T) {
	payload := minimalPayload()
	payload.BetList = make([]Bet, 0, 200)
	for first := 1; first <= 18 && len(payload.BetList) < 200; first++ {
		for second := 1; second <= 18 && len(payload.BetList) < 200; second++ {
			if first != second {
				payload.BetList = append(payload.BetList, Bet{BetID: fmt.Sprintf("b6_c0_%d_%d", first, second), Money: "100"})
			}
		}
	}
	if err := ValidatePayload(payload); err != nil {
		t.Fatal(err)
	}
	payload.BetList = append(payload.BetList, Bet{BetID: "b1_c0_1", Money: "100"})
	if err := ValidatePayload(payload); err == nil {
		t.Fatal("expected 200-point limit error")
	}
}

func TestValidatePayloadRejectsDuplicateBetIDWithDifferentMoney(t *testing.T) {
	payload := minimalPayload()
	payload.BetList = []Bet{{BetID: "b5_c0_3_12", Money: "100"}, {BetID: "b5_c0_3_12", Money: "200"}}
	if err := ValidatePayload(payload); err == nil || !strings.Contains(err.Error(), "duplicate bet_id") {
		t.Fatalf("got %v", err)
	}
}

func TestValidateTestPayloadAcceptsSameFrameBracketQuinella(t *testing.T) {
	policy := fullDayPolicy(t)
	payload := minimalPayload()
	payload.BetList = []Bet{{BetID: "b3_c0_8_8", Money: "100"}}
	if err := ValidateTestPayload(payload, policy); err != nil {
		t.Fatal(err)
	}
}

func testPlan(t *testing.T, policy Policy, now time.Time) PlanEnvelope {
	t.Helper()
	payload := minimalPayload()
	digest, err := PayloadSHA256(payload)
	if err != nil {
		t.Fatal(err)
	}
	post := time.Date(2026, 8, 15, 12, 0, 0, 0, now.Location())
	return PlanEnvelope{
		SchemaVersion:      1,
		PlanID:             "test-plan",
		PolicyID:           policy.PolicyID,
		RaceDate:           policy.ConnectionDate,
		CreatedAt:          now.UTC(),
		ScheduledPostTime:  post,
		TargetSubmitTime:   post.Add(-time.Duration(policy.Timing.TargetSubmitSecondsBeforePost) * time.Second),
		HardSubmitDeadline: post.Add(-time.Duration(policy.Timing.HardCutoffSecondsBeforePost) * time.Second),
		Payload:            payload,
		PayloadSHA256:      digest,
		Provenance: Provenance{
			ForecastPath:   "outputs/vote_plan.json",
			ForecastSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			AdapterVersion: "test/v1",
		},
	}
}
