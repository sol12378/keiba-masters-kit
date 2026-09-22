package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sol12378/keiba-masters-kit/internal/voting"
)

func TestPrepareTestConvertsOnePythonPlanRaceToGuardedEnvelope(t *testing.T) {
	originalClock := currentTime
	currentTime = func() time.Time { return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalClock })
	repositoryRoot, err := voting.FindProjectRoot()
	if err != nil {
		t.Fatal(err)
	}
	config, err := voting.LoadRuntimeConfig(repositoryRoot, "configs/voting_policy.json")
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	config.Root = temporary
	config.StateDir = filepath.Join(temporary, "outputs", "voting-server")
	source := filepath.Join(temporary, "vote_plan.json")
	input := map[string]any{
		"bet_data": []any{map[string]any{
			"race_id": "202601010101",
			"mark":    map[string]int{"3": 1},
			"bet_list": []any{map[string]string{
				"bet_id": "b3_c0_2_5",
				"money":  "20000",
			}},
		}},
	}
	data, _ := json.Marshal(input)
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(temporary, "prepared.json")
	if err := prepareTest(config, []string{
		"--source", source,
		"--index", "0",
		"--post-time", "2026-08-15T12:00:00+09:00",
		"--output", output,
	}); err != nil {
		t.Fatal(err)
	}
	prepared, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var plan voting.PlanEnvelope
	if err := json.Unmarshal(prepared, &plan); err != nil {
		t.Fatal(err)
	}
	if err := voting.ValidatePlan(plan, config.Policy); err != nil {
		t.Fatal(err)
	}
	if plan.Payload.BetList[0].Money != "100" {
		t.Fatalf("money=%s", plan.Payload.BetList[0].Money)
	}
	post, _ := time.Parse(time.RFC3339, "2026-08-15T12:00:00+09:00")
	if !plan.TargetSubmitTime.Equal(post.Add(-5*time.Minute)) || !plan.HardSubmitDeadline.Equal(post.Add(-4*time.Minute)) {
		t.Fatal("unexpected target/deadline")
	}
}

func TestPrepareDayBuildsSortedBudgetedBundle(t *testing.T) {
	originalClock := currentTime
	currentTime = func() time.Time { return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalClock })
	repositoryRoot, err := voting.FindProjectRoot()
	if err != nil {
		t.Fatal(err)
	}
	config, err := voting.LoadRuntimeConfig(repositoryRoot, "configs/voting_policy_fullday.json")
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	config.Root = temporary
	config.StateDir = filepath.Join(temporary, "outputs", "voting-server-full-day")
	modelPath := filepath.Join(temporary, "approved-model.pkl")
	modelBytes := []byte("approved competition model")
	if err := os.WriteFile(modelPath, modelBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	modelHash := sha256.Sum256(modelBytes)
	modelDigest := hex.EncodeToString(modelHash[:])
	config.Policy.ModelGate.Required = true
	config.Policy.ModelGate.ModelSHA256 = modelDigest
	payload1 := voting.RacePayload{
		RaceID:  "202601010101",
		Mark:    map[string]int{"3": 1},
		BetList: []voting.Bet{{BetID: "b3_c0_2_5", Money: "20000"}},
	}
	payload2 := voting.RacePayload{
		RaceID:  "202604020101",
		Mark:    map[string]int{"9": 1},
		BetList: []voting.Bet{{BetID: "b3_c0_1_7", Money: "100"}},
	}
	input := map[string]any{
		"date":        "2026-08-15",
		"model":       map[string]string{"path": modelPath, "sha256": modelDigest, "adapter": "test"},
		"total_races": 2,
		"total_stake": 20100,
		"bet_data":    []voting.RacePayload{payload1, payload2},
		"plan": []map[string]any{
			{"place": "新潟", "race_num": 1, "start_time": "10:30", "payload": payload2},
			{"place": "札幌", "race_num": 1, "start_time": "10:00", "payload": payload1},
		},
	}
	data, _ := json.Marshal(input)
	source := filepath.Join(temporary, "vote_plan.json")
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(temporary, "full_day.json")
	if err := prepareDay(config, []string{"--source", source, "--output", output}); err != nil {
		t.Fatal(err)
	}
	prepared, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var bundle voting.PlanBundle
	if err := json.Unmarshal(prepared, &bundle); err != nil {
		t.Fatal(err)
	}
	if err := voting.ValidateBundle(bundle, config.Policy); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Plans) != 2 || bundle.Plans[0].Payload.RaceID != payload1.RaceID || bundle.Plans[1].Payload.RaceID != payload2.RaceID {
		t.Fatalf("unexpected plan order: %+v", bundle.Plans)
	}
	if voting.PayloadStake(bundle.Plans[0].Payload)+voting.PayloadStake(bundle.Plans[1].Payload) != 20100 {
		t.Fatal("unexpected bundle stake")
	}
	if bundle.Plans[0].Provenance.ModelSHA256 == nil || *bundle.Plans[0].Provenance.ModelSHA256 != modelDigest {
		t.Fatal("approved model provenance was not pinned into the bundle")
	}
}

