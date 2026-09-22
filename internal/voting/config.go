package voting

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type ModelGate struct {
	Required       bool   `json:"required"`
	ApprovalStatus string `json:"approval_status"`
	ApprovalScope  string `json:"approval_scope"`
	TargetDate     string `json:"target_date"`
	GatePath       string `json:"gate_path"`
	GateSHA256     string `json:"gate_sha256"`
	ModelPath      string `json:"model_path"`
	ModelSHA256    string `json:"model_sha256"`
	AdapterPath    string `json:"adapter_path"`
	AdapterSHA256  string `json:"adapter_sha256"`
	ValidationPath string `json:"validation_path"`
	ValidationSHA  string `json:"validation_sha256"`
	ApprovalID     string `json:"approval_id"`

	// The generated V4 policy originally exposed the rehearsal gate under
	// rehearsal_model_gate with the shorter status/path/scope names. Keep
	// these aliases so an old immutable policy can be decoded safely while
	// all validation below uses the canonical fields above.
	Status string `json:"status,omitempty"`
	Scope  string `json:"scope,omitempty"`
	Path   string `json:"path,omitempty"`
}

type Policy struct {
	SchemaVersion         int    `json:"schema_version"`
	PolicyID              string `json:"policy_id"`
	Status                string `json:"status"`
	Timezone              string `json:"timezone"`
	ConnectionDate        string `json:"connection_date"`
	CompetitionPointsOnly bool   `json:"competition_points_only"`
	AutomaticRecovery     bool   `json:"automatic_recovery,omitempty"`
	Runtime               struct {
		Language             string `json:"language"`
		Version              string `json:"version"`
		GoModVersion         string `json:"go_mod_version"`
		Toolchain            string `json:"toolchain"`
		StandardLibraryFirst bool   `json:"standard_library_first"`
	} `json:"runtime"`
	Interfaces struct {
		StatusListen      string `json:"status_listen"`
		AllowNonLoopback  bool   `json:"allow_non_loopback"`
		ControlSocket     string `json:"control_socket"`
		ControlSocketMode string `json:"control_socket_mode"`
		StateDirectory    string `json:"state_directory"`
		DirectoryMode     string `json:"directory_mode"`
		FileMode          string `json:"file_mode"`
	} `json:"interfaces"`
	Modes struct {
		Default           string   `json:"default"`
		Allowed           []string `json:"allowed"`
		ProductionEnabled bool     `json:"production_enabled"`
	} `json:"modes"`
	Planner json.RawMessage `json:"planner"`
	Timing  struct {
		PlanFreezeSecondsBeforePost   int `json:"plan_freeze_seconds_before_post"`
		TargetSubmitSecondsBeforePost int `json:"target_submit_seconds_before_post"`
		HardCutoffSecondsBeforePost   int `json:"hard_cutoff_seconds_before_post"`
		OfficialCutoffSecondsBefore   int `json:"official_cutoff_seconds_before_post"`
		MaximumClockSkewSeconds       int `json:"maximum_clock_skew_seconds"`
		VerificationInitialWait       int `json:"verification_initial_wait_seconds"`
		VerificationInterval          int `json:"verification_interval_seconds"`
		VerificationMaxGETAttempts    int `json:"verification_max_get_attempts"`
		ShutdownGraceSeconds          int `json:"shutdown_grace_seconds"`
	} `json:"timing"`
	HTTP struct {
		ConnectTimeoutSeconds                   int   `json:"connect_timeout_seconds"`
		TLSHandshakeTimeoutSeconds              int   `json:"tls_handshake_timeout_seconds"`
		ResponseHeaderTimeoutSeconds            int   `json:"response_header_timeout_seconds"`
		LoginGETRequestTimeoutSeconds           int   `json:"login_get_request_timeout_seconds"`
		POSTRequestTimeoutSeconds               int   `json:"post_request_timeout_seconds"`
		MaxResponseBodyBytes                    int64 `json:"max_response_body_bytes"`
		TLSVerificationRequired                 bool  `json:"tls_verification_required"`
		LoginGETMaxAttempts                     int   `json:"login_get_max_attempts"`
		LoginGETRetryMinimumCutoffMarginSeconds int   `json:"login_get_retry_minimum_cutoff_margin_seconds"`
		POSTMaxAttempts                         int   `json:"post_max_attempts"`
	} `json:"http"`
	Token struct {
		OfficialLifetimeSeconds    int  `json:"official_lifetime_seconds"`
		LocalUsableLifetimeSeconds int  `json:"local_usable_lifetime_seconds"`
		Persist                    bool `json:"persist"`
		Log                        bool `json:"log"`
	} `json:"token"`
	ModelGate          ModelGate `json:"model_gate"`
	RehearsalModelGate ModelGate `json:"rehearsal_model_gate"`
	Submission         struct {
		Enabled                    bool              `json:"enabled"`
		RequiredEnvironment        map[string]string `json:"required_environment"`
		MaxArms                    int               `json:"max_arms"`
		MaxPOSTRequests            int               `json:"max_post_requests"`
		MaxRaces                   int               `json:"max_races"`
		MaxRacesPerRequest         int               `json:"max_races_per_request"`
		MaxBetsPerRace             int               `json:"max_bets_per_race"`
		MaxTotalStake              int               `json:"max_total_stake"`
		MaxStakePerRace            int               `json:"max_stake_per_race"`
		MaxDailyTotalStake         int               `json:"max_daily_total_stake"`
		StakeUnit                  int               `json:"stake_unit"`
		MaxExpandedBetPoints       int               `json:"max_expanded_bet_points"`
		AllowedBetIDPatterns       []string          `json:"allowed_bet_id_patterns"`
		RequireSortedCombinations  bool              `json:"require_sorted_combinations"`
		RequireEmptyPrecheck       bool              `json:"require_empty_precheck"`
		AllowOverwrite             bool              `json:"allow_overwrite"`
		AutomaticPOSTRetry         bool              `json:"automatic_post_retry"`
		RequireExactReconciliation bool              `json:"require_exact_get_reconciliation"`
		KillSwitch                 string            `json:"kill_switch"`
	} `json:"submission"`
	Concurrency struct {
		GlobalMaxInFlightPOSTs      int  `json:"global_max_in_flight_posts"`
		SerializeByRaceID           bool `json:"serialize_by_race_id"`
		PersistPostingBeforeNetwork bool `json:"persist_posting_before_network"`
		RestartFromPostingIsGETOnly bool `json:"restart_from_posting_is_get_only"`
	} `json:"concurrency"`
	Storage struct {
		Journal                   string `json:"journal"`
		Snapshot                  string `json:"snapshot"`
		JournalFsyncEachEvent     bool   `json:"journal_fsync_each_event"`
		SnapshotAtomicRename      bool   `json:"snapshot_atomic_rename"`
		SingleWriter              bool   `json:"single_writer"`
		RetentionDaysAfterContest int    `json:"retention_days_after_competition"`
		AutomaticDeletion         bool   `json:"automatic_deletion"`
	} `json:"storage"`
	RedactedFields []string `json:"redacted_fields"`
	StopConditions []string `json:"stop_conditions"`
}

