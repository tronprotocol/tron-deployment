package recipe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// RunOptions configures a single recipe run.
type RunOptions struct {
	// Binary is the trond executable to invoke for each step. Defaults
	// to os.Args[0]; tests pass a fake binary that emits canned JSON.
	Binary string

	// Params is the user-supplied param map. Keys must match
	// recipe.Param.Name. Required params with no default are validated
	// before any step runs.
	Params map[string]string

	// DryRun prints the resolved command for each step without
	// executing. Useful for "show me what this would do".
	DryRun bool

	// ResumeFrom skips every step before the named ID. The skipped
	// steps' outputs are NOT rebuilt — stepsState starts empty for the
	// resumed run. Resumed steps that reference {{ steps.<earlier>.X }}
	// will fail loudly with a "missing key" template error; recipes
	// designed for resume must either avoid such references or pass
	// the previously-computed values through --param.
	ResumeFrom string

	// Out / Err receive human-readable progress lines. Recipe runs
	// always log structured progress to stderr in addition to the
	// final RunResult.
	Out io.Writer
	Err io.Writer

	// StateDir is the parent's resolved state directory, forwarded to
	// every step. Without it a run started with --state-dir reads its
	// recipe from one state and executes every step against ~/.trond —
	// two different states, with nothing in the output to say so.
	StateDir string

	// RequirePrivate mirrors guard.Requested() in the parent.
	//
	// The gate is `flag OR inherited env` (internal/guard). The env form
	// crosses into a re-exec'd step for free; the flag form is a
	// process-local package var and does not. So without this,
	// `trond --require-private recipe run X` runs every step ungated,
	// while the identical run started with TROND_REQUIRE_PRIVATE=1 is
	// gated — the safety floor would depend on which way you asked for it.
	RequirePrivate bool

	// AllowHostExec permits `kind: host` steps, which run arbitrary
	// programs on the machine running trond rather than trond
	// subcommands. Off by default: every other step kind is bounded by
	// what trond itself will do (and each of those is individually gated
	// and audited), while a host step is bounded by nothing. Requiring
	// the caller to say so makes "this run may execute arbitrary
	// programs" an explicit, greppable decision rather than a property
	// of whichever file was passed to --file.
	AllowHostExec bool

	// AuditHostStep, when set, is called after each host step that
	// actually ran (not one refused or previewed).
	//
	// Host steps are the only kind nothing else records. A command step
	// re-execs trond, and that child writes its own audit entry under its
	// own verb — so the audit log already sees it, at the granularity of
	// the verb. A host step never enters trond again, which would leave
	// the most capable thing a recipe can do as the one thing the log
	// never sees.
	//
	// A callback rather than a direct write: the audit log's location and
	// policy belong to the CLI, and internal/recipe should not grow an
	// opinion about either.
	AuditHostStep func(step Step, res StepResult)

	// RunID is a correlation id exported into every step's environment,
	// so the audit entries a command step writes as a child process can
	// be tied back to the run that caused them. Empty means "do not set
	// it", which keeps a direct Run() call from inventing one.
	RunID string

	// RunIDEnv names the environment variable RunID travels in. The CLI
	// owns the name; internal/recipe should not hard-code a convention
	// that belongs to the audit log.
	RunIDEnv string
}

// childEnv returns the environment for a step's child process: the
// inherited environment, plus the correlation id, plus any step-level
// overrides. Returns nil when there is nothing to add, so exec inherits
// the parent environment unchanged.
func (o RunOptions) childEnv(extra map[string]string) []string {
	if o.RunID == "" || o.RunIDEnv == "" {
		if len(extra) == 0 {
			return nil
		}
	}
	env := os.Environ()
	if o.RunID != "" && o.RunIDEnv != "" {
		env = append(env, o.RunIDEnv+"="+o.RunID)
	}
	for _, k := range sortedEnvKeys(extra) {
		env = append(env, k+"="+extra[k])
	}
	return env
}