func TestPrepareDayVerifiesDeclaredSourceArtifactSHA(t *testing.T) {
	originalClock := currentTime
	currentTime = func() time.Time { return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalClock })
	repositoryRoot, err := voting.FindProjectRoot()
	if err != nil {
		t.Fatal(err)
	}
	config, err := voting.LoadRuntimeConfig(repositoryRoot, "configs/voting_policy_fullday.json")
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	config.Root = temporary
	config.StateDir = filepath.Join(temporary, "state")
	payload := voting.RacePayload{
		RaceID:  "202601010101",
		Mark:    map[string]int{"3": 1},
		BetList: []voting.Bet{{BetID: "b3_c0_2_5", Money: "100"}},
	}
	artifactPath := filepath.Join(temporary, "prediction.json")
	artifactBytes := []byte(`{"prediction_sha256":"prediction-artifact"}`)
	if err := os.WriteFile(artifactPath, artifactBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	artifactDigest := sha256.Sum256(artifactBytes)
	source := filepath.Join(temporary, "source.json")
	input := map[string]any{
		"date":          "2026-08-15",
		"source_path":   "prediction.json",
		"source_sha256": hex.EncodeToString(artifactDigest[:]),
		"total_races":   1,
		"total_stake":   100,
		"bet_data":      []voting.RacePayload{payload},
		"plan": []map[string]any{{
			"place": "札幌", "race_num": 1, "start_time": "10:00", "payload": payload,
		}},
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(temporary, "bundle.json")
	if err := prepareDay(config, []string{"--source", source, "--output", output}); err != nil {
		t.Fatal(err)
	}

	input["source_sha256"] = strings.Repeat("0", 64)
	data, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareDay(config, []string{"--source", source, "--output", filepath.Join(temporary, "mismatch.json")}); err == nil || !strings.Contains(err.Error(), "source_sha256") {
		t.Fatalf("expected source artifact hash rejection, got %v", err)
	}
}

func TestHashSourceArtifactAcceptsCanonicalFinalLiveSnapshotHash(t *testing.T) {
	temporary := t.TempDir()
	path := filepath.Join(temporary, "snapshot.json")
	material := map[string]any{
		"race_date": "2026-08-29",
		"status":    "FINAL_LIVE_FROZEN",
		"races":     []any{map[string]any{"race_id": "202608290101"}},
	}
	canonical, err := json.Marshal(material)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	expected := hex.EncodeToString(digest[:])
	material["snapshot_sha256"] = expected
	data, err := json.Marshal(material)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	actual, returned, err := hashSourceArtifact(path, expected)
	if err != nil {
		t.Fatalf("canonical snapshot hash rejected: %v", err)
	}
	if actual != expected || string(returned) != string(data) {
		t.Fatalf("unexpected source result: hash=%s bytes=%d", actual, len(returned))
	}

	material["races"] = []any{map[string]any{"race_id": "202608290102"}}
	tampered, err := json.Marshal(material)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := hashSourceArtifact(path, expected); err == nil {
		t.Fatal("tampered canonical snapshot was accepted")
	}
}

func TestPrepareDayRejectsSelfReferentialSourceSHA(t *testing.T) {
	originalClock := currentTime
	currentTime = func() time.Time { return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalClock })
	repositoryRoot, err := voting.FindProjectRoot()
	if err != nil {
		t.Fatal(err)
	}
	config, err := voting.LoadRuntimeConfig(repositoryRoot, "configs/voting_policy_fullday.json")
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	config.Root = temporary
	config.StateDir = filepath.Join(temporary, "state")
	payload := voting.RacePayload{
		RaceID:  "202601010101",
		Mark:    map[string]int{"3": 1},
		BetList: []voting.Bet{{BetID: "b3_c0_2_5", Money: "100"}},
	}
	source := filepath.Join(temporary, "source.json")
	input := map[string]any{
		"date":          "2026-08-15",
		"source_path":   "source.json",
		"source_sha256": strings.Repeat("a", 64),
		"total_races":   1,
		"total_stake":   100,
		"bet_data":      []voting.RacePayload{payload},
		"plan": []map[string]any{{
			"place": "札幌", "race_num": 1, "start_time": "10:00", "payload": payload,
		}},
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareDay(config, []string{"--source", source, "--output", filepath.Join(temporary, "bundle.json")}); err == nil || !strings.Contains(err.Error(), "self-reference") {
		t.Fatalf("expected self-reference rejection, got %v", err)
	}
}

func TestWriteAtomicPublishesImmutableOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.json")
	if err := writeAtomic(path, []byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(path, []byte("second\n")); err == nil {
		t.Fatal("existing output was replaced")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first\n" {
		t.Fatalf("immutable output changed to %q", data)
	}
}
