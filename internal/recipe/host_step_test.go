package recipe

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `kind: host` is the only step that escapes trond's own surface: a
// command step is bounded by what trond will do, and every node-touching
// trond verb is individually gated and audited. A host step is bounded by
// nothing.
//
// So the tests that matter here are the ones that prove it does NOT run.

func hostRecipe(step Step) Recipe {
	step.Kind = KindHost
	if step.ID == "" {
		step.ID = "h1"
	}
	return Recipe{Name: "t", Steps: []Step{step}}
}

func TestHostStep_RunsWhenAllowed(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	r := hostRecipe(Step{Run: []string{"touch", marker}})

	if _, err := Run(context.Background(), r, RunOptions{
		Out: io.Discard, Err: io.Discard, AllowHostExec: true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("host step did not run: %v", err)
	}
}

// TestHostStep_RefusedWithoutOptIn: choosing a recipe file and opting into
// arbitrary execution are separate decisions, and --file makes the file
// come from anywhere.
func TestHostStep_RefusedWithoutOptIn(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	r := hostRecipe(Step{Run: []string{"touch", marker}})

	_, err := Run(context.Background(), r, RunOptions{Out: io.Discard, Err: io.Discard})
	if err == nil {
		t.Fatal("want a refusal without --allow-host-exec, got nil")
	}
	if !strings.Contains(err.Error(), "allow-host-exec") {
		t.Errorf("error %q should name the flag that would permit it", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("the refused step ran anyway")
	}
}

// TestHostStep_RefusedUnderPrivateGate is the load-bearing one.
// --require-private promises an agent is "mechanically incapable of
// mutating a mainnet/nile rig". A host step can delete a mainnet node's
// data directory without ever naming the node, so the gate must refuse
// rather than inspect the command and guess.
func TestHostStep_RefusedUnderPrivateGate(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	r := hostRecipe(Step{Run: []string{"touch", marker}})

	// Even with the explicit opt-in: the gate is a floor, not a default.
	_, err := Run(context.Background(), r, RunOptions{
		Out: io.Discard, Err: io.Discard,
		AllowHostExec: true, RequirePrivate: true,
	})
	if err == nil {
		t.Fatal("want a refusal under --require-private, got nil")
	}
	if !strings.Contains(err.Error(), "require-private") {
		t.Errorf("error %q should say the gate refused it", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("a host step ran with the private gate on; --allow-host-exec must not " +
			"be able to lift the floor")
	}
}

// TestHostStep_DryRunPreviewsUnderBothRefusals: printing what would run
// changes nothing, and the same reasoning already lets `auto-heal
// --dry-run` through the gate.
func TestHostStep_DryRunPreviewsUnderBothRefusals(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	r := hostRecipe(Step{Run: []string{"touch", marker}})

	var out strings.Builder
	if _, err := Run(context.Background(), r, RunOptions{
		Out: &out, Err: io.Discard, DryRun: true, RequirePrivate: true,
	}); err != nil {
		t.Fatalf("dry-run should preview, not refuse: %v", err)
	}
	if !strings.Contains(out.String(), "touch") {
		t.Errorf("dry-run did not show the host command: %q", out.String())
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("dry-run executed the step")
	}
}

func TestHostStep_ScriptGetsAShell(t *testing.T) {
	dir := t.TempDir()
	r := hostRecipe(Step{Script: "echo shell-ran > " + filepath.Join(dir, "out")})

	if _, err := Run(context.Background(), r, RunOptions{
		Out: io.Discard, Err: io.Discard, AllowHostExec: true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "out"))
	if err != nil || strings.TrimSpace(string(got)) != "shell-ran" {
		t.Errorf("script step produced %q, %v", got, err)
	}
}

// TestHostStep_RunIsArgvNotShell: run: is argv, so shell metacharacters
// are inert. If they were not, "run" and "script" would differ only in
// spelling and the grep-for-script census would be worthless.
func TestHostStep_RunIsArgvNotShell(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "should-not-exist")
	r := hostRecipe(Step{Run: []string{"echo", "hello > " + sentinel}})

	if _, err := Run(context.Background(), r, RunOptions{
		Out: io.Discard, Err: io.Discard, AllowHostExec: true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Error("a redirection inside an argv item was interpreted by a shell")
	}
}

func TestHostStep_SubstitutesArgsAndEnv(t *testing.T) {
	dir := t.TempDir()
	r := Recipe{
		Name:   "t",
		Params: []Param{{Name: "target", Required: true}},
		Steps: []Step{{
			ID: "h1", Kind: KindHost,
			Script: `printf '%s' "$WHERE" > ` + filepath.Join(dir, "out"),
			Env:    map[string]string{"WHERE": "{{ params.target }}"},
		}},
	}
	if _, err := Run(context.Background(), r, RunOptions{
		Out: io.Discard, Err: io.Discard, AllowHostExec: true,
		Params: map[string]string{"target": "substituted"},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "out"))
	if string(got) != "substituted" {
		t.Errorf("env value = %q, want %q", got, "substituted")
	}
}

func TestHostStep_DirIsHonoured(t *testing.T) {
	dir := t.TempDir()
	r := hostRecipe(Step{Run: []string{"touch", "made-here"}, Dir: dir})

	if _, err := Run(context.Background(), r, RunOptions{
		Out: io.Discard, Err: io.Discard, AllowHostExec: true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "made-here")); err != nil {
		t.Errorf("dir: was not applied: %v", err)
	}
}

func TestValidate_HostStepShapes(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			"neither run nor script",
			"name: t\nsteps:\n  - id: a\n    kind: host\n",
			"needs run or script",
		},
		{
			"both run and script",
			"name: t\nsteps:\n  - id: a\n    kind: host\n    run: [\"echo\"]\n    script: echo hi\n",
			"not both",
		},
		{
			"command on a host step",
			"name: t\nsteps:\n  - id: a\n    kind: host\n    run: [\"echo\"]\n    command: status\n",
			"a host step uses run or script",
		},
		{
			"host fields on a command step",
			"name: t\nsteps:\n  - id: a\n    command: status\n    run: [\"echo\"]\n",
			"belong to `kind: host` steps",
		},
		{
			"unknown kind",
			"name: t\nsteps:\n  - id: a\n    kind: sidecar\n    command: status\n",
			`kind "sidecar" is not one of command, host`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("want an error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestValidate_CommandStepsUnchanged: the zero Kind must keep meaning
// "command", or every recipe written before this field existed breaks.
func TestValidate_CommandStepsUnchanged(t *testing.T) {
	if _, err := Parse([]byte(minimalRecipe)); err != nil {
		t.Fatalf("a recipe with no kind: field must still parse: %v", err)
	}
}

// TestValidate_ScriptRejectsTemplateSyntax covers a defect shipped in the
// kind: host change: script is not substituted, so `{{ params.x }}` inside
// it reached the shell as literal text. The step ran, nothing errored, and
// it operated on the template string — a recipe whose await-loop curled a
// literal "{{ params.node_url }}" span until the caller gave up.
//
// Substituting into a shell body is not the fix: a param of "; rm -rf /"
// would execute. env: is substituted and the shell sees it as data, so the
// trap becomes a load-time error pointing there.
func TestValidate_ScriptRejectsTemplateSyntax(t *testing.T) {
	body := "name: t\nsteps:\n  - id: a\n    kind: host\n" +
		"    script: curl -sf {{ params.node_url }}/health\n"
	_, err := Parse([]byte(body))
	if err == nil {
		t.Fatal("want {{ }} in a script rejected, got nil — it would have run as literal text")
	}
	for _, want := range []string{"script is not substituted", "env:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %v should contain %q so the author knows what to do instead", err, want)
		}
	}
}

// TestHostStep_EnvIsTheSubstitutionChannel is the other half: the escape
// hatch the error points at has to actually work.
func TestHostStep_EnvIsTheSubstitutionChannel(t *testing.T) {
	dir := t.TempDir()
	r := Recipe{
		Name:   "t",
		Params: []Param{{Name: "target", Required: true}},
		Steps: []Step{{
			ID: "h1", Kind: KindHost,
			Env:    map[string]string{"TARGET": "{{ params.target }}"},
			Script: `printf '%s' "$TARGET" > ` + filepath.Join(dir, "out"),
		}},
	}
	if _, err := Run(context.Background(), r, RunOptions{
		Out: io.Discard, Err: io.Discard, AllowHostExec: true,
		Params: map[string]string{"target": "http://example:8090"},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "out"))
	if string(got) != "http://example:8090" {
		t.Errorf("script saw %q; env: must carry substituted values into the shell", got)
	}
}

// TestHostStep_DirIsSubstituted: dir: was shipped unsubstituted alongside
// script:, but the right answer differs. cmd.Dir is a path handed to
// chdir — a hostile value can only fail to open — so it substitutes,
// where a script body is code and does not.
func TestHostStep_DirIsSubstituted(t *testing.T) {
	dir := t.TempDir()
	r := Recipe{
		Name:   "t",
		Params: []Param{{Name: "workdir", Required: true}},
		Steps: []Step{{
			ID: "h1", Kind: KindHost,
			Dir: "{{ params.workdir }}",
			Run: []string{"touch", "made-here"},
		}},
	}
	if _, err := Run(context.Background(), r, RunOptions{
		Out: io.Discard, Err: io.Discard, AllowHostExec: true,
		Params: map[string]string{"workdir": dir},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "made-here")); err != nil {
		t.Errorf("dir: was not substituted — the step would chdir to a literal "+
			"{{ }} path: %v", err)
	}
}