// Run executes a recipe. Returns a RunResult plus error; the error is
// nil on a clean run, non-nil whenever any step's on_failure abort
// fires or when params validation fails.
func Run(ctx context.Context, r Recipe, opts RunOptions) (*RunResult, error) {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.Err == nil {
		opts.Err = os.Stderr
	}
	if opts.Binary == "" {
		opts.Binary = os.Args[0]
	}

	resolved, err := resolveParams(r.Params, opts.Params)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	result := &RunResult{
		Recipe:    r.Name,
		StartedAt: start.UTC().Format(time.RFC3339),
		Status:    "success",
	}
	stepsState := map[string]map[string]any{}
	skipping := opts.ResumeFrom != ""

	for _, step := range r.Steps {
		if skipping {
			if step.ID == opts.ResumeFrom {
				skipping = false
			} else {
				result.Steps = append(result.Steps, StepResult{
					ID:      step.ID,
					Skipped: true,
				})
				continue
			}
		}

		// Skip predicate: render the template, treat literal "true"
		// (case-insensitive) as "skip this step". We evaluate even on
		// dry-run so the planned chain matches the real chain.
		if step.Skip != "" {
			renderedSkip, err := substitute(step.Skip, resolved, stepsState)
			if err != nil {
				return result, fmt.Errorf("step %s: skip template: %w", step.ID, err)
			}
			if strings.EqualFold(strings.TrimSpace(renderedSkip), "true") {
				fmt.Fprintf(opts.Err, "  [%s] skipped (skip-condition true)\n", step.ID)
				result.Steps = append(result.Steps, StepResult{ID: step.ID, Skipped: true})
				continue
			}
		}

		// A host step's arguments live in run[1:] (run[0] is the program
		// and is never templated — a substituted program name would make
		// "what does this recipe execute" unanswerable by reading it).
		toSubstitute := step.Args
		if step.Kind == KindHost && len(step.Run) > 1 {
			toSubstitute = step.Run[1:]
		}
		args, err := substituteAll(toSubstitute, resolved, stepsState)
		if err != nil {
			return result, fmt.Errorf("step %s: %w", step.ID, err)
		}

		st := step
		// dir: is substituted; script: is not. The difference is where the
		// value lands: cmd.Dir is a path handed to chdir, where a hostile
		// value can only fail to open, while a script body is code. Both
		// were unsubstituted at first, and the failures looked nothing
		// alike — the script silently ran on literal "{{ ... }}" text,
		// the dir failed loudly at chdir.
		if step.Dir != "" {
			sd, err := substitute(step.Dir, resolved, stepsState)
			if err != nil {
				return result, fmt.Errorf("step %s: dir: %w", step.ID, err)
			}
			st.Dir = sd
		}
		if len(step.Env) > 0 {
			st.Env = make(map[string]string, len(step.Env))
			for k, v := range step.Env {
				sv, err := substitute(v, resolved, stepsState)
				if err != nil {
					return result, fmt.Errorf("step %s: env %s: %w", step.ID, k, err)
				}
				st.Env[k] = sv
			}
		}

		stepResult, err := runStep(ctx, opts, st, args)
		result.Steps = append(result.Steps, stepResult)

		// Persist BEFORE the failure switch. Rollback steps exist to clean
		// up after a failed step and routinely need that step's output —
		// the shipped fresh-mainnet recipe's rollback references
		// {{ steps.apply.name }}, and its own comment says rollback runs
		// only when apply fails. Persisting after the switch meant the one
		// case the reference was written for was the one case where the
		// key did not exist. `on_failure: continue` lost it the same way.
		persistStep(stepsState, step, stepResult)

		if err != nil {
			switch step.OnFailure {
			case "continue":
				fmt.Fprintf(opts.Err, "  [%s] failed (continuing): %v\n", step.ID, err)
				continue
			case "rollback":
				result.Status = "rolled_back"
				result.FailedAt = step.ID
				result.RollbackRan = true
				fmt.Fprintf(opts.Err, "  [%s] failed; running rollback\n", step.ID)
				rolled := runRollback(ctx, opts, r.Rollback, resolved, stepsState)
				result.RollbackSteps = rolled
				result.DurationMs = time.Since(start).Milliseconds()
				return result, fmt.Errorf("step %s failed: %w", step.ID, err)
			default: // "abort" / unset
				result.Status = "failed"
				result.FailedAt = step.ID
				result.DurationMs = time.Since(start).Milliseconds()
				return result, fmt.Errorf("step %s failed: %w", step.ID, err)
			}
		}

	}

	// A --resume-from that matched nothing skipped every step and returned
	// "success" with no work done. A typo'd step ID is the likely cause,
	// and reporting it as a clean run is the worst possible answer.
	if skipping {
		result.Status = "failed"
		result.DurationMs = time.Since(start).Milliseconds()
		return result, fmt.Errorf("--resume-from %q matched no step in recipe %q (steps: %s)",
			opts.ResumeFrom, r.Name, stepIDs(r.Steps))
	}

	result.DurationMs = time.Since(start).Milliseconds()
	return result, nil
}

