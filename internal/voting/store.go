package voting

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"
)

type RaceRecord struct {
	Plan                 PlanEnvelope `json:"plan"`
	PlanSHA256           string       `json:"plan_sha256"`
	State                string       `json:"state"`
	Armed                bool         `json:"armed"`
	ArmCount             int          `json:"arm_count"`
	POSTAttempts         int          `json:"post_attempts"`
	PrecheckAttempts     int          `json:"precheck_attempts,omitempty"`
	VerificationAttempts int          `json:"verification_attempts"`
	NextActionAt         *time.Time   `json:"next_action_at,omitempty"`
	LastHTTPStatus       int          `json:"last_http_status,omitempty"`
	LastAPIStatus        string       `json:"last_api_status,omitempty"`
	LastAPIReason        string       `json:"last_api_reason,omitempty"`
	LastErrorClass       string       `json:"last_error_class,omitempty"`
	LastErrorOperation   string       `json:"last_error_operation,omitempty"`
	RemainingMoney       string       `json:"remaining_money,omitempty"`
	UpdatedAt            time.Time    `json:"updated_at"`
}

type Snapshot struct {
	SchemaVersion int                    `json:"schema_version"`
	Sequence      uint64                 `json:"sequence"`
	Mode          string                 `json:"mode"`
	Killed        bool                   `json:"killed"`
	KillReason    string                 `json:"kill_reason,omitempty"`
	Races         map[string]*RaceRecord `json:"races"`
	UpdatedAt     time.Time              `json:"updated_at"`
}

type Event struct {
	EventID       string      `json:"event_id"`
	Sequence      uint64      `json:"sequence"`
	OccurredAt    time.Time   `json:"occurred_at"`
	RaceID        string      `json:"race_id,omitempty"`
	PlanID        string      `json:"plan_id,omitempty"`
	PayloadSHA256 string      `json:"payload_sha256,omitempty"`
	FromState     string      `json:"from_state,omitempty"`
	ToState       string      `json:"to_state,omitempty"`
	Operation     string      `json:"operation"`
	ErrorClass    string      `json:"error_class,omitempty"`
	Record        *RaceRecord `json:"record,omitempty"`
	Killed        *bool       `json:"killed,omitempty"`
	KillReason    string      `json:"kill_reason,omitempty"`
}

type Store struct {
	mu       sync.Mutex
	journal  string
	snapshot string
	state    Snapshot
	now      func() time.Time
}

func OpenStore(config RuntimeConfig, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	for label, path := range map[string]string{
		"state directory": config.StateDir,
		"journal":         config.Journal,
		"snapshot":        config.Snapshot,
		"control socket":  config.Socket,
		"kill switch":     config.KillSwitch,
	} {
		if err := validatePathNoSymlink(config.Root, path); err != nil {
			return nil, fmt.Errorf("%s path: %w", label, err)
		}
	}
	if err := os.MkdirAll(config.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(config.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("protect state directory: %w", err)
	}
	store := &Store{
		journal:  config.Journal,
		snapshot: config.Snapshot,
		now:      now,
		state: Snapshot{
			SchemaVersion: 1,
			Mode:          config.Policy.Modes.Default,
			Races:         map[string]*RaceRecord{},
			UpdatedAt:     now().UTC(),
		},
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *Store) load() error {
	if info, err := os.Lstat(store.journal); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("journal path is a symlink")
		}
		if !info.Mode().IsRegular() {
			return errors.New("journal path is not a regular file")
		}
		if err := os.Chmod(store.journal, 0o600); err != nil {
			return fmt.Errorf("protect journal: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect journal: %w", err)
	}
	file, err := os.Open(store.journal)
	if errors.Is(err, os.ErrNotExist) {
		if snapshotData, snapshotErr := os.ReadFile(store.snapshot); snapshotErr == nil {
			var previous Snapshot
			if decodeErr := json.Unmarshal(snapshotData, &previous); decodeErr != nil {
				return fmt.Errorf("snapshot integrity failure: %w", decodeErr)
			}
			if previous.Sequence != 0 || len(previous.Races) != 0 || previous.Killed {
				return errors.New("journal is missing while snapshot contains voting state")
			}
		} else if !errors.Is(snapshotErr, os.ErrNotExist) {
			return fmt.Errorf("read snapshot: %w", snapshotErr)
		}
		return store.writeSnapshotLocked()
	}
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return fmt.Errorf("journal integrity failure at line %d: %w", line, err)
		}
		if event.Sequence != store.state.Sequence+1 {
			return fmt.Errorf("journal sequence failure at line %d", line)
		}
		if err := store.validateEvent(event); err != nil {
			return fmt.Errorf("journal integrity failure at line %d: %w", line, err)
		}
		store.applyEventMemory(event)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read journal: %w", err)
	}
	if snapshotData, snapshotErr := os.ReadFile(store.snapshot); snapshotErr == nil {
		var previous Snapshot
		if decodeErr := json.Unmarshal(snapshotData, &previous); decodeErr != nil {
			return fmt.Errorf("snapshot integrity failure: %w", decodeErr)
		}
		if previous.SchemaVersion != 1 {
			return errors.New("snapshot schema version is unsupported")
		}
		if previous.Sequence > store.state.Sequence {
			return errors.New("snapshot sequence is ahead of journal")
		}
		if previous.Sequence == store.state.Sequence && !reflect.DeepEqual(previous, store.state) {
			return errors.New("snapshot does not match journal state")
		}
	} else if !errors.Is(snapshotErr, os.ErrNotExist) {
		return fmt.Errorf("read snapshot: %w", snapshotErr)
	}
	return store.writeSnapshotLocked()
}

