// Package recipe runs declarative trond multi-step workflows.
//
// A recipe is a YAML document — one of the built-ins embedded from
// files/*.yaml, or any file passed to `recipe run --file` — codifying a
// workflow from AGENTS.md (deploy fresh node, snapshot then apply,
// recover from failed upgrade, etc.). Arguments can reference the
// user-supplied parameters and the outputs of earlier steps via
// {{ params.* }} / {{ steps.<id>.<field> }} substitution.
//
// There are two kinds of step:
//
//   - kind: command (the default) re-execs the trond binary with a
//     subcommand path, and captures its JSON for downstream references.
//     The run's --state-dir and --require-private are forwarded, so a
//     step acts in the same place and under the same safety policy as
//     the run that launched it.
//
//   - kind: host runs a program on the machine running trond. It is the
//     only step that escapes trond's own surface, so it is refused
//     unless --allow-host-exec is passed, and refused outright under
//     --require-private: a host step names no node, so the gate has
//     nothing to check and cannot vouch for it.
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

// Step kinds.
//
// The zero value is KindCommand, and that is load-bearing: it is what
// keeps every recipe written before this field existed parsing and
// running byte-identically.
//
// "host" rather than "exec" or "local": `trond exec <node>` already means
// the opposite of this (run something INSIDE a managed node), and "local"
// collides with `target: {type: local}`, which is about where a node
// lives, not where a step runs.
const (
	KindCommand = "command" // re-exec the trond binary with a subcommand path
	KindHost    = "host"    // run a program on the machine running trond
)

// Step is one unit of work.
type Step struct {
	ID          string `yaml:"id"                   json:"id"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Kind selects the executor: "command" (default) or "host". Never
	// normalised on the wire, so `recipe show -o json` for an existing
	// recipe stays byte-identical to what it printed before.
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"`

	// Run is the argv of a host step: run[0] is the program, one YAML
	// item per token. Deliberately NOT whitespace-split — a path with a
	// space in it would otherwise become two arguments — and deliberately
	// not a shell, so `script:` is the only way to get one and
	// `grep -l 'script:'` is a complete census of shell usage in a
	// recipe tree. Mutually exclusive with Script.
	Run []string `yaml:"run,omitempty" json:"run,omitempty"`

	// Script is a shell body run as `sh -c <script>`. Mutually exclusive
	// with Run. Use it when you need pipes, globs or conditionals;
	// prefer Run when you do not, because argv has no quoting rules to
	// get wrong.
	//
	// Script is NOT substituted, and {{ }} inside it is rejected at load
	// time rather than passed through. Interpolating a value into a shell
	// body is command injection by construction — a param of
	// "; rm -rf /" would execute — and passing the template text through
	// silently is worse still: the script runs, nothing errors, and it
	// operates on the literal string "{{ params.x }}".
	//
	// Reach values through env: instead, which IS substituted and which
	// the shell sees as data rather than code:
	//
	//	env:    {NODE_URL: "{{ params.node_url }}"}
	//	script: curl -sf "$NODE_URL/wallet/getnowblock"
	Script string `yaml:"script,omitempty" json:"script,omitempty"`

	// Dir is the working directory for a host step. Relative paths are
	// resolved against the process's cwd, not the recipe's location —
	// a recipe should not silently depend on where its file happens to
	// sit.
	Dir string `yaml:"dir,omitempty" json:"dir,omitempty"`

	// Env adds environment variables to a host step, on top of the
	// inherited environment. Values go through the same substitution as
	// args.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`

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
