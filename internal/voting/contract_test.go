package voting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Checks that Go computes the same hashes as the Python planner for the fixture
// in fixtures/plan_bundle_contract.json.
func TestPlanBundleHashesMatchThePythonPlanner(t *testing.T) {
	root, err := FindProjectRoot()
	if err != nil {
		t.Fatalf("FindProjectRoot: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "fixtures", "plan_bundle_contract.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var bundle PlanBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(bundle.Plans) != 1 {
		t.Fatalf("fixture carries %d plans, want 1", len(bundle.Plans))
	}

	plan := bundle.Plans[0]
	payloadDigest, err := PayloadSHA256(plan.Payload)
	if err != nil {
		t.Fatalf("PayloadSHA256: %v", err)
	}
	if payloadDigest != plan.PayloadSHA256 {
		t.Fatalf("payload hash mismatch:\n  go     %s\n  python %s", payloadDigest, plan.PayloadSHA256)
	}

	bundleDigest, err := BundleSHA256(bundle)
	if err != nil {
		t.Fatalf("BundleSHA256: %v", err)
	}
	if bundleDigest != bundle.BundleSHA256 {
		t.Fatalf("bundle hash mismatch:\n  go     %s\n  python %s", bundleDigest, bundle.BundleSHA256)
	}

	planDigest, err := PlanEnvelopeSHA256(plan)
	if err != nil {
		t.Fatalf("PlanEnvelopeSHA256: %v", err)
	}
	if len(planDigest) != 64 {
		t.Fatalf("plan envelope digest has length %d, want 64", len(planDigest))
	}

	for _, bet := range plan.Payload.BetList {
		if err := ValidateBetID(bet.BetID); err != nil {
			t.Fatalf("bet id %q written by the planner is invalid: %v", bet.BetID, err)
		}
	}
}