// persistStep records the step's output for {{ steps.<id>.<field> }}
// substitution, honouring an explicit persist list when present.
func persistStep(state map[string]map[string]any, step Step, res StepResult) {
	if len(step.Persist) > 0 && res.Output != nil {
		persisted := map[string]any{}
		for _, k := range step.Persist {
			if v, ok := res.Output[k]; ok {
				persisted[k] = v
			}
		}
		state[step.ID] = persisted
		return
	}
	state[step.ID] = res.Output
}

func stepIDs(steps []Step) string {
	ids := make([]string, 0, len(steps))
	for _, s := range steps {
		ids = append(ids, s.ID)
	}
	return strings.Join(ids, ", ")
}

// runStep handles a single step's exec + output capture.
func runStep(ctx context.Context, opts RunOptions, step Step, args []string) (StepResult, error) {
	if step.Kind == KindHost {
		return runHostStep(ctx, opts, step, args)
	}

	res := StepResult{ID: step.ID}
	if step.Command == "" {
		res.Error = "step has no command"
		return res, errors.New(res.Error)
	}

	// Global flags go immediately after the subcommand path, never at the
	// end. A step's own args may contain "--" (every `exec` step does),
	// and anything after it belongs to the inner command: appending
	// "--output json" there passed those two tokens to the program being
	// exec'd AND left trond itself in text mode. Cobra accepts persistent
	// flags anywhere before the "--", so this position is both correct
	// and the only one that stays correct for every step.
	full := strings.Fields(step.Command)
	full = append(full, "--output", "json")
	if opts.StateDir != "" {
		full = append(full, "--state-dir", opts.StateDir)
	}
	if opts.RequirePrivate {
		full = append(full, "--require-private")
	}
	full = append(full, args...)

	if opts.DryRun {
		fmt.Fprintf(opts.Out, "  [%s] would run: %s %s\n", step.ID, opts.Binary, strings.Join(full, " "))
		return res, nil
	}

	fmt.Fprintf(opts.Err, "  [%s] %s %s\n", step.ID, opts.Binary, strings.Join(full, " "))
	start := time.Now()

	cmd := exec.CommandContext(ctx, opts.Binary, full...)
	cmd.Env = opts.childEnv(nil)
	cmd.Stderr = opts.Err
	stdout, err := cmd.Output()
	res.DurationMs = time.Since(start).Milliseconds()
	res.ExitCode = cmd.ProcessState.ExitCode()
	res.Output = captureOutput(stdout)

	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	return res, nil
}

// runRollback executes every rollback step in order, logging but not
// short-circuiting on failures. Rollback is "best effort cleanup".
func runRollback(ctx context.Context, opts RunOptions, steps []Step, params map[string]string, state map[string]map[string]any) []StepResult {
	out := make([]StepResult, 0, len(steps))
	for _, s := range steps {
		args, err := substituteAll(s.Args, params, state)
		if err != nil {
			out = append(out, StepResult{ID: s.ID, Error: err.Error()})
			continue
		}
		res, err := runStep(ctx, opts, s, args)
		if err != nil {
			fmt.Fprintf(opts.Err, "  rollback [%s] failed (continuing): %v\n", s.ID, err)
		}
		out = append(out, res)
	}
	return out
}