type RuntimeConfig struct {
	Policy     Policy
	Root       string
	PolicyPath string
	StateDir   string
	Journal    string
	Snapshot   string
	Socket     string
	KillSwitch string
}

func LoadRuntimeConfig(root, policyPath string) (RuntimeConfig, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return RuntimeConfig{}, err
	}
	resolvedPolicy := resolve(root, policyPath)
	if err := validatePathNoSymlink(absoluteRoot, resolvedPolicy); err != nil {
		return RuntimeConfig{}, fmt.Errorf("policy path: %w", err)
	}
	file, err := os.Open(resolvedPolicy)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("open policy: %w", err)
	}
	defer file.Close()
	var policy Policy
	if err := json.NewDecoder(file).Decode(&policy); err != nil {
		return RuntimeConfig{}, fmt.Errorf("decode policy: %w", err)
	}
	policy = normalizePolicyGates(policy)
	if err := ValidatePolicy(policy); err != nil {
		return RuntimeConfig{}, err
	}
	config := RuntimeConfig{
		Policy:     policy,
		Root:       absoluteRoot,
		PolicyPath: resolvedPolicy,
		StateDir:   resolve(absoluteRoot, policy.Interfaces.StateDirectory),
		Journal:    resolve(absoluteRoot, policy.Storage.Journal),
		Snapshot:   resolve(absoluteRoot, policy.Storage.Snapshot),
		Socket:     resolve(absoluteRoot, policy.Interfaces.ControlSocket),
		KillSwitch: resolve(absoluteRoot, policy.Submission.KillSwitch),
	}
	for label, path := range map[string]string{
		"state directory": config.StateDir,
		"journal":         config.Journal,
		"snapshot":        config.Snapshot,
		"control socket":  config.Socket,
		"kill switch":     config.KillSwitch,
	} {
		if err := validatePathNoSymlink(absoluteRoot, path); err != nil {
			return RuntimeConfig{}, fmt.Errorf("%s path: %w", label, err)
		}
	}
	if gate, required := EffectiveModelGate(config.Policy); required {
		if err := validateRequiredGateArtifacts(config.Root, gate); err != nil {
			return RuntimeConfig{}, err
		}
	}
	return config, nil
}