func (store *Store) validateEvent(event Event) error {
	if event.EventID != fmt.Sprintf("evt-%020d", event.Sequence) {
		return errors.New("event_id does not match sequence")
	}
	if event.OccurredAt.IsZero() || event.Operation == "" {
		return errors.New("event timestamp and operation are required")
	}
	if event.Killed != nil {
		if !*event.Killed || event.Operation != "kill" || event.Record != nil || event.PlanID != "" || event.RaceID != "" || event.KillReason == "" || len(event.KillReason) > 256 {
			return errors.New("invalid kill event")
		}
		return nil
	}
	if event.Record == nil || event.PlanID == "" || event.RaceID == "" || event.ToState == "" {
		return errors.New("record event is incomplete")
	}
	record := event.Record
	if record.Plan.PlanID != event.PlanID || record.State != event.ToState || record.Plan.Payload.RaceID != event.RaceID || record.Plan.PayloadSHA256 != event.PayloadSHA256 {
		return errors.New("record event identity does not match payload")
	}
	if record.UpdatedAt.IsZero() || !record.UpdatedAt.Equal(event.OccurredAt) {
		return errors.New("record event timestamp mismatch")
	}
	if !knownState(record.State) {
		return errors.New("record event contains an unknown state")
	}
	digest, err := PayloadSHA256(record.Plan.Payload)
	if err != nil || digest != record.Plan.PayloadSHA256 || !hex64Pattern.MatchString(record.Plan.PayloadSHA256) {
		return errors.New("record event payload hash mismatch")
	}
	planDigest, err := PlanEnvelopeSHA256(record.Plan)
	if err != nil || record.PlanSHA256 != planDigest || !hex64Pattern.MatchString(record.PlanSHA256) {
		return errors.New("record event plan hash mismatch (PlanEnvelope)")
	}
	previous, exists := store.state.Races[event.PlanID]
	if event.Operation == "import_plan" {
		if exists || event.FromState != StateDiscovered {
			return errors.New("invalid import transition")
		}
	} else {
		if !exists || event.FromState != previous.State {
			return errors.New("invalid record state transition")
		}
	}
	if isTerminal(record.State) && (record.Armed || record.NextActionAt != nil) {
		return errors.New("terminal record remains armed")
	}
	return nil
}

func knownState(state string) bool {
	switch state {
	case StateDiscovered, StateValidated, StateArmed, StatePrecheckedEmpty,
		StatePosting, StatePendingConfirmation, StateConfirmed,
		StateConfirmedExisting, StateConflict, StateAmbiguous, StateRejected,
		StateExpired, StateKilled:
		return true
	default:
		return false
	}
}

func (store *Store) Snapshot() Snapshot {
	store.mu.Lock()
	defer store.mu.Unlock()
	return cloneSnapshot(store.state)
}

