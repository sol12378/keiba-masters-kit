package voting

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func competitionPolicy(t *testing.T) Policy {
	t.Helper()
	policy := fullDayPolicy(t)
	policy.Submission.MaxBetsPerRace = 200
	policy.Submission.MaxExpandedBetPoints = 200
	policy.Submission.AllowedBetIDPatterns = []string{
		`^b[12]_c0_(?:[1-9]|1[0-8])$`,
		`^b3_c0_[1-8]_[1-8]$`,
		`^b[45]_c0_(?:[1-9]|1[0-8])_(?:[1-9]|1[0-8])$`,
		`^b6_c0_(?:[1-9]|1[0-8])_(?:[1-9]|1[0-8])$`,
		`^b7_c0_(?:[1-9]|1[0-8])_(?:[1-9]|1[0-8])_(?:[1-9]|1[0-8])$`,
		`^b8_c0_(?:[1-9]|1[0-8])_(?:[1-9]|1[0-8])_(?:[1-9]|1[0-8])$`,
	}
	return policy
}

func TestLoadRuntimeConfigAcceptsCompetitionTwoHundredPointPolicy(t *testing.T) {
	policy := competitionPolicy(t)
	root := t.TempDir()
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "competition-day-policy.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadRuntimeConfig(root, path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Policy.Submission.MaxBetsPerRace != 200 {
		t.Fatalf("got %d", config.Policy.Submission.MaxBetsPerRace)
	}
}

func TestValidatePolicyRejectsTwoHundredOnePoints(t *testing.T) {
	policy := competitionPolicy(t)
	policy.Submission.MaxBetsPerRace = 201
	policy.Submission.MaxExpandedBetPoints = 201
	err := ValidatePolicy(policy)
	if err == nil || !strings.Contains(err.Error(), "1..200") {
		t.Fatalf("got %v", err)
	}
}

func TestValidatePolicyAcceptsOnlyTheAuthorizedAllInRaceCeiling(t *testing.T) {
	policy := competitionPolicy(t)
	policy.Submission.MaxStakePerRace = 33300
	policy.Submission.MaxDailyTotalStake = 535000
	policy.Submission.MaxTotalStake = 535000
	if err := ValidatePolicy(policy); err != nil {
		t.Fatal(err)
	}
	policy.Submission.MaxStakePerRace = 33400
	if err := ValidatePolicy(policy); err == nil || !strings.Contains(err.Error(), "100..33300") {
		t.Fatalf("expected all-in ceiling rejection, got %v", err)
	}
}

func TestValidatePolicyRejectsInvalidOrIncompleteMultiBetPatterns(t *testing.T) {
	policy := competitionPolicy(t)
	policy.Submission.AllowedBetIDPatterns = []string{"["}
	if err := ValidatePolicy(policy); err == nil {
		t.Fatal("expected invalid regexp error")
	}
	policy = competitionPolicy(t)
	policy.Submission.AllowedBetIDPatterns = policy.Submission.AllowedBetIDPatterns[:1]
	if err := ValidatePolicy(policy); err == nil || !strings.Contains(err.Error(), "every official pool") {
		t.Fatalf("got %v", err)
	}
}

func TestValidatePolicyRequiresCanonicalCombinationOrdering(t *testing.T) {
	policy := competitionPolicy(t)
	policy.Submission.RequireSortedCombinations = false
	if err := ValidatePolicy(policy); err == nil || !strings.Contains(err.Error(), "canonical combination ordering") {
		t.Fatalf("got %v", err)
	}
}

func TestValidatePolicyRequiresDateScopedRehearsalGateContract(t *testing.T) {
	policy := competitionPolicy(t)
	policy.ConnectionDate = "2026-08-23"
	policy.Submission.MaxStakePerRace = 100
	policy.Submission.MaxDailyTotalStake = 3600
	policy.Submission.MaxTotalStake = 3600
	policy.Submission.StakeUnit = 100
	policy.ModelGate = ModelGate{
		Required:       true,
		ApprovalStatus: RehearsalModelApprovalStatus,
		ApprovalScope:  RehearsalApprovalScope,
		TargetDate:     "20260823",
		GatePath:       "configs/rehearsal-gate.json",
		GateSHA256:     strings.Repeat("a", 64),
		ModelPath:      "models/model.pkl",
		ModelSHA256:    strings.Repeat("b", 64),
		AdapterPath:    "scripts/adapter.py",
		AdapterSHA256:  strings.Repeat("c", 64),
		ValidationPath: "outputs/validation.json",
		ValidationSHA:  strings.Repeat("d", 64),
		ApprovalID:     "approval-1",
	}
	if err := ValidatePolicy(policy); err != nil {
		t.Fatal(err)
	}

	policy.ModelGate.TargetDate = "20260822"
	if err := ValidatePolicy(policy); err == nil || !strings.Contains(err.Error(), "date-scoped") {
		t.Fatalf("expected target-date rejection, got %v", err)
	}
	policy.ModelGate.TargetDate = "20260823"
	policy.Submission.MaxStakePerRace = 200
	if err := ValidatePolicy(policy); err == nil || !strings.Contains(err.Error(), "100 points") {
		t.Fatalf("expected per-race budget rejection, got %v", err)
	}
	policy.Submission.MaxStakePerRace = 100
	policy.Submission.MaxDailyTotalStake = 3700
	if err := ValidatePolicy(policy); err == nil || !strings.Contains(err.Error(), "3600") {
		t.Fatalf("expected daily budget rejection, got %v", err)
	}
}

