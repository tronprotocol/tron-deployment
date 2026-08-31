package recipe

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEmbedded_AllParseAndHaveMandatoryFields(t *testing.T) {
	all, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	if len(all) < 5 {
		t.Errorf("expected at least 5 recipes, got %d", len(all))
	}
	for name, r := range all {
		t.Run(name, func(t *testing.T) {
			if r.Name == "" {
				t.Error("Name field empty")
			}
			if r.Description == "" {
				t.Error("Description empty — recipes must explain themselves")
			}
			if len(r.Steps) == 0 {
				t.Error("no steps")
			}
			seenIDs := map[string]bool{}
			for i, s := range r.Steps {
				if s.ID == "" {
					t.Errorf("step %d has empty ID", i)
				}
				if seenIDs[s.ID] {
					t.Errorf("duplicate step ID %q", s.ID)
				}
				seenIDs[s.ID] = true
				if s.Command == "" {
					t.Errorf("step %s has no command", s.ID)
				}
				if s.OnFailure != "" &&
					s.OnFailure != "abort" &&
					s.OnFailure != "continue" &&
					s.OnFailure != "rollback" {
					t.Errorf("step %s: invalid on_failure %q", s.ID, s.OnFailure)
				}
			}
		})
	}
}

func TestNames_SortedAndUnique(t *testing.T) {
	names, err := Names()
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("Names not sorted: %s before %s", names[i-1], names[i])
		}
		if names[i-1] == names[i] {
			t.Errorf("duplicate name %s", names[i])
		}
	}
}

func TestSubstitute_ParamsAndSteps(t *testing.T) {
	params := map[string]string{"node_name": "my-node", "intent_path": "intent.yaml"}
	steps := map[string]map[string]any{
		"download": {"job_id": "20260501-123-abcd"},
	}

	cases := []struct {
		in, want string
	}{
		{"{{ params.node_name }}", "my-node"},
		{"--intent {{ params.intent_path }}", "--intent intent.yaml"},
		{"{{ steps.download.job_id }}", "20260501-123-abcd"},
		{"plain string with no template", "plain string with no template"},
		{"mixed {{ params.node_name }} and steps {{ steps.download.job_id }}",
			"mixed my-node and steps 20260501-123-abcd"},
	}
	for _, c := range cases {
		got, err := substitute(c.in, params, steps)
		if err != nil {
			t.Errorf("substitute(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("substitute(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSubstitute_MissingParamErrors(t *testing.T) {
	_, err := substitute("{{ params.unknown }}", map[string]string{}, nil)
	if err == nil {
		t.Fatal("expected error for unknown param reference, got nil")
	}
}

func TestResolveParams_RequiredAndDefaults(t *testing.T) {
	declared := []Param{
		{Name: "node", Required: true},
		{Name: "version", Default: "latest"},
	}
	// Missing required.
	if _, err := resolveParams(declared, map[string]string{}); err == nil {
		t.Error("expected error when required param missing")
	}
	// Required supplied + default applied.
	got, err := resolveParams(declared, map[string]string{"node": "n1"})
	if err != nil {
		t.Fatal(err)
	}
	if got["node"] != "n1" || got["version"] != "latest" {
		t.Errorf("unexpected params: %+v", got)
	}
	// Unknown param rejected.
	if _, err := resolveParams(declared, map[string]string{"node": "n1", "typo": "x"}); err == nil {
		t.Error("expected error for unknown param 'typo'")
	}
}

func TestRun_AbortsOnFirstFailure(t *testing.T) {
	// Use /usr/bin/false as the per-step "binary" so every step
	// non-zero-exits. We're testing the runner's failure handling, not
	// any real trond behavior.
	if runtime.GOOS == "windows" {
		t.Skip("test relies on POSIX /usr/bin/false")
	}
	r := Recipe{
		Name: "test",
		Steps: []Step{
			{ID: "first", Command: "anything"},
			{ID: "second", Command: "more"},
		},
	}
	out := &bytes.Buffer{}
	res, err := Run(context.Background(), r, RunOptions{
		Binary: "/usr/bin/false",
		Out:    out,
		Err:    out,
	})
	if err == nil {
		t.Fatal("expected error from failing step")
	}
	if res.Status != "failed" {
		t.Errorf("Status = %s, want failed", res.Status)
	}
	if res.FailedAt != "first" {
		t.Errorf("FailedAt = %s, want first", res.FailedAt)
	}
	if len(res.Steps) != 1 {
		t.Errorf("expected only first step recorded, got %d", len(res.Steps))
	}
}

func TestRun_ContinueLetsNextStepRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	r := Recipe{
		Name: "test",
		Steps: []Step{
			{ID: "first", Command: "anything", OnFailure: "continue"},
			{ID: "second", Command: "more", OnFailure: "continue"},
		},
	}
	out := &bytes.Buffer{}
	res, err := Run(context.Background(), r, RunOptions{
		Binary: "/usr/bin/false",
		Out:    out,
		Err:    out,
	})
	if err != nil {
		t.Fatalf("expected nil err with continue, got %v", err)
	}
	if len(res.Steps) != 2 {
		t.Errorf("expected both steps recorded, got %d", len(res.Steps))
	}
}

func TestRun_RollbackTriggersOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	r := Recipe{
		Name: "test",
		Steps: []Step{
			{ID: "first", Command: "anything", OnFailure: "rollback"},
		},
		Rollback: []Step{
			{ID: "cleanup", Command: "more", OnFailure: "continue"},
		},
	}
	out := &bytes.Buffer{}
	res, err := Run(context.Background(), r, RunOptions{
		Binary: "/usr/bin/false",
		Out:    out,
		Err:    out,
	})
	if err == nil {
		t.Fatal("rollback path should still surface the original error")
	}
	if res.Status != "rolled_back" {
		t.Errorf("Status = %s, want rolled_back", res.Status)
	}
	if !res.RollbackRan {
		t.Error("RollbackRan should be true")
	}
	if len(res.RollbackSteps) != 1 {
		t.Errorf("expected 1 rollback step recorded, got %d", len(res.RollbackSteps))
	}
}

func TestRun_DryRunSkipsExec(t *testing.T) {
	r := Recipe{
		Name: "test",
		Steps: []Step{
			{ID: "first", Command: "should not execute"},
		},
	}
	out := &bytes.Buffer{}
	res, err := Run(context.Background(), r, RunOptions{
		Binary: "/path/that/does/not/exist",
		DryRun: true,
		Out:    out,
		Err:    out,
	})
	if err != nil {
		t.Fatalf("dry-run should never error: %v", err)
	}
	if res.Status != "success" {
		t.Errorf("dry-run status = %s, want success", res.Status)
	}
	if !strings.Contains(out.String(), "would run") {
		t.Errorf("dry-run output should contain 'would run', got: %s", out.String())
	}
}

func TestRun_StartFailureDoesNotPanicOnNilProcessState(t *testing.T) {
	r := Recipe{Name: "start-failure", Steps: []Step{{ID: "first", Command: "anything"}}}
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("run panicked: %v", recovered)
		}
	}()
	res, err := Run(context.Background(), r, RunOptions{Binary: "/path/does/not/exist", Out: io.Discard, Err: io.Discard})
	if err == nil {
		t.Fatal("expected start error")
	}
	if len(res.Steps) != 1 || res.Steps[0].ExitCode != 0 {
		t.Fatalf("result=%+v", res)
	}
}

func TestRun_ResumeFromSkipsEarlierSteps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	r := Recipe{
		Name: "test",
		Steps: []Step{
			{ID: "first", Command: "anything"},
			{ID: "second", Command: "more"},
			{ID: "third", Command: "more"},
		},
	}
	out := &bytes.Buffer{}
	res, _ := Run(context.Background(), r, RunOptions{
		Binary:     "/usr/bin/false",
		ResumeFrom: "third",
		Out:        out,
		Err:        out,
	})
	if len(res.Steps) != 3 {
		t.Fatalf("expected 3 step records, got %d", len(res.Steps))
	}
	if !res.Steps[0].Skipped || !res.Steps[1].Skipped {
		t.Error("first and second should be skipped")
	}
	if res.Steps[2].Skipped {
		t.Error("third should NOT be skipped")
	}
}