func ValidatePolicy(policy Policy) error {
	policy = normalizePolicyGates(policy)
	if policy.SchemaVersion != 1 || policy.PolicyID == "" || policy.Status != "frozen_for_implementation" {
		return errors.New("unsupported or unfrozen voting policy")
	}
	if policy.Runtime.Language != "go" || policy.Runtime.Toolchain != "go1.26.6" {
		return errors.New("voting policy must pin toolchain go1.26.6")
	}
	if !policy.CompetitionPointsOnly || policy.Modes.ProductionEnabled {
		return errors.New("v1 must remain competition-points-only with production disabled")
	}
	host, port, err := net.SplitHostPort(policy.Interfaces.StatusListen)
	if err != nil || port == "" || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || policy.Interfaces.AllowNonLoopback {
		return errors.New("status_listen must be a numeric loopback address")
	}
	if !policy.Submission.Enabled || policy.Submission.MaxArms < 1 || policy.Submission.MaxArms > 36 ||
		policy.Submission.MaxPOSTRequests < 1 || policy.Submission.MaxPOSTRequests > 36 ||
		policy.Submission.MaxRaces < 1 || policy.Submission.MaxRaces > 36 ||
		policy.Submission.MaxRacesPerRequest != 1 || policy.HTTP.POSTMaxAttempts != 1 {
		return errors.New("policy must limit arms, races, and POST requests to 1..36 and each request/attempt to one")
	}
	if policy.Submission.MaxPOSTRequests < policy.Submission.MaxRaces || policy.Submission.MaxArms < policy.Submission.MaxRaces {
		return errors.New("policy arm and POST limits must cover max_races")
	}
	if policy.Submission.StakeUnit != 100 || policy.Submission.MaxBetsPerRace < 1 || policy.Submission.MaxBetsPerRace > 200 {
		return errors.New("v1 policy must limit each race to 1..200 100-point-unit bets")
	}
	if policy.Submission.MaxExpandedBetPoints < policy.Submission.MaxBetsPerRace || policy.Submission.MaxExpandedBetPoints > 200 {
		return errors.New("max_expanded_bet_points must cover max_bets_per_race and not exceed 200")
	}
	if !policy.Submission.RequireSortedCombinations {
		return errors.New("policy must require canonical combination ordering")
	}
	if len(policy.Submission.AllowedBetIDPatterns) == 0 {
		return errors.New("allowed_bet_id_patterns must not be empty")
	}
	for _, pattern := range policy.Submission.AllowedBetIDPatterns {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("invalid allowed_bet_id_pattern %q: %w", pattern, err)
		}
	}
	if policy.Submission.MaxBetsPerRace > 1 {
		representatives := []string{"b1_c0_1", "b2_c0_1", "b3_c0_1_1", "b4_c0_1_2", "b5_c0_1_2", "b6_c0_1_2", "b7_c0_1_2_3", "b8_c0_1_2_3"}
		for _, betID := range representatives {
			if !matchesAnyPattern(betID, policy.Submission.AllowedBetIDPatterns) {
				return fmt.Errorf("multi-bet policy must allow every official pool; missing %s", betID[:2])
			}
		}
	}
	// The authorized 2026-09-19/20 all-in policy uses 31 tickets plus a
	// 100-point class-A boost and a one-off 2,200-point final-race remainder:
	// 33,300 points.  Keep that exact ceiling as the runtime safety boundary.
	maxRace, maxDay := 33300, 1000000
	if policy.PolicyID == "COMPETITION-2026-DYNAMIC-20260920" && policy.ConnectionDate == "2026-09-20" {
		// Explicitly authorized full virtual-point balance; does not recycle
		// winnings beyond the frozen opening-bank turnover allowance.
		maxRace, maxDay = 1268500, 1268500
	} else if policy.AutomaticRecovery {
		return errors.New("automatic recovery requires the date-scoped dynamic policy")
	}
	if MaxStakePerRace(policy) < 100 || MaxStakePerRace(policy) > maxRace || MaxStakePerRace(policy)%100 != 0 {
		return errors.New("max stake per race must be a 100-point unit in 100..33300")
	}
	if MaxDailyStake(policy) < MaxStakePerRace(policy) || MaxDailyStake(policy) > maxDay {
		return errors.New("daily stake limit must cover one race and not exceed initial points")
	}
	if policy.Submission.AllowOverwrite || policy.Submission.AutomaticPOSTRetry || !policy.Submission.RequireEmptyPrecheck || !policy.Submission.RequireExactReconciliation {
		return errors.New("test policy overwrite/retry/precheck/reconciliation invariants failed")
	}
	if policy.Concurrency.GlobalMaxInFlightPOSTs != 1 || !policy.Concurrency.SerializeByRaceID ||
		!policy.Concurrency.PersistPostingBeforeNetwork || !policy.Concurrency.RestartFromPostingIsGETOnly {
		return errors.New("test policy concurrency invariants failed")
	}
	if policy.Timing.TargetSubmitSecondsBeforePost <= policy.Timing.HardCutoffSecondsBeforePost ||
		policy.Timing.HardCutoffSecondsBeforePost <= policy.Timing.OfficialCutoffSecondsBefore ||
		policy.Timing.VerificationInitialWait < 1 || policy.Timing.VerificationMaxGETAttempts < 1 {
		return errors.New("invalid timing policy")
	}
	if !policy.Storage.JournalFsyncEachEvent || !policy.Storage.SnapshotAtomicRename || !policy.Storage.SingleWriter || policy.Storage.AutomaticDeletion {
		return errors.New("invalid fail-closed storage policy")
	}
	if policy.Token.OfficialLifetimeSeconds != 300 || policy.Token.LocalUsableLifetimeSeconds < 1 ||
		policy.Token.LocalUsableLifetimeSeconds > 240 || policy.Token.Persist || policy.Token.Log {
		return errors.New("token policy must remain memory-only with a maximum 240-second local lifetime")
	}
	if policy.ModelGate.Required && policy.RehearsalModelGate.Required {
		return errors.New("competition and rehearsal model gates cannot both be required")
	}
	if gate, required := EffectiveModelGate(policy); required {
		if err := validateRequiredModelGate(policy, gate); err != nil {
			return err
		}
	}
	return nil
}

