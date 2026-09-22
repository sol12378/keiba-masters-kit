package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sol12378/keiba-masters-kit/internal/voting"
)

const (
	testAdapterVersion = "votectl-prepare-test/v1"
	dayAdapterVersion  = "votectl-prepare-day/v1"
)

var currentTime = time.Now

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "votectl:", err)
		os.Exit(2)
	}
}

func run(arguments []string) error {
	global := flag.NewFlagSet("votectl", flag.ContinueOnError)
	rootFlag := global.String("project-root", "", "repository root; auto-detected when empty")
	policyFlag := global.String("policy", "configs/voting_policy.json", "voting policy path")
	if err := global.Parse(arguments); err != nil {
		return err
	}
	if global.NArg() == 0 {
		return usageError()
	}
	root := *rootFlag
	if root == "" {
		var err error
		root, err = voting.FindProjectRoot()
		if err != nil {
			return err
		}
	}
	config, err := voting.LoadRuntimeConfig(root, *policyFlag)
	if err != nil {
		return err
	}
	command := global.Arg(0)
	commandArguments := global.Args()[1:]
	switch command {
	case "validate-policy":
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"valid": true, "policy_id": config.Policy.PolicyID,
			"connection_date":    config.Policy.ConnectionDate,
			"max_daily_stake":    voting.MaxDailyStake(config.Policy),
			"automatic_recovery": config.Policy.AutomaticRecovery,
		})
	case "cancel-plan":
		set := flag.NewFlagSet(command, flag.ContinueOnError)
		planID := set.String("plan-id", "", "unposted plan ID")
		if err := set.Parse(commandArguments); err != nil {
			return err
		}
		if *planID == "" {
			return errors.New("--plan-id is required")
		}
		return callJSON(config.Socket, http.MethodPost, "/v1/cancel-unposted", map[string]string{"plan_id": *planID})
	case "prepare-test":
		return prepareTest(config, commandArguments)
	case "prepare-day":
		return prepareDay(config, commandArguments)
	case "import-plan":
		set := flag.NewFlagSet(command, flag.ContinueOnError)
		path := set.String("file", "", "v1 plan envelope")
		if err := set.Parse(commandArguments); err != nil {
			return err
		}
		if *path == "" {
			return errors.New("--file is required")
		}
		body, err := os.ReadFile(*path)
		if err != nil {
			return err
		}
		return callControl(config.Socket, http.MethodPost, "/v1/import-plan", body)
	case "import-day":
		set := flag.NewFlagSet(command, flag.ContinueOnError)
		path := set.String("file", "", "full-day plan bundle")
		if err := set.Parse(commandArguments); err != nil {
			return err
		}
		if *path == "" {
			return errors.New("--file is required")
		}
		body, err := os.ReadFile(*path)
		if err != nil {
			return err
		}
		return callControl(config.Socket, http.MethodPost, "/v1/import-day", body)
	case "arm-test":
		set := flag.NewFlagSet(command, flag.ContinueOnError)
		planID := set.String("plan-id", "", "plan ID")
		confirmation := set.String("confirm-sha256", "", "exact payload SHA-256")
		if err := set.Parse(commandArguments); err != nil {
			return err
		}
		if *planID == "" || *confirmation == "" {
			return errors.New("--plan-id and --confirm-sha256 are required")
		}
		return callJSON(config.Socket, http.MethodPost, "/v1/arm-test", map[string]string{"plan_id": *planID, "confirm_sha256": *confirmation})
	case "arm-day":
		set := flag.NewFlagSet(command, flag.ContinueOnError)
		path := set.String("file", "", "full-day plan bundle")
		confirmation := set.String("confirm-sha256", "", "exact bundle SHA-256")
		if err := set.Parse(commandArguments); err != nil {
			return err
		}
		if *path == "" || *confirmation == "" {
			return errors.New("--file and --confirm-sha256 are required")
		}
		body, err := os.ReadFile(*path)
		if err != nil {
			return err
		}
		var bundle voting.PlanBundle
		if err := json.Unmarshal(body, &bundle); err != nil {
			return err
		}
		return callJSON(config.Socket, http.MethodPost, "/v1/arm-day", map[string]any{"bundle": bundle, "confirm_sha256": *confirmation})
	case "auth-check":
		return callJSON(config.Socket, http.MethodPost, "/v1/auth-check", map[string]string{})
	case "check":
		set := flag.NewFlagSet(command, flag.ContinueOnError)
		raceID := set.String("race-id", "", "official 12-character race ID")
		if err := set.Parse(commandArguments); err != nil {
			return err
		}
		if *raceID == "" {
			return errors.New("--race-id is required")
		}
		return callJSON(config.Socket, http.MethodPost, "/v1/check", map[string]string{"race_id": *raceID})
	case "status":
		return callControl(config.Socket, http.MethodGet, "/v1/status", nil)
	case "kill":
		set := flag.NewFlagSet(command, flag.ContinueOnError)
		reason := set.String("reason", "", "operator-visible stop reason")
		if err := set.Parse(commandArguments); err != nil {
			return err
		}
		if strings.TrimSpace(*reason) == "" {
			return errors.New("--reason is required")
		}
		return callJSON(config.Socket, http.MethodPost, "/v1/kill", map[string]string{"reason": *reason})
	default:
		return usageError()
	}
}

