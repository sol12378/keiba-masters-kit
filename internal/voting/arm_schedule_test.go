package voting

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Re-planning the same selections for a later post time leaves the payload
// hash untouched -- the payload is race_id, marks and bets, with no schedule
// in it -- while the bundle hash changes.  Arming compared only the payload
// hash, so an operator could confirm the new bundle's digest and silently arm
// the schedule that was imported earlier.  In the case that surfaced this, the
// imported schedule had already passed its cutoff, so all four races expired
// the moment they were armed.
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

// The same bundle that was imported still arms, so the new check does not
// simply forbid arming.
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