// resolveParams validates user inputs against the recipe's declared
// params and applies defaults. Missing required params are an error
// before any step runs.
func resolveParams(declared []Param, supplied map[string]string) (map[string]string, error) {
	out := map[string]string{}
	declaredByName := map[string]Param{}
	for _, p := range declared {
		declaredByName[p.Name] = p
		if v, ok := supplied[p.Name]; ok {
			out[p.Name] = v
			continue
		}
		if p.Default != "" {
			out[p.Name] = p.Default
			continue
		}
		if p.Required {
			return nil, fmt.Errorf("required param %q not supplied", p.Name)
		}
	}
	// Surface unrecognised params explicitly — silently ignoring them
	// hides typos like --param node-name=... when the recipe declares
	// node_name.
	for k := range supplied {
		if _, ok := declaredByName[k]; !ok {
			return nil, fmt.Errorf("unknown param %q (recipe declares: %s)", k, paramNames(declared))
		}
	}
	return out, nil
}

func paramNames(ps []Param) string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

// runHostStep runs a program on the machine running trond.
//
// Two refusals happen here rather than at load time, because both depend
// on how the run was invoked rather than on what the recipe says:
//
//   - Without AllowHostExec the step does not run. A recipe file is
//     content, and --file makes it content from anywhere; the operator
//     opting into arbitrary execution is a separate decision from
//     choosing the file.
//
//   - Under the private gate it does not run either, and that one is not
//     negotiable. `--require-private` promises an agent is "mechanically
//     incapable of mutating a mainnet/nile rig" (AGENTS.md). A host step
//     can delete a mainnet node's data directory without ever naming the
//     node, so honouring that promise means refusing, not inspecting the
//     command and guessing. The gate has no node to check here — which is
//     precisely why it must refuse rather than allow.
//
// --dry-run still previews host steps under either refusal: printing what
// would run changes nothing, and the same reasoning already lets
// `auto-heal --dry-run` through the gate.
func runHostStep(ctx context.Context, opts RunOptions, step Step, args []string) (StepResult, error) {
	res := StepResult{ID: step.ID}

	var argv []string
	switch {
	case len(step.Run) > 0:
		argv = append([]string{step.Run[0]}, args...)
	case step.Script != "":
		argv = []string{"sh", "-c", step.Script}
	default:
		res.Error = "host step has neither run nor script"
		return res, errors.New(res.Error)
	}

	if opts.DryRun {
		fmt.Fprintf(opts.Out, "  [%s] would run on host: %s\n", step.ID, strings.Join(argv, " "))
		return res, nil
	}
	if opts.RequirePrivate {
		res.Error = "host step refused: --require-private is set, and a host step runs " +
			"outside any node's network so the gate cannot vouch for it"
		return res, errors.New(res.Error)
	}
	if !opts.AllowHostExec {
		res.Error = "host step refused: pass --allow-host-exec to permit `kind: host` steps, " +
			"which run arbitrary programs on this machine"
		return res, errors.New(res.Error)
	}

	fmt.Fprintf(opts.Err, "  [%s] host: %s\n", step.ID, strings.Join(argv, " "))
	start := time.Now()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = step.Dir
	cmd.Stderr = opts.Err
	cmd.Env = opts.childEnv(step.Env)
	stdout, err := cmd.Output()
	res.DurationMs = time.Since(start).Milliseconds()
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if opts.AuditHostStep != nil {
		if err != nil {
			res.Error = err.Error()
		}
		opts.AuditHostStep(step, res)
	}
	// A host step is not obliged to emit JSON; captureOutput returns an
	// empty map when it does not, and one that does emit JSON feeds
	// {{ steps.<id>.<field> }} exactly like a command step.
	res.Output = captureOutput(stdout)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	return res, nil
}

func sortedEnvKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
