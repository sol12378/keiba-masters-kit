package voting

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The payload hash does not include the schedule, so a bundle with the same bets
// but different post times must not arm the previously imported plans.
func TestArmingRefusesABundleWhoseScheduleDiffersFromTheImportedPlan(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Tokyo")
	clock := &fakeClock{now: time.Date(2026, 8, 15, 8, 30, 0, 0, location)}
	policy := fullDayPolicy(t)
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
	payload := minimalPayload()
	imported := fullDayPlan(t, policy, clock.Now(), payload, time.Date(2026, 8, 15, 10, 0, 0, 0, location))
	replanned := fullDayPlan(t, policy, clock.Now(), payload, time.Date(2026, 8, 15, 11, 0, 0, 0, location))
	replanned.PlanID = imported.PlanID

	if imported.PayloadSHA256 != replanned.PayloadSHA256 {
		t.Fatal("the two plans should share a payload hash; the test no longer covers the case")
	}

	store, err := OpenStore(config, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(config, store, &fakeAPI{}, func() (string, string, error) { return "student", "password", nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)), clock.Now)
	t.Setenv("KEIBA_ENABLE_FULL_DAY_SUBMISSION", "FULL-DAY-2026-08-15")

	if _, err := service.ImportBundle(fullDayBundle(t, policy, clock.Now(), []PlanEnvelope{imported})); err != nil {
		t.Fatalf("import: %v", err)
	}

	replannedBundle := fullDayBundle(t, policy, clock.Now(), []PlanEnvelope{replanned})
	_, err = service.ArmBundle(replannedBundle, replannedBundle.BundleSHA256)
	if err == nil {
		t.Fatal("expected arming a re-scheduled bundle to be refused")
	}
	if !strings.Contains(err.Error(), "different schedule") {
		t.Fatalf("error does not explain the schedule mismatch: %v", err)
	}
	if record := store.Snapshot().Races[imported.PlanID]; record.Armed {
		t.Fatal("the imported plan was armed despite the refusal")
	}
}

// The imported bundle itself still arms.
func TestArmingAcceptsTheBundleThatWasImported(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Tokyo")
	clock := &fakeClock{now: time.Date(2026, 8, 15, 8, 30, 0, 0, location)}
	policy := fullDayPolicy(t)
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
	plan := fullDayPlan(t, policy, clock.Now(), minimalPayload(), time.Date(2026, 8, 15, 10, 0, 0, 0, location))
	bundle := fullDayBundle(t, policy, clock.Now(), []PlanEnvelope{plan})

	store, err := OpenStore(config, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(config, store, &fakeAPI{}, func() (string, string, error) { return "student", "password", nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)), clock.Now)
	t.Setenv("KEIBA_ENABLE_FULL_DAY_SUBMISSION", "FULL-DAY-2026-08-15")

	if _, err := service.ImportBundle(bundle); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := service.ArmBundle(bundle, bundle.BundleSHA256); err != nil {
		t.Fatalf("arm: %v", err)
	}
	if !store.Snapshot().Races[plan.PlanID].Armed {
		t.Fatal("the imported bundle did not arm")
	}
}
