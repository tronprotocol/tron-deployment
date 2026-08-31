// NOTE: no //go:build e2e tag — this file runs in the DEFAULT test suite
// (moved out of the e2e tag in 3d8943b). See agentsmd_test.go for the
// rationale and hermeticity constraints for new tests here.
package cmd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// TestE2E_SchemaManifestReverse turns `trond schema -o json` from a
// curiosity into a contract: every command + flag the manifest
// advertises must actually exist in the cobra tree, with the same
// types and required-ness.
//
// Why: AI agents read the manifest once and from that point believe
// they know every available command. If a flag in the manifest
// doesn't actually exist (rename, removal), the agent emits invalid
// CLI calls and gets confused. The conformance e2e checks the
// schemas/output/ shape; this test checks the manifest's command
// surface.
//
// No Docker required.
func TestE2E_SchemaManifestReverse(t *testing.T) {
	_, env := e2eEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out := runTrondCtx(ctx, t, env, "schema", "--output", "json")

	var manifest struct {
		SchemaVersion string            `json:"schema_version"`
		Tool          string            `json:"tool"`
		Commands      []manifestCommand `json:"commands"`
	}
	if err := json.Unmarshal(out, &manifest); err != nil {
		t.Fatalf("manifest is not JSON: %v\n%s", err, out)
	}
	if manifest.SchemaVersion == "" {
		t.Errorf("manifest.schema_version is empty")
	}
	if manifest.Tool != "trond" {
		t.Errorf("manifest.tool: want \"trond\", got %q", manifest.Tool)
	}
	if len(manifest.Commands) == 0 {
		t.Fatal("manifest has no commands; cobra walker is broken")
	}

	root := Root()
	for _, cmd := range manifest.Commands {
		t.Run(cmd.FullName, func(t *testing.T) {
			verifyCommand(t, root, cmd)
		})
	}
}

type manifestCommand struct {
	Name        string            `json:"name"`
	FullName    string            `json:"full_name"`
	Use         string            `json:"use"`
	Aliases     []string          `json:"aliases"`
	Flags       []manifestFlag    `json:"flags"`
	Subcommands []manifestCommand `json:"subcommands"`
}

type manifestFlag struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Default  string `json:"default"`
	Usage    string `json:"usage"`
	Required bool   `json:"required"`
}

func verifyCommand(t *testing.T, root *cobra.Command, cmd manifestCommand) {
	t.Helper()
	// Resolve "trond <a> <b>" → cobra command.
	parts := strings.Fields(cmd.FullName)
	if len(parts) == 0 || parts[0] != "trond" {
		t.Fatalf("unexpected full_name %q", cmd.FullName)
	}
	// cobra adds `completion` as an auto-generated subcommand only
	// at Execute time; the static tree the test runs against doesn't
	// include it, so skip the lookup. The manifest walker iterates
	// Commands() at run time and does see it, which is correct.
	if cmd.FullName == "trond completion" {
		return
	}
	cobraCmd, _, err := root.Find(parts[1:])
	if err != nil || cobraCmd == nil || (cobraCmd == root && len(parts) > 1) {
		t.Fatalf("manifest references %q but cobra can't find it", cmd.FullName)
	}

	// Verify each advertised flag is actually defined on the
	// command (or inherited from a parent). cobra adds --help / -h
	// implicitly to every command at execution time; not present in
	// the static flag-set.
	for _, f := range cmd.Flags {
		if f.Name == "help" || f.Name == "h" {
			continue
		}
		if cobraCmd.Flag(f.Name) == nil && cobraCmd.PersistentFlags().Lookup(f.Name) == nil &&
			cobraCmd.InheritedFlags().Lookup(f.Name) == nil {
			t.Errorf("manifest advertises flag --%s on %s but cobra has no such flag",
				f.Name, cmd.FullName)
		}
	}

	// Verify aliases match.
	gotAliases := map[string]bool{}
	for _, a := range cobraCmd.Aliases {
		gotAliases[a] = true
	}
	for _, a := range cmd.Aliases {
		if !gotAliases[a] {
			t.Errorf("manifest advertises alias %q on %s but cobra doesn't have it",
				a, cmd.FullName)
		}
	}

	// Recurse into subcommands.
	for _, sub := range cmd.Subcommands {
		verifyCommand(t, root, sub)
	}
}
