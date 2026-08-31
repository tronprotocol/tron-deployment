// NOTE: no //go:build e2e tag — this file runs in the DEFAULT test suite
// (moved out of the e2e tag in 3d8943b). See agentsmd_test.go for the
// rationale and hermeticity constraints for new tests here.
package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// TestE2E_SchemaConformance walks every entry in the table and asserts
// that the live `trond <cmd> -o json` output validates against the
// matching schemas/output/<name>.schema.json.
//
// Why this exists: the schemas under schemas/output/ are advertised
// to AI agents and CI consumers as the contract for trond's JSON
// output. They were originally written aspirationally and drifted
// from the implementation — the first three e2e tests landed last
// caught three drift bugs (recipe-show, network-create,
// network-status). Rather than spot-checking each command by hand,
// this test runs every no-Docker command we can reach and validates
// the output as JSON Schema. Schema drift after this lands fails CI.
//
// Docker-gated cases (apply, network-create, status, health,
// diagnose, verify) live in their own e2e tests because they need a
// real container; the conformance pass focuses on the read-side that
// runs in milliseconds.
func TestE2E_SchemaConformance(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	intentPath := filepath.Join(repoRoot, "examples", "nile-fullnode.yaml")

	cases := []struct {
		// name is the subtest name and (lowercased, .schema.json
		// suffixed) the schema file to load.
		schema string
		args   []string
		// skipReason, when non-empty, marks this case as known-bad
		// and skips it instead of failing — used for cases where the
		// schema or the output is wrong but a fix would cascade
		// outside this PR's scope.
		skipReason string
	}{
		{schema: "version", args: []string{"version"}},
		{schema: "doctor", args: []string{"doctor"}},
		{schema: "list", args: []string{"list"}},
		{schema: "preflight", args: []string{"preflight", "--intent", intentPath}},
		{schema: "config-validate", args: []string{"config", "validate", intentPath}},
		{schema: "config-render", args: []string{"config", "render", intentPath}},
		{schema: "config-diff", args: []string{"config", "diff", intentPath}},
		{schema: "plan", args: []string{"plan", "--intent", intentPath}},
		{schema: "recipe-list", args: []string{"recipe", "list"}},
		{schema: "recipe-show", args: []string{"recipe", "show", "nile-test-fullnode"}},
		{schema: "snapshot-sources", args: []string{"snapshot", "sources"}},
		{schema: "snapshot-list", args: []string{"snapshot", "list", "--network", "nile"}},
		{schema: "snapshot-jobs", args: []string{"snapshot", "jobs"}},
		{schema: "network-status", args: []string{"network", "status"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, tc := range cases {
		t.Run(tc.schema, func(t *testing.T) {
			if tc.skipReason != "" {
				t.Skip(tc.skipReason)
			}
			_, env := e2eEnv(t)
			args := append([]string{}, tc.args...)
			args = append(args, "--output", "json")

			out := runTrondCtx(ctx, t, env, args...)

			var parsed any
			if err := json.Unmarshal(out, &parsed); err != nil {
				t.Fatalf("output not valid JSON: %v\nraw:\n%s", err, out)
			}

			schemaPath := filepath.Join(repoRoot, "schemas", "output", tc.schema+".schema.json")
			if err := validateAgainstSchema(schemaPath, parsed); err != nil {
				t.Fatalf("schema %s validation failed: %v\noutput:\n%s",
					tc.schema, err, out)
			}
		})
	}
}

// TestE2E_SchemaConformance_ErrorEnvelope checks that the canonical
// error envelope shape (error.schema.json) is honoured by a sample of
// commands that return structured failures: VALIDATION_ERROR,
// HUMAN_REQUIRED, NODE_NOT_FOUND. Same idea as the success-path test
// above — the error envelope is part of trond's public contract with
// agents, drift here breaks every consumer at once.
func TestE2E_SchemaConformance_ErrorEnvelope(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	schemaPath := filepath.Join(repoRoot, "schemas", "output", "error.schema.json")

	cases := []struct {
		name string
		args []string
	}{
		// Bad intent path — VALIDATION_ERROR.
		{"validate-missing-file",
			[]string{"config", "validate", "/no/such/intent.yaml"}},
		// Inspect with no selector — VALIDATION_ERROR.
		{"inspect-no-selector",
			[]string{"inspect"}},
		// Status of a node that doesn't exist — NODE_NOT_FOUND.
		{"status-missing-node",
			[]string{"status", "definitely-not-deployed"}},
		// Remove without --confirm — HUMAN_REQUIRED.
		{"remove-no-confirm",
			[]string{"remove", "anything"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, env := e2eEnv(t)
			args := append([]string{}, tc.args...)
			args = append(args, "--output", "json")

			out, err := runTrondAllowFail(ctx, t, env, args...)
			if err == nil {
				t.Fatalf("expected non-zero exit; got success with output:\n%s", out)
			}

			var parsed any
			if jsonErr := json.Unmarshal(out, &parsed); jsonErr != nil {
				t.Fatalf("error envelope not valid JSON: %v\nraw:\n%s", jsonErr, out)
			}
			if vErr := validateAgainstSchema(schemaPath, parsed); vErr != nil {
				t.Fatalf("error envelope failed schema: %v\nraw:\n%s", vErr, out)
			}
		})
	}
}

// validateAgainstSchema compiles the schema at schemaPath and
// validates parsed against it. Compiled fresh per call (cheap; the
// library is fast and the alternative — caching — adds complexity
// for negligible gain in test runtime).
func validateAgainstSchema(schemaPath string, parsed any) error {
	c := jsonschema.NewCompiler()
	f, err := os.Open(schemaPath)
	if err != nil {
		return err
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		return err
	}
	if err := c.AddResource(schemaPath, doc); err != nil {
		return err
	}
	sch, err := c.Compile(schemaPath)
	if err != nil {
		return err
	}
	return sch.Validate(parsed)
}