func (store *Store) ImportPlan(plan PlanEnvelope) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.state.Races[plan.PlanID]; exists {
		return errors.New("plan_id already exists")
	}
	for _, record := range store.state.Races {
		if record.Plan.Payload.RaceID == plan.Payload.RaceID {
			if record.Plan.PayloadSHA256 != plan.PayloadSHA256 {
				return errors.New("a different plan already exists for race_id")
			}
			return errors.New("a plan already exists for race_id")
		}
	}
	now := store.now().UTC()
	planHash, err := PlanEnvelopeSHA256(plan)
	if err != nil {
		return err
	}
	record := &RaceRecord{Plan: plan, PlanSHA256: planHash, State: StateValidated, UpdatedAt: now}
	return store.appendLocked(Event{
		OccurredAt:    now,
		RaceID:        plan.Payload.RaceID,
		PlanID:        plan.PlanID,
		PayloadSHA256: plan.PayloadSHA256,
		FromState:     StateDiscovered,
		ToState:       StateValidated,
		Operation:     "import_plan",
		Record:        record,
	})
}

func (store *Store) UpdateRecord(planID, toState, operation, errorClass string, mutate func(*RaceRecord) error) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	existing, ok := store.state.Races[planID]
	if !ok {
		return errors.New("unknown plan_id")
	}
	record := cloneRecord(existing)
	if mutate != nil {
		if err := mutate(record); err != nil {
			return err
		}
	}
	fromState := record.State
	record.State = toState
	record.UpdatedAt = store.now().UTC()
	if isTerminal(toState) {
		record.Armed = false
		record.NextActionAt = nil
	}
	return store.appendLocked(Event{
		OccurredAt:    record.UpdatedAt,
		RaceID:        record.Plan.Payload.RaceID,
		PlanID:        planID,
		PayloadSHA256: record.Plan.PayloadSHA256,
		FromState:     fromState,
		ToState:       toState,
		Operation:     operation,
		ErrorClass:    errorClass,
		Record:        record,
	})
}

func (store *Store) SetKilled(reason string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	value := true
	now := store.now().UTC()
	return store.appendLocked(Event{
		OccurredAt: now,
		Operation:  "kill",
		Killed:     &value,
		KillReason: reason,
	})
}

func (store *Store) appendLocked(event Event) error {
	event.Sequence = store.state.Sequence + 1
	event.EventID = fmt.Sprintf("evt-%020d", event.Sequence)
	if err := store.validateEvent(event); err != nil {
		return fmt.Errorf("event validation: %w", err)
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(store.journal, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open journal for append: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return fmt.Errorf("append journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	store.applyEventMemory(event)
	if err := store.writeSnapshotLocked(); err != nil {
		// The journal is authoritative. Keep the in-memory state and fail the
		// operation so the caller does not proceed to network activity.
		return fmt.Errorf("write snapshot: %w", err)
	}
	return nil
}

func (store *Store) applyEventMemory(event Event) {
	store.state.Sequence = event.Sequence
	store.state.UpdatedAt = event.OccurredAt
	if event.Record != nil {
		store.state.Races[event.PlanID] = cloneRecord(event.Record)
	}
	if event.Killed != nil {
		store.state.Killed = *event.Killed
		store.state.KillReason = event.KillReason
	}
}

func (store *Store) writeSnapshotLocked() error {
	directory := filepath.Dir(store.snapshot)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".snapshot-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(store.state); err != nil {
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
	if err := os.Rename(temporaryPath, store.snapshot); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}

func cloneRecord(record *RaceRecord) *RaceRecord {
	data, _ := json.Marshal(record)
	var cloned RaceRecord
	_ = json.Unmarshal(data, &cloned)
	if cloned.PlanSHA256 == "" {
		if digest, err := PlanEnvelopeSHA256(cloned.Plan); err == nil {
			cloned.PlanSHA256 = digest
		}
	}
	return &cloned
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	data, _ := json.Marshal(snapshot)
	var cloned Snapshot
	_ = json.Unmarshal(data, &cloned)
	return cloned
}

func isTerminal(state string) bool {
	switch state {
	case StateConfirmed, StateConfirmedExisting, StateConflict, StateRejected, StateExpired, StateKilled:
		return true
	default:
		return false
	}
}