const (
	CompetitionModelApprovalStatus = "APPROVED_FOR_COMPETITION_SUBMISSION"
	RehearsalModelApprovalStatus   = "APPROVED_USER_AUTHORIZED_MODEL_REHEARSAL"
	RehearsalApprovalScope         = "DATE_SCOPED_USER_REHEARSAL"
)

// normalizePolicyGates supports both the canonical model_gate shape and the
// short-lived rehearsal_model_gate shape emitted by the first V4 generator.
// The canonical model gate always wins when it is required; callers should use
// EffectiveModelGate instead of inspecting one JSON member directly.
func normalizePolicyGates(policy Policy) Policy {
	policy.ModelGate = normalizeModelGate(policy.ModelGate)
	policy.RehearsalModelGate = normalizeModelGate(policy.RehearsalModelGate)
	return policy
}

func normalizeModelGate(gate ModelGate) ModelGate {
	if gate.ApprovalStatus == "" {
		gate.ApprovalStatus = gate.Status
	}
	if gate.ApprovalScope == "" {
		gate.ApprovalScope = gate.Scope
	}
	if gate.GatePath == "" {
		gate.GatePath = gate.Path
	}
	return gate
}

// EffectiveModelGate returns the single gate that is allowed to constrain a
// runtime. A rehearsal gate is kept separate in the JSON schema so an
// explicitly disabled competition gate cannot accidentally authorize it.
func EffectiveModelGate(policy Policy) (ModelGate, bool) {
	policy = normalizePolicyGates(policy)
	if policy.ModelGate.Required {
		return policy.ModelGate, true
	}
	if policy.RehearsalModelGate.Required {
		return policy.RehearsalModelGate, true
	}
	return ModelGate{}, false
}

