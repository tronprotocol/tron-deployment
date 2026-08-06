// Package recipe runs declarative trond multi-step workflows.
//
// A recipe is a YAML document in recipes/*.yaml that codifies one of
// the canonical workflows from AGENTS.md (deploy fresh node, snapshot
// then apply, recover from failed upgrade, etc.). Each step calls a
// trond subcommand with arguments that can reference the user-supplied
// parameters and outputs from earlier steps via {{ params.* }} /
// {{ steps.<id>.<field> }} template substitution.
//
// The runner re-execs the trond binary itself for each step (no shell
// dependency beyond exec) and captures the JSON output for downstream
// references. This keeps every step idempotent and testable in
// isolation.
package recipe

import "encoding/json"

// Recipe is the parsed YAML document.
//
// JSON tags mirror schemas/output/recipe-show.schema.json so a CLI
// `recipe show --output json` round-trips through the published
// schema. yaml tags drive the source-of-truth (recipes/*.yaml).
type Recipe struct {
	Name        string  `yaml:"name"        json:"name"`
	Description string  `yaml:"description" json:"description"`
	Params      []Param `yaml:"params,omitempty" json:"params,omitempty"`
	Steps       []Step  `yaml:"steps"       json:"steps"`

	// Rollback section runs only when a step with on_failure=rollback
	// triggers it (or when --rollback is passed and the recipe has
	// committed enough state to need cleanup). Steps inside rollback
	// run in order, errors logged but don't abort each other.
	Rollback []Step `yaml:"rollback,omitempty" json:"rollback,omitempty"`
}

// Param describes one user-supplied input. Required params with no
// default cause `recipe run` to fail upfront before any step executes.
type Param struct {
	Name        string `yaml:"name"                 json:"name"`
	Type        string `yaml:"type,omitempty"       json:"type,omitempty"` // string | int | bool | path
	Required    bool   `yaml:"required,omitempty"   json:"required,omitempty"`
	Default     string `yaml:"default,omitempty"    json:"default,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Step is one unit of work. Today only command steps are supported;
// future kinds (poll, sleep, branch) live behind the same struct so
// recipes don't need migration when added.
type Step struct {
	ID          string `yaml:"id"                   json:"id"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Command is the trond subcommand path to invoke, e.g.
	// "config validate", "snapshot download", "apply". Trond's own
	// argv prefix is added by the runner.
	Command string `yaml:"command,omitempty" json:"command,omitempty"`

	// Args are appended after the command. Each value goes through
	// template substitution; references to {{ params.* }} and
	// {{ steps.<id>.<field> }} are resolved at step time, not at
	// recipe-load time.
	Args []string `yaml:"args,omitempty" json:"args,omitempty"`

	// OnFailure decides what happens when the step's exit code is
	// non-zero. Default = "abort" (stop the recipe with the error).
	// "continue" logs and proceeds. "rollback" jumps to the recipe's
	// rollback steps.
	OnFailure string `yaml:"on_failure,omitempty" json:"on_failure,omitempty"`

	// Persist names the JSON fields from this step's stdout that
	// future steps can reference via {{ steps.<id>.<name> }}. We make
	// this explicit (rather than capturing all output) so recipes
	// stay readable about what each step exposes downstream.
	Persist []string `yaml:"persist,omitempty" json:"persist,omitempty"`

	// Skip evaluates a template; if it renders "true" the step is
	// skipped. Used so optional inputs gate optional steps.
	Skip string `yaml:"skip,omitempty" json:"skip,omitempty"`
}

// StepResult records what one step produced. Captured by the runner
// in-memory and used as the {{ steps.<id> }} substitution source.
type StepResult struct {
	ID         string         `json:"id"`
	Skipped    bool           `json:"skipped,omitempty"`
	ExitCode   int            `json:"exit_code"`
	DurationMs int64          `json:"duration_ms"`
	Output     map[string]any `json:"output,omitempty"`
	Error      string         `json:"error,omitempty"`
}

// RunResult is what `recipe run` returns at the end. Stable JSON
// shape so an MCP recipe-runner tool can return it verbatim.
type RunResult struct {
	Recipe string `json:"recipe"`

	// Source records where the recipe came from: "builtin:<name>" or the
	// path passed to --file. Set by the CLI, not the runner — the runner
	// is handed a parsed Recipe and has no idea where it was read from.
	// A run against a file on disk is otherwise indistinguishable in its
	// output from a run of the built-in with the same name.
	Source string `json:"source,omitempty"`

	Status        string       `json:"status"` // success | failed | aborted | rolled_back
	StartedAt     string       `json:"started_at"`
	DurationMs    int64        `json:"duration_ms"`
	Steps         []StepResult `json:"steps"`
	RollbackRan   bool         `json:"rollback_ran,omitempty"`
	RollbackSteps []StepResult `json:"rollback_steps,omitempty"`
	FailedAt      string       `json:"failed_at,omitempty"` // step ID
}

// captureOutput attempts to decode a step's stdout as JSON. Recipes
// only persist fields from steps that emit JSON (every trond -o json
// command does); steps that don't emit JSON return an empty map and
// the persist list is silently empty.
func captureOutput(stdout []byte) map[string]any {
	if len(stdout) == 0 {
		return map[string]any{}
	}
	var v map[string]any
	if err := json.Unmarshal(stdout, &v); err != nil {
		return map[string]any{}
	}
	return v
}
