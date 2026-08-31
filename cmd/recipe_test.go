// NOTE: no //go:build e2e tag — this file runs in the DEFAULT test suite
// (moved out of the e2e tag in 3d8943b). See agentsmd_test.go for the
// rationale and hermeticity constraints for new tests here.
package cmd

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestE2E_Recipe_ListAndShow exercises the recipe registry + cobra
// plumbing via a real trond subprocess. Verifies:
//
//   - `trond recipe list --output json` returns the registry as an
//     array with the canonical recipes embedded at build time
//   - `trond recipe show <name> --output json` decodes one recipe's
//     full structure (steps + params)
//
// No Docker required — these are pure read-side commands.
func TestE2E_Recipe_ListAndShow(t *testing.T) {
	_, env := e2eEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// list
	out := runTrondCtx(ctx, t, env, "recipe", "list", "--output", "json")
	var listed struct {
		Recipes []map[string]any `json:"recipes"`
	}
	mustUnmarshalJSON(t, out, &listed)
	wantNames := []string{
		"nile-test-fullnode",
		"fresh-mainnet-fullnode-with-snapshot",
		"upgrade-with-verify",
		"recover-failed-upgrade",
		"destroy-private-network-cleanly",
	}
	got := map[string]bool{}
	for _, r := range listed.Recipes {
		if name, _ := r["name"].(string); name != "" {
			got[name] = true
		}
	}
	for _, n := range wantNames {
		if !got[n] {
			t.Errorf("recipe %q missing from list output: %s", n, out)
		}
	}

	// show
	out = runTrondCtx(ctx, t, env, "recipe", "show", "nile-test-fullnode", "--output", "json")
	var shown struct {
		Name  string           `json:"name"`
		Steps []map[string]any `json:"steps"`
	}
	mustUnmarshalJSON(t, out, &shown)
	if shown.Name != "nile-test-fullnode" {
		t.Errorf("unexpected name from show: %q", shown.Name)
	}
	wantStepIDs := []string{"validate", "preflight", "apply", "verify"}
	if len(shown.Steps) != len(wantStepIDs) {
		t.Fatalf("expected %d steps, got %d: %s", len(wantStepIDs), len(shown.Steps), out)
	}
	for i, want := range wantStepIDs {
		if id, _ := shown.Steps[i]["id"].(string); id != want {
			t.Errorf("step[%d] id: want %q, got %q", i, want, id)
		}
	}
}

// TestE2E_Recipe_DryRun exercises the dry-run path through a real
// subprocess. Confirms:
//
//   - --param key=value is parsed correctly
//   - {{ params.intent_path }} substitution lands in the printed
//     command line
//   - all 4 steps are visited (none skipped, no early abort)
//
// No Docker required — dry-run never re-execs the inner subprocesses.
func TestE2E_Recipe_DryRun(t *testing.T) {
	_, env := e2eEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	intentPath := absExample(t, "examples/nile-fullnode.yaml")

	out := runTrondCtx(ctx, t, env,
		"recipe", "run", "nile-test-fullnode",
		"--dry-run",
		"--param", "intent_path="+intentPath,
	)
	body := string(out)

	// Each step prints "  [<id>] would run: <binary> <cmd> <args>".
	for _, want := range []string{
		"[validate] would run:",
		"[preflight] would run:",
		"[apply] would run:",
		"[verify] would run:",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dry-run output missing %q\nfull output:\n%s", want, body)
		}
	}
	// Param substitution: the intent_path should appear at least once
	// in the rendered commands (apply + verify both reference it).
	if !strings.Contains(body, intentPath) {
		t.Errorf("dry-run did not interpolate intent_path %q\noutput:\n%s", intentPath, body)
	}
}
