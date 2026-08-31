// NOTE: no //go:build e2e tag — this file runs in the DEFAULT test suite
// (moved out of the e2e tag in 3d8943b). See agentsmd_test.go for the
// rationale and hermeticity constraints for new tests here.
package cmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// TestE2E_Recipe_DryRunMatrix walks every embedded recipe through
// `recipe run --dry-run` with realistic --param values and asserts:
//
//   - the runner produces a "would run:" line for every step
//   - all {{ params.* }} placeholders resolved (no literal "{{"
//     left in the output)
//   - the command listed for each step is at least the prefix of a
//     real trond subcommand path (no typos / removed commands)
//
// Without this matrix, a future renaming of (say) `trond rollback`
// to something else would break recover-failed-upgrade.yaml silently
// — the recipe loads, the dry-run prints, but real execution would
// fail at step time. This test is the smoke check that turns "silent"
// into "loud."
//
// No Docker required.
func TestE2E_Recipe_DryRunMatrix(t *testing.T) {
	intentPath := absExample(t, "examples/nile-fullnode.yaml")
	snapIntent := absExample(t, "examples/mainnet-fullnode-snapshot.yaml")

	cases := []struct {
		recipe string
		params []string
		// stepIDs we expect to see in the dry-run output.
		expectSteps []string
	}{
		{
			recipe:      "nile-test-fullnode",
			params:      []string{"intent_path=" + intentPath},
			expectSteps: []string{"validate", "preflight", "apply", "verify"},
		},
		{
			recipe: "fresh-mainnet-fullnode-with-snapshot",
			params: []string{
				"intent_path=" + snapIntent,
				"snapshot_dest=/tmp/trond-recipe-fixture-snapshot",
				"snapshot_kind=lite",
			},
			expectSteps: []string{"validate", "preflight"},
		},
		{
			recipe:      "destroy-private-network-cleanly",
			params:      []string{"network=private-dev"},
			expectSteps: []string{"status-check", "destroy"},
		},
		{
			recipe:      "recover-failed-upgrade",
			params:      []string{"node=my-fullnode"},
			expectSteps: []string{"diagnose", "rollback"},
		},
		{
			recipe: "upgrade-with-verify",
			params: []string{
				"node=my-fullnode",
				"version=4.8.1",
				"intent_path=" + intentPath,
			},
			expectSteps: []string{"pre-upgrade-status", "upgrade", "verify"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, tc := range cases {
		t.Run(tc.recipe, func(t *testing.T) {
			_, env := e2eEnv(t)
			args := []string{"recipe", "run", tc.recipe, "--dry-run"}
			for _, p := range tc.params {
				args = append(args, "--param", p)
			}
			out := runTrondCtx(ctx, t, env, args...)
			body := string(out)

			// 1. unresolved templates would surface as literal "{{ params.X }}"
			//    (or trim variants). Substitution is supposed to leave none behind.
			if strings.Contains(body, "{{") {
				t.Errorf("dry-run output contains unresolved template directive\n%s", body)
			}

			// 2. each step must produce a "would run:" line.
			for _, id := range tc.expectSteps {
				marker := "[" + id + "] would run:"
				if !strings.Contains(body, marker) {
					t.Errorf("dry-run missing %q\nfull output:\n%s", marker, body)
				}
			}

			// 3. recipe run normally renders dry-run lines like
			//    "  [step] would run: <binary> <subcmd> --output json".
			//    Tear off the cmd portion and assert each invoked
			//    subcommand exists in the cobra tree.
			validateInvokedCommandsExist(t, body)
		})
	}
}

// validateInvokedCommandsExist parses each "would run:" line and
// checks that the trond subcommand named there resolves to a real
// cobra command. Catches recipes that reference removed commands.
func validateInvokedCommandsExist(t *testing.T, dryRunBody string) {
	t.Helper()
	root := Root() // *cobra.Command — see cmd/root.go
	for _, line := range strings.Split(dryRunBody, "\n") {
		idx := strings.Index(line, "would run:")
		if idx < 0 {
			continue
		}
		// Format after "would run: ": "<binary> <cmd> [args...]"
		rest := strings.TrimSpace(line[idx+len("would run:"):])
		fields := strings.Fields(rest)
		if len(fields) < 2 {
			continue
		}
		// Strip the binary path; first arg after it is the command.
		// Recipes use e.g. "config validate", "snapshot download" —
		// multi-word commands. Try the longest prefix that resolves.
		args := fields[1:]
		if !commandExists(root, args) {
			t.Errorf("recipe references unknown trond command path: %v", args)
		}
	}
}

// commandExists returns true when the cobra tree resolves the
// longest-matching subcommand prefix from args. Args may include
// flags like "--auto-approve"; we stop at the first token starting
// with '-'.
func commandExists(root *cobra.Command, args []string) bool {
	tokens := []string{}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}
		tokens = append(tokens, a)
	}
	if len(tokens) == 0 {
		return false
	}
	cur, _, err := root.Find(tokens)
	if err != nil || cur == nil || cur == root {
		return false
	}
	// cobra.Find returns the deepest matched command — for our
	// recipe-validation purpose, any non-root match means the
	// command path resolves. Recipes legitimately pass positional
	// args after the command (`network status pn`); cobra returns
	// the network/status node and we don't care that "pn" was
	// unconsumed.
	return true
}