func TestRun_SkipPredicateGatesStep(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	r := Recipe{
		Name: "test",
		Params: []Param{
			{Name: "skip_first", Default: "false"},
		},
		Steps: []Step{
			{ID: "first", Command: "anything", Skip: "{{ params.skip_first }}"},
			{ID: "second", Command: "anything"},
		},
	}
	out := &bytes.Buffer{}

	// skip_first=true → first should be skipped, second should run (and fail
	// against /usr/bin/false).
	res, _ := Run(context.Background(), r, RunOptions{
		Binary: "/usr/bin/false",
		Params: map[string]string{"skip_first": "true"},
		Out:    out, Err: out,
	})
	if !res.Steps[0].Skipped {
		t.Errorf("first step should be skipped when skip evaluates 'true', got %+v", res.Steps[0])
	}

	// skip_first=false → both run; first hits /usr/bin/false and aborts.
	out.Reset()
	res, _ = Run(context.Background(), r, RunOptions{
		Binary: "/usr/bin/false",
		Params: map[string]string{"skip_first": "false"},
		Out:    out, Err: out,
	})
	if res.Steps[0].Skipped {
		t.Errorf("first step should NOT be skipped when skip evaluates 'false'")
	}
}

// Integration-style: verify the YAML on disk in recipes/ matches the
// embedded copy in internal/recipe/files/. Catches a merge that
// updates one but forgets the other.
//
// Assumes recipe_test.go remains at internal/recipe/recipe_test.go;
// if the test moves, update the relative paths here.
func TestEmbedded_MatchesPublicCopy(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	publicDir := filepath.Join(repoRoot, "recipes")
	embedDir := filepath.Join(repoRoot, "internal", "recipe", "files")

	publicEntries, err := os.ReadDir(publicDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range publicEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		pub, err := os.ReadFile(filepath.Join(publicDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		emb, err := os.ReadFile(filepath.Join(embedDir, e.Name()))
		if err != nil {
			t.Errorf("%s: missing from internal/recipe/files/", e.Name())
			continue
		}
		if string(pub) != string(emb) {
			t.Errorf("%s: public copy and embedded copy diverged", e.Name())
		}
	}
}