func usageError() error {
	return errors.New("usage: votectl [--project-root DIR] [--policy FILE] <validate-policy|cancel-plan|prepare-test|prepare-day|import-plan|import-day|arm-test|arm-day|auth-check|check|status|kill> [options]")
}

func prepareTest(config voting.RuntimeConfig, arguments []string) error {
	set := flag.NewFlagSet("prepare-test", flag.ContinueOnError)
	source := set.String("source", "", "existing Python vote_plan JSON")
	index := set.Int("index", -1, "zero-based race index")
	postTimeText := set.String("post-time", "", "scheduled post time as RFC3339 with timezone")
	output := set.String("output", "", "v1 plan output; defaults under outputs/voting-server/plans")
	if err := set.Parse(arguments); err != nil {
		return err
	}
	if *source == "" || *index < 0 || *postTimeText == "" {
		return errors.New("--source, --index >= 0, and --post-time are required")
	}
	sourceBytes, err := os.ReadFile(*source)
	if err != nil {
		return err
	}
	var sourcePlan struct {
		BetData []voting.RacePayload `json:"bet_data"`
	}
	if err := json.Unmarshal(sourceBytes, &sourcePlan); err != nil {
		return fmt.Errorf("decode source plan: %w", err)
	}
	if *index >= len(sourcePlan.BetData) {
		return fmt.Errorf("index %d outside source bet_data range 0..%d", *index, len(sourcePlan.BetData)-1)
	}
	payload := sourcePlan.BetData[*index]
	if len(payload.BetList) != 1 {
		return errors.New("test plan requires a source race with exactly one bet")
	}
	payload.BetList[0].Money = strconv.Itoa(voting.MaxStakePerRace(config.Policy))
	if err := voting.ValidateTestPayload(payload, config.Policy); err != nil {
		return err
	}
	postTime, err := time.Parse(time.RFC3339, *postTimeText)
	if err != nil {
		return fmt.Errorf("parse --post-time: %w", err)
	}
	location, err := time.LoadLocation(config.Policy.Timezone)
	if err != nil {
		return err
	}
	if postTime.In(location).Format("2006-01-02") != config.Policy.ConnectionDate {
		return errors.New("post-time date does not match policy connection_date")
	}
	payloadHash, err := voting.PayloadSHA256(payload)
	if err != nil {
		return err
	}
	sourceHash := sha256.Sum256(sourceBytes)
	planID := fmt.Sprintf("test-%s-%s-%s", strings.ReplaceAll(config.Policy.ConnectionDate, "-", ""), payload.RaceID, payloadHash[:12])
	plan := voting.PlanEnvelope{
		SchemaVersion:      1,
		PlanID:             planID,
		PolicyID:           config.Policy.PolicyID,
		RaceDate:           config.Policy.ConnectionDate,
		CreatedAt:          currentTime().UTC(),
		ScheduledPostTime:  postTime,
		TargetSubmitTime:   postTime.Add(-time.Duration(config.Policy.Timing.TargetSubmitSecondsBeforePost) * time.Second),
		HardSubmitDeadline: postTime.Add(-time.Duration(config.Policy.Timing.HardCutoffSecondsBeforePost) * time.Second),
		Payload:            payload,
		PayloadSHA256:      payloadHash,
		Provenance: voting.Provenance{
			ForecastPath:   *source,
			ForecastSHA256: hex.EncodeToString(sourceHash[:]),
			AdapterVersion: testAdapterVersion,
		},
	}
	if err := voting.ValidatePlan(plan, config.Policy); err != nil {
		return err
	}
	outputPath := *output
	if outputPath == "" {
		outputPath = filepath.Join(config.StateDir, "plans", planID+".json")
	} else if !filepath.IsAbs(outputPath) {
		outputPath = filepath.Join(config.Root, outputPath)
	}
	if err := requireWithinRoot(config.Root, outputPath); err != nil {
		return err
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(outputPath, append(data, '\n')); err != nil {
		return err
	}
	fmt.Printf("prepared=%s\nplan_id=%s\nrace_id=%s\npayload_sha256=%s\ntarget_submit_time=%s\nhard_submit_deadline=%s\n", outputPath, planID, payload.RaceID, payloadHash, plan.TargetSubmitTime.Format(time.RFC3339), plan.HardSubmitDeadline.Format(time.RFC3339))
	return nil
}

type pythonPlanRow struct {
	Place     string             `json:"place"`
	RaceNum   int                `json:"race_num"`
	StartTime string             `json:"start_time"`
	Payload   voting.RacePayload `json:"payload"`
}

type pythonVotePlan struct {
	Date       string               `json:"date"`
	Model      *pythonModel         `json:"model"`
	TotalRaces int                  `json:"total_races"`
	TotalStake int                  `json:"total_stake"`
	CreatedAt  string               `json:"created_at"`
	SourcePath string               `json:"source_path"`
	SourceSHA  string               `json:"source_sha256"`
	InputSHA   string               `json:"input_sha256"`
	BetData    []voting.RacePayload `json:"bet_data"`
	Plan       []pythonPlanRow      `json:"plan"`
}

type pythonModel struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Adapter string `json:"adapter"`
}

