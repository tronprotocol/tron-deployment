package recipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalRecipe = `name: probe
description: a probe
steps:
  - id: first
    command: status
    args: ["n0"]
`

func writeRecipe(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write recipe: %v", err)
	}
	return p
}

func TestLoadFile_Minimal(t *testing.T) {
	r, err := LoadFile(writeRecipe(t, minimalRecipe))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if r.Name != "probe" || len(r.Steps) != 1 || r.Steps[0].ID != "first" {
		t.Errorf("parsed recipe = %+v", r)
	}
}

// TestParse_RejectsUnknownFields is the reason the file loader is strict
// where the embedded loader is not. `on_failre: continue` parses clean
// under lenient YAML and silently means "abort" — the recipe does the
// opposite of what it says, at the moment a step has already failed.
func TestParse_RejectsUnknownFields(t *testing.T) {
	body := `name: probe
steps:
  - id: first
    command: status
    on_failre: continue
`
	_, err := Parse([]byte(body))
	if err == nil {
		t.Fatal("want an error for a misspelled field, got nil")
	}
	if !strings.Contains(err.Error(), "on_failre") {
		t.Errorf("error %q should name the offending field", err)
	}
}

// TestParse_RejectsMultiDocument: yaml.Unmarshal returns document 1 and
// says nothing, so a file holding two recipes silently runs half of what
// it contains.
func TestParse_RejectsMultiDocument(t *testing.T) {
	body := minimalRecipe + "---\n" + strings.Replace(minimalRecipe, "probe", "second", 1)
	_, err := Parse([]byte(body))
	if err == nil {
		t.Fatal("want an error for a multi-document recipe file, got nil")
	}
	if !strings.Contains(err.Error(), "more than one YAML document") {
		t.Errorf("error %q should explain that only the first would run", err)
	}
}

func TestParse_Empty(t *testing.T) {
	if _, err := Parse([]byte("")); err == nil {
		t.Fatal("want an error for an empty recipe, got nil")
	}
}

// TestValidate_ReportsEveryProblemAtOnce: a recipe is a sequence of real
// operations, so the caller should learn about all the structural
// problems before the first one runs, not one per attempt.
func TestValidate_ReportsEveryProblemAtOnce(t *testing.T) {
	body := `name: ""
steps:
  - id: ""
    command: ""
    on_failure: rollbck
  - id: dup
    command: status
  - id: dup
    command: status
rollback:
  - id: ""
    command: stop
`
	_, err := Parse([]byte(body))
	if err == nil {
		t.Fatal("want validation errors, got nil")
	}
	for _, want := range []string{
		"name is required",
		"id is required",
		"command is required",
		`on_failure "rollbck"`,
		`duplicate step id "dup"`,
		"rollback[0]",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validation output is missing %q; a caller fixing one problem at a "+
				"time re-runs the recipe once per typo.\ngot:\n%s", want, err)
		}
	}
}

func TestValidate_RequiresSteps(t *testing.T) {
	_, err := Parse([]byte("name: probe\n"))
	if err == nil || !strings.Contains(err.Error(), "steps is required") {
		t.Fatalf("want a steps-required error, got %v", err)
	}
}

// TestValidate_AcceptsEveryEmbeddedRecipe keeps the new strictness honest:
// if a rule here would reject a recipe that ships in the binary, the rule
// is wrong (or the shipped recipe is, which is equally worth knowing).
func TestValidate_AcceptsEveryEmbeddedRecipe(t *testing.T) {
	all, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no embedded recipes found")
	}
	for name, r := range all {
		if err := r.Validate(); err != nil {
			t.Errorf("embedded recipe %q fails the file-loader's validation: %v", name, err)
		}
	}
}

// TestLoadFile_ParseErrorNamesThePath — a validation failure reported
// without the file it came from is useless when a recipe references
// others by path.
func TestLoadFile_ParseErrorNamesThePath(t *testing.T) {
	p := writeRecipe(t, "name: probe\n")
	_, err := LoadFile(p)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), p) {
		t.Errorf("error %q should name %q", err, p)
	}
}

func TestLoadFile_MissingFile(t *testing.T) {
	_, err := LoadFile(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil || !strings.Contains(err.Error(), "read recipe") {
		t.Fatalf("want a read error, got %v", err)
	}
}