func TestLoadRuntimeConfigVerifiesRequiredGateArtifacts(t *testing.T) {
	root := t.TempDir()
	policy := competitionPolicy(t)
	policy.ModelGate = ModelGate{
		Required:       true,
		ApprovalStatus: RehearsalModelApprovalStatus,
		ApprovalScope:  RehearsalApprovalScope,
		TargetDate:     policy.ConnectionDate,
		GatePath:       "configs/rehearsal-gate.json",
		ModelPath:      "models/model.pkl",
		AdapterPath:    "scripts/adapter.py",
		ValidationPath: "outputs/validation.json",
		ApprovalID:     "approval-1",
	}
	policy.Submission.MaxStakePerRace = 100
	policy.Submission.MaxDailyTotalStake = 3600
	policy.Submission.MaxTotalStake = 3600
	policy.Submission.StakeUnit = 100
	paths := map[string][]byte{
		policy.ModelGate.GatePath:       []byte(`{"gate":"rehearsal"}`),
		policy.ModelGate.ModelPath:      []byte("model"),
		policy.ModelGate.AdapterPath:    []byte("adapter"),
		policy.ModelGate.ValidationPath: []byte("validation"),
	}
	for relative, data := range paths {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		hex := fmt.Sprintf("%x", digest[:])
		switch relative {
		case policy.ModelGate.GatePath:
			policy.ModelGate.GateSHA256 = hex
		case policy.ModelGate.ModelPath:
			policy.ModelGate.ModelSHA256 = hex
		case policy.ModelGate.AdapterPath:
			policy.ModelGate.AdapterSHA256 = hex
		case policy.ModelGate.ValidationPath:
			policy.ModelGate.ValidationSHA = hex
		}
	}
	policyPath := filepath.Join(root, "policy.json")
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeConfig(root, policyPath); err != nil {
		t.Fatal(err)
	}

	policy.ModelGate.ModelSHA256 = strings.Repeat("e", 64)
	data, err = json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeConfig(root, policyPath); err == nil || !strings.Contains(err.Error(), "model artifact SHA-256") {
		t.Fatalf("expected artifact hash rejection, got %v", err)
	}
}

func TestLoadRuntimeConfigRejectsRequiredGateSymlink(t *testing.T) {
	root := t.TempDir()
	policy := competitionPolicy(t)
	policy.ModelGate = ModelGate{
		Required:       true,
		ApprovalStatus: RehearsalModelApprovalStatus,
		ApprovalScope:  RehearsalApprovalScope,
		TargetDate:     policy.ConnectionDate,
		GatePath:       "gate.json",
		GateSHA256:     strings.Repeat("a", 64),
		ModelPath:      "model.pkl",
		ModelSHA256:    strings.Repeat("b", 64),
		AdapterPath:    "adapter.py",
		AdapterSHA256:  strings.Repeat("c", 64),
		ValidationPath: "validation.json",
		ValidationSHA:  strings.Repeat("d", 64),
		ApprovalID:     "approval-1",
	}
	policy.Submission.MaxStakePerRace = 100
	policy.Submission.MaxDailyTotalStake = 3600
	policy.Submission.MaxTotalStake = 3600
	policy.Submission.StakeUnit = 100
	for _, relative := range []string{"gate.json", "model.pkl", "adapter.py", "validation.json"} {
		path := filepath.Join(root, relative)
		if err := os.WriteFile(path, []byte(relative), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Make the model path point at a symlink after creating its target. The
	// declared digest is irrelevant: LoadRuntimeConfig must reject the path
	// before reading through it.
	if err := os.Remove(filepath.Join(root, "model.pkl")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "real-model.pkl")
	if err := os.WriteFile(target, []byte("model.pkl"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "model.pkl")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	policyPath := filepath.Join(root, "policy.json")
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeConfig(root, policyPath); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}