type rehearsalPrediction struct {
	Kind                string `json:"kind"`
	TargetDate          string `json:"target_date"`
	SubmissionPerformed bool   `json:"submission_performed"`
	Model               struct {
		SHA256 string `json:"sha256"`
	} `json:"model"`
	Inference struct {
		SelectedHorse int    `json:"selected_horse"`
		Selection     string `json:"selection"`
	} `json:"inference"`
}

func prepareDay(config voting.RuntimeConfig, arguments []string) error {
	set := flag.NewFlagSet("prepare-day", flag.ContinueOnError)
	source := set.String("source", "", "existing Python vote_plan JSON with all daily races")
	output := set.String("output", "", "full-day bundle output; defaults under the full-day state directory")
	if err := set.Parse(arguments); err != nil {
		return err
	}
	if *source == "" {
		return errors.New("--source is required")
	}
	sourceBytes, err := os.ReadFile(*source)
	if err != nil {
		return err
	}
	var sourcePlan pythonVotePlan
	if err := json.Unmarshal(sourceBytes, &sourcePlan); err != nil {
		return fmt.Errorf("decode source plan: %w", err)
	}
	if sourcePlan.Date != config.Policy.ConnectionDate {
		return errors.New("source plan date does not match policy connection_date")
	}
	if len(sourcePlan.Plan) == 0 || len(sourcePlan.Plan) != len(sourcePlan.BetData) || sourcePlan.TotalRaces != len(sourcePlan.Plan) {
		return errors.New("source plan counts or plan/bet_data alignment are invalid")
	}
	if len(sourcePlan.Plan) > config.Policy.Submission.MaxRaces {
		return fmt.Errorf("source has %d races, policy permits %d", len(sourcePlan.Plan), config.Policy.Submission.MaxRaces)
	}
	location, err := time.LoadLocation(config.Policy.Timezone)
	if err != nil {
		return err
	}
	createdAt := currentTime().UTC()
	if sourcePlan.CreatedAt != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, sourcePlan.CreatedAt)
		if parseErr != nil {
			return fmt.Errorf("source created_at: %w", parseErr)
		}
		createdAt = parsed.UTC()
	}
	sourceHash := sha256.Sum256(sourceBytes)
	sourceDigest := hex.EncodeToString(sourceHash[:])
	var referencedSourceBytes []byte
	gate, modelRequired := voting.EffectiveModelGate(config.Policy)
	if modelRequired && gate.ApprovalStatus == voting.RehearsalModelApprovalStatus && sourcePlan.SourceSHA == "" {
		return errors.New("required rehearsal model source must include source_path and source_sha256")
	}
	if sourcePlan.SourceSHA != "" {
		if !votingSHA256Pattern(sourcePlan.SourceSHA) {
			return errors.New("source_sha256 must be lowercase SHA-256")
		}
		// source_sha256 is provenance for source_path (the capture or model
		// prediction manifest), not a hash of this wrapper. The wrapper itself
		// contains source_sha256, so accepting sourceBytes here would create an
		// impossible self-referential digest and would reject real model plans.
		if strings.TrimSpace(sourcePlan.SourcePath) == "" {
			return errors.New("source_sha256 requires source_path")
		}
		referencedPath := sourcePlan.SourcePath
		if !filepath.IsAbs(referencedPath) {
			referencedPath = filepath.Join(config.Root, referencedPath)
		}
		if err := requireWithinRoot(config.Root, referencedPath); err != nil {
			return fmt.Errorf("source path: %w", err)
		}
		sourceAbsolute, err := filepath.Abs(*source)
		if err != nil {
			return err
		}
		referencedAbsolute, err := filepath.Abs(referencedPath)
		if err != nil {
			return err
		}
		if filepath.Clean(sourceAbsolute) == filepath.Clean(referencedAbsolute) {
			return errors.New("source_sha256 cannot self-reference the source JSON wrapper")
		}
		// A FINAL-LIVE snapshot's source_sha256 is the canonical hash of the
		// snapshot with its self-hash field removed, whereas a capture/prediction
		// artifact normally uses the raw file SHA-256.  Validate both forms.  The
		// Python adapter has already checked the original snapshot before creating
		// this wrapper; therefore a competition (non-rehearsal) bundle may carry
		// the canonical provenance even when the original source file is not
		// copied into the temporary staging root.  A rehearsal gate still requires
		// the source bytes because it inspects the prediction envelope below.
		needsReferencedSource := modelRequired && gate.ApprovalStatus == voting.RehearsalModelApprovalStatus
		referencedDigest, referencedBytes, sourceErr := hashSourceArtifact(referencedAbsolute, sourcePlan.SourceSHA)
		if sourceErr != nil {
			if !needsReferencedSource && sourcePlan.InputSHA == sourcePlan.SourceSHA && errors.Is(sourceErr, os.ErrNotExist) {
				// The source wrapper binds the canonical snapshot hash through
				// input_sha256.  Do not treat this as an official rehearsal source;
				// this path exists for submission-free local transforms where the
				// adapter's immutable input was deliberately not copied alongside
				// the wrapper.
				sourceDigest = sourcePlan.SourceSHA
			} else {
				return fmt.Errorf("source artifact: %w", sourceErr)
			}
		} else {
			sourceDigest = referencedDigest
			referencedSourceBytes = referencedBytes
		}
	}
	forecastPath := *source
	if sourcePlan.SourcePath != "" {
		forecastPath = sourcePlan.SourcePath
	}
	var modelDigest *string
	if sourcePlan.Model != nil {
		if !votingSHA256Pattern(sourcePlan.Model.SHA256) || strings.TrimSpace(sourcePlan.Model.Path) == "" {
			return errors.New("source model provenance is incomplete")
		}
		modelPath := sourcePlan.Model.Path
		if !filepath.IsAbs(modelPath) {
			modelPath = filepath.Join(config.Root, modelPath)
		}
		if err := requireWithinRoot(config.Root, modelPath); err != nil {
			return fmt.Errorf("source model path: %w", err)
		}
		actualDigest, err := hashRegularSource(modelPath)
		if err != nil {
			return fmt.Errorf("read source model: %w", err)
		}
		if actualDigest != sourcePlan.Model.SHA256 {
			return errors.New("source model SHA-256 does not match the model file")
		}
		modelDigest = &actualDigest
	}
	if modelRequired {
		if modelDigest == nil || *modelDigest != gate.ModelSHA256 {
			return errors.New("source vote plan does not use the approved model")
		}
	}
	if modelRequired && gate.ApprovalStatus == voting.RehearsalModelApprovalStatus {
		var prediction rehearsalPrediction
		if err := json.Unmarshal(referencedSourceBytes, &prediction); err != nil {
			return fmt.Errorf("decode rehearsal model prediction: %w", err)
		}
		if prediction.Kind != "weekend_live_final_live_prediction" ||
			prediction.TargetDate != strings.ReplaceAll(config.Policy.ConnectionDate, "-", "") ||
			prediction.SubmissionPerformed || prediction.Model.SHA256 != gate.ModelSHA256 ||
			prediction.Inference.Selection != "FINAL_LIVE_P1_ARGMAX_WIN_100" ||
			prediction.Inference.SelectedHorse < 1 || prediction.Inference.SelectedHorse > 18 {
			return errors.New("rehearsal model prediction does not match the required gate")
		}
		if len(sourcePlan.Plan) != 1 || len(sourcePlan.BetData) != 1 {
			return errors.New("rehearsal model source must contain exactly one race")
		}
		payload := sourcePlan.Plan[0].Payload
		expectedBetID := fmt.Sprintf("b1_c0_%d", prediction.Inference.SelectedHorse)
		if len(payload.BetList) != 1 || payload.BetList[0].BetID != expectedBetID ||
			payload.BetList[0].Money != "100" || payload.Mark[strconv.Itoa(prediction.Inference.SelectedHorse)] != 1 {
			return errors.New("rehearsal payload does not match the model-selected horse")
		}
	}
	betDataHashes := make(map[string]string, len(sourcePlan.BetData))
	for _, payload := range sourcePlan.BetData {
		digest, hashErr := voting.PayloadSHA256(payload)
		if hashErr != nil {
			return fmt.Errorf("source bet_data race %s: %w", payload.RaceID, hashErr)
		}
		if _, exists := betDataHashes[payload.RaceID]; exists {
			return fmt.Errorf("duplicate source race_id %s", payload.RaceID)
		}
		betDataHashes[payload.RaceID] = digest
	}
	plans := make([]voting.PlanEnvelope, 0, len(sourcePlan.Plan))
	computedStake := 0
	for index, row := range sourcePlan.Plan {
		if err := voting.ValidateTestPayload(row.Payload, config.Policy); err != nil {
			return fmt.Errorf("plan[%d] %s: %w", index, row.Payload.RaceID, err)
		}
		payloadHash, err := voting.PayloadSHA256(row.Payload)
		if err != nil {
			return err
		}
		if betDataHashes[row.Payload.RaceID] != payloadHash {
			return fmt.Errorf("plan[%d] payload does not exactly match bet_data", index)
		}
		postTime, err := parseDailyPostTime(config.Policy.ConnectionDate, row.StartTime, location)
		if err != nil {
			return fmt.Errorf("plan[%d] start_time: %w", index, err)
		}
		// Python's FINAL-LIVE adapter emits canonical RFC3339 timestamps in UTC
		// (with a trailing Z).  Keep the Go envelope byte-for-byte identical;
		// BundleSHA256 alone is not sufficient because reconciliation compares
		// the complete PlanEnvelope.
		postTime = postTime.UTC()
		planID := fmt.Sprintf("day-%s-%s-%s", strings.ReplaceAll(config.Policy.ConnectionDate, "-", ""), row.Payload.RaceID, payloadHash[:12])
		plan := voting.PlanEnvelope{
			SchemaVersion:      1,
			PlanID:             planID,
			PolicyID:           config.Policy.PolicyID,
			RaceDate:           config.Policy.ConnectionDate,
			CreatedAt:          createdAt,
			ScheduledPostTime:  postTime,
			TargetSubmitTime:   postTime.Add(-time.Duration(config.Policy.Timing.TargetSubmitSecondsBeforePost) * time.Second),
			HardSubmitDeadline: postTime.Add(-time.Duration(config.Policy.Timing.HardCutoffSecondsBeforePost) * time.Second),
			Payload:            row.Payload,
			PayloadSHA256:      payloadHash,
			Provenance: voting.Provenance{
				ForecastPath:   forecastPath,
				ForecastSHA256: sourceDigest,
				ModelSHA256:    modelDigest,
				AdapterVersion: dayAdapterVersion,
			},
		}
		if err := voting.ValidatePlan(plan, config.Policy); err != nil {
			return fmt.Errorf("plan[%d]: %w", index, err)
		}
		computedStake += voting.PayloadStake(row.Payload)
		plans = append(plans, plan)
	}
	if computedStake != sourcePlan.TotalStake {
		return fmt.Errorf("source total_stake=%d but payloads total %d", sourcePlan.TotalStake, computedStake)
	}
	if computedStake > voting.MaxDailyStake(config.Policy) {
		return fmt.Errorf("daily stake %d exceeds policy maximum %d", computedStake, voting.MaxDailyStake(config.Policy))
	}
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].TargetSubmitTime.Equal(plans[j].TargetSubmitTime) {
			return plans[i].Payload.RaceID < plans[j].Payload.RaceID
		}
		return plans[i].TargetSubmitTime.Before(plans[j].TargetSubmitTime)
	})
	bundle := voting.PlanBundle{
		SchemaVersion: 1,
		PolicyID:      config.Policy.PolicyID,
		RaceDate:      config.Policy.ConnectionDate,
		CreatedAt:     createdAt,
		Plans:         plans,
		SourcePath:    forecastPath,
		SourceSHA256:  sourceDigest,
	}
	bundleHash, err := voting.BundleSHA256(bundle)
	if err != nil {
		return err
	}
	bundle.BundleSHA256 = bundleHash
	bundle.BundleID = fmt.Sprintf("full-day-%s-%s", strings.ReplaceAll(config.Policy.ConnectionDate, "-", ""), bundleHash[:12])
	if err := voting.ValidateBundle(bundle, config.Policy); err != nil {
		return err
	}
	outputPath := *output
	if outputPath == "" {
		outputPath = filepath.Join(config.StateDir, "plans", bundle.BundleID+".json")
	} else if !filepath.IsAbs(outputPath) {
		outputPath = filepath.Join(config.Root, outputPath)
	}
	if err := requireWithinRoot(config.Root, outputPath); err != nil {
		return err
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(outputPath, append(data, '\n')); err != nil {
		return err
	}
	fmt.Printf("prepared=%s\nbundle_id=%s\nbundle_sha256=%s\nraces=%d\ntotal_stake=%d\n", outputPath, bundle.BundleID, bundle.BundleSHA256, len(bundle.Plans), computedStake)
	for _, plan := range bundle.Plans {
		fmt.Printf("race_id=%s post=%s target=%s hard_cutoff=%s stake=%d payload_sha256=%s\n",
			plan.Payload.RaceID,
			plan.ScheduledPostTime.In(location).Format(time.RFC3339),
			plan.TargetSubmitTime.In(location).Format(time.RFC3339),
			plan.HardSubmitDeadline.In(location).Format(time.RFC3339),
			voting.PayloadStake(plan.Payload),
			plan.PayloadSHA256,
		)
	}
	return nil
}