func validateRequiredModelGate(policy Policy, gate ModelGate) error {
	if gate.ApprovalID == "" || gate.ModelPath == "" || gate.AdapterPath == "" || gate.ValidationPath == "" ||
		!hex64Pattern.MatchString(gate.ModelSHA256) ||
		!hex64Pattern.MatchString(gate.AdapterSHA256) ||
		!hex64Pattern.MatchString(gate.ValidationSHA) {
		return errors.New("required model gate is not fully approved and hash-pinned")
	}
	switch gate.ApprovalStatus {
	case CompetitionModelApprovalStatus:
		// Preserve the original competition contract. Its approval record is
		// the trust anchor; a gate artifact is optional for this legacy mode.
		return nil
	case RehearsalModelApprovalStatus:
		targetDate := canonicalPolicyDate(gate.TargetDate)
		connectionDate := canonicalPolicyDate(policy.ConnectionDate)
		if gate.ApprovalScope != RehearsalApprovalScope || targetDate == "" || connectionDate == "" ||
			targetDate != connectionDate ||
			gate.GatePath == "" || !hex64Pattern.MatchString(gate.GateSHA256) {
			return errors.New("required rehearsal model gate is not date-scoped and hash-pinned")
		}
		if policy.Submission.StakeUnit != 100 || MaxStakePerRace(policy) != 100 ||
			MaxDailyStake(policy) < 100 || MaxDailyStake(policy) > 3600 ||
			(policy.Submission.MaxTotalStake > 0 && policy.Submission.MaxTotalStake > 3600) {
			return errors.New("required rehearsal model gate must be limited to 100 points per race and 3600 points per day")
		}
		return nil
	default:
		return errors.New("required model gate approval status is not authorized")
	}
}

func canonicalPolicyDate(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == len("20060102") && !strings.Contains(value, "-") {
		if parsed, err := time.Parse("20060102", value); err == nil {
			return parsed.Format("2006-01-02")
		}
	}
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed.Format("2006-01-02")
	}
	return ""
}

func validateRequiredGateArtifacts(root string, gate ModelGate) error {
	artifacts := []struct {
		label       string
		path        string
		declaredSHA string
	}{
		{"model", gate.ModelPath, gate.ModelSHA256},
		{"adapter", gate.AdapterPath, gate.AdapterSHA256},
		{"validation", gate.ValidationPath, gate.ValidationSHA},
	}
	if gate.GatePath != "" {
		artifacts = append(artifacts, struct {
			label       string
			path        string
			declaredSHA string
		}{"gate", gate.GatePath, gate.GateSHA256})
	}
	for _, artifact := range artifacts {
		if artifact.path == "" || !hex64Pattern.MatchString(artifact.declaredSHA) {
			return fmt.Errorf("required %s artifact path/hash is incomplete", artifact.label)
		}
		resolved := resolve(root, artifact.path)
		if err := validatePathNoSymlink(root, resolved); err != nil {
			return fmt.Errorf("required %s artifact path: %w", artifact.label, err)
		}
		actual, err := hashRegularFile(resolved)
		if err != nil {
			return fmt.Errorf("required %s artifact: %w", artifact.label, err)
		}
		if actual != artifact.declaredSHA {
			return fmt.Errorf("required %s artifact SHA-256 does not match declaration", artifact.label)
		}
	}
	return nil
}

func hashRegularFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("artifact must be a regular non-symlink file")
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
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func MaxStakePerRace(policy Policy) int {
	if policy.Submission.MaxStakePerRace > 0 {
		return policy.Submission.MaxStakePerRace
	}
	return policy.Submission.MaxTotalStake
}

func MaxDailyStake(policy Policy) int {
	if policy.Submission.MaxDailyTotalStake > 0 {
		return policy.Submission.MaxDailyTotalStake
	}
	return policy.Submission.MaxTotalStake
}

func resolve(root, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(root, filepath.Clean(path))
}

// validatePathNoSymlink protects all mutable runtime paths from path
// substitution.  Existing and not-yet-created components are checked
// lexically, so a symlink in a parent directory is rejected instead of being
// silently followed by os.OpenFile/os.Rename.
func validatePathNoSymlink(root, path string) error {
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
		return errors.New("path must remain inside project root")
	}
	cursor := absoluteRoot
	if info, statErr := os.Lstat(cursor); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("project root is a symlink: %s", cursor)
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		cursor = filepath.Join(cursor, component)
		info, statErr := os.Lstat(cursor)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return fmt.Errorf("inspect path component %s: %w", cursor, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink path component is not allowed: %s", cursor)
		}
	}
	return nil
}

func FindProjectRoot() (string, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for current := workingDirectory; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("cannot locate project root")
		}
	}
}
