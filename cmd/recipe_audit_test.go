package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/paths"
)

// A recipe run is the most capable single command trond has — since
// `kind: host`, one step can be an arbitrary program — and it was the
// only one that could act and leave nothing in the audit log. Command
// steps re-exec trond, so the child writes its own entry; host steps
// never re-enter trond, and the run itself was never recorded at all.

func writeTempRecipe(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write recipe: %v", err)
	}
	return p
}

// auditLines returns (command, detail, result) for each audit entry.
func auditLines(t *testing.T) [][3]string {
	t.Helper()
	raw, err := os.ReadFile(paths.AuditLog())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read audit log: %v", err)
	}
	var out [][3]string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var e struct{ Command, Detail, Result string }
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("audit line %q: %v", line, err)
		}
		out = append(out, [3]string{e.Command, e.Detail, e.Result})
	}
	return out
}

func setupRecipeAudit(t *testing.T) {
	t.Helper()
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	t.Cleanup(func() {
		recipeFile, recipeAllowHostExec, recipeRunDryRun = "", false, false
		recipeRunParams = nil
	})
}

func TestRecipeRun_AuditsRunAndHostSteps(t *testing.T) {
	setupRecipeAudit(t)
	marker := filepath.Join(t.TempDir(), "ran")
	recipeFile = writeTempRecipe(t, `name: audited
steps:
  - id: stage
    kind: host
    run: ["touch", "`+marker+`"]
`)
	recipeAllowHostExec = true

	if err := runRecipeRun(newCmd(), nil); err != nil {
		t.Fatalf("runRecipeRun: %v", err)
	}

	lines := auditLines(t)
	var host, run [3]string
	for _, l := range lines {
		switch l[0] {
		case "recipe host-step":
			host = l
		case "recipe run":
			run = l
		}
	}
	if host[0] == "" {
		t.Fatalf("no `recipe host-step` entry; a host step ran and the log never saw it.\ngot: %v", lines)
	}
	if !strings.Contains(host[1], "stage") || !strings.Contains(host[1], "touch") {
		t.Errorf("host-step detail = %q, want it to name the step and its program", host[1])
	}
	if run[0] == "" {
		t.Fatalf("no `recipe run` entry.\ngot: %v", lines)
	}
	if run[1] != recipeFile {
		t.Errorf("run detail = %q, want the recipe source %q", run[1], recipeFile)
	}
	if run[2] != "success" {
		t.Errorf("run result = %q, want success", run[2])
	}
}

// TestRecipeRun_RefusedHostStepLeavesNoStepEntry: the audit log records
// what ran. A step refused by --allow-host-exec did not run, so claiming
// it did would be worse than silence — but the run itself is still
// recorded, as a failure.
func TestRecipeRun_RefusedHostStepLeavesNoStepEntry(t *testing.T) {
	setupRecipeAudit(t)
	marker := filepath.Join(t.TempDir(), "ran")
	recipeFile = writeTempRecipe(t, `name: refused
steps:
  - id: stage
    kind: host
    run: ["touch", "`+marker+`"]
`)
	// recipeAllowHostExec stays false.

	if err := runRecipeRun(newCmd(), nil); err == nil {
		t.Fatal("want the refusal to surface as an error")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the refused step ran")
	}

	for _, l := range auditLines(t) {
		if l[0] == "recipe host-step" {
			t.Errorf("audited a host step that never ran: %v", l)
		}
	}
	var found bool
	for _, l := range auditLines(t) {
		if l[0] == "recipe run" {
			found = true
			if l[2] != "error" {
				t.Errorf("run result = %q, want error", l[2])
			}
		}
	}
	if !found {
		t.Error("a failed run left no `recipe run` entry")
	}
}

// TestRecipeRun_DryRunAuditsNoHostStep — a preview executed nothing.
func TestRecipeRun_DryRunAuditsNoHostStep(t *testing.T) {
	setupRecipeAudit(t)
	recipeFile = writeTempRecipe(t, `name: preview
steps:
  - id: stage
    kind: host
    run: ["touch", "/tmp/should-not-happen"]
`)
	recipeAllowHostExec = true
	recipeRunDryRun = true

	if err := runRecipeRun(newCmd(), nil); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	for _, l := range auditLines(t) {
		if l[0] == "recipe host-step" {
			t.Errorf("dry-run audited a host step: %v", l)
		}
	}
}