func votingSHA256Pattern(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func hashRegularSource(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("source artifact must be a regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// hashSourceArtifact validates the two source hash contracts used by the
// submission-free adapter pipeline:
//
//   - ordinary capture/prediction files are bound to their raw file SHA-256;
//   - FINAL-LIVE snapshots carry snapshot_sha256, which is the SHA-256 of the
//     canonical JSON object after removing that self-hash field.
//
// Returning the bytes also lets the date-scoped rehearsal gate inspect the
// prediction envelope after the same integrity check.  No path is resolved
// here; callers must apply requireWithinRoot before invoking this helper.
func hashSourceArtifact(path, expected string) (string, []byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", nil, errors.New("source artifact must be a regular non-symlink file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	rawDigest := sha256.Sum256(data)
	raw := hex.EncodeToString(rawDigest[:])
	if raw == expected {
		return raw, data, nil
	}

	// Do not accept arbitrary self-declared fields.  Only the explicit
	// snapshot_sha256 contract is recognized, and the canonical digest is
	// recomputed after removing that field.  json.Marshal emits compact JSON
	// with deterministic lexicographic map-key ordering, matching the Python
	// adapter's canonical_bytes contract for this schema.
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return raw, nil, fmt.Errorf("source_sha256 does not match the referenced source artifact")
	}
	declaredRaw, ok := object["snapshot_sha256"]
	var declared string
	if !ok || json.Unmarshal(declaredRaw, &declared) != nil || declared != expected {
		return raw, nil, fmt.Errorf("source_sha256 does not match the referenced source artifact")
	}
	delete(object, "snapshot_sha256")
	canonical, err := json.Marshal(object)
	if err != nil {
		return raw, nil, fmt.Errorf("canonicalize source artifact: %w", err)
	}
	canonicalDigest := sha256.Sum256(canonical)
	canonicalHash := hex.EncodeToString(canonicalDigest[:])
	if canonicalHash != expected {
		return raw, nil, fmt.Errorf("source_sha256 does not match the referenced source artifact")
	}
	return canonicalHash, data, nil
}

func parseDailyPostTime(raceDate, value string, location *time.Location) (time.Time, error) {
	for _, layout := range []string{"15:04", "15:04:05"} {
		parsed, err := time.ParseInLocation("2006-01-02 "+layout, raceDate+" "+value, location)
		if err == nil {
			return parsed, nil
		}
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	return time.Time{}, errors.New("must be HH:MM, HH:MM:SS, or RFC3339")
}

func callJSON(socket, method, path string, input any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return callControl(socket, method, path, body)
}

func callControl(socket, method, path string, body []byte) error {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		dialer := net.Dialer{Timeout: 2 * time.Second}
		return dialer.DialContext(ctx, "unix", socket)
	}}
	client := &http.Client{Transport: transport, Timeout: 35 * time.Second}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequest(method, "http://unix"+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("connect to votingd control socket: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	var decoded any
	if json.Unmarshal(responseBody, &decoded) == nil {
		pretty, _ := json.MarshalIndent(decoded, "", "  ")
		fmt.Println(string(pretty))
	} else {
		fmt.Println(string(responseBody))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("votingd returned HTTP %d", response.StatusCode)
	}
	return nil
}

func requireWithinRoot(root, path string) error {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(absoluteRoot, absolutePath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("output path must remain inside the project root")
	}
	if info, statErr := os.Lstat(absoluteRoot); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("project root must not be a symlink")
	}
	cursor := absoluteRoot
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		cursor = filepath.Join(cursor, component)
		info, statErr := os.Lstat(cursor)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("output path contains a symlink")
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".plan-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("immutable output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
