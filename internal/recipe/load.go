package recipe

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadFile reads a recipe from disk for `recipe run --file` / `--show`.
//
// Parsing is strict where the embedded loader is lenient, because the two
// have different failure modes. An embedded recipe is reviewed at build
// time and its tests run in CI; a file handed to --file is typed by
// someone who wants it to run *now*, and a silently-ignored field there
// means the run does something other than what the file says. A recipe is
// also a sequence of real operations against real nodes: the expensive
// place to discover a typo is step four, half-applied.
func LoadFile(path string) (Recipe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Recipe{}, fmt.Errorf("read recipe %s: %w", path, err)
	}
	r, err := Parse(data)
	if err != nil {
		return Recipe{}, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// Parse decodes recipe YAML strictly and validates it.
func Parse(data []byte) (Recipe, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	// Unknown fields are errors: `on_failre: continue` would otherwise
	// parse clean and silently mean "abort".
	dec.KnownFields(true)

	var r Recipe
	if err := dec.Decode(&r); err != nil {
		if errors.Is(err, io.EOF) {
			return Recipe{}, errors.New("recipe is empty")
		}
		return Recipe{}, fmt.Errorf("parse: %w", err)
	}

	// A second document would be dropped on the floor. yaml.Unmarshal
	// returns document 1 and says nothing, so a file holding two recipes
	// runs half of what it contains.
	var extra Recipe
	if err := dec.Decode(&extra); err == nil {
		return Recipe{}, errors.New("recipe file holds more than one YAML document; " +
			"only the first would run — split them into separate files")
	} else if !errors.Is(err, io.EOF) {
		return Recipe{}, fmt.Errorf("parse (document 2): %w", err)
	}

	if err := r.Validate(); err != nil {
		return Recipe{}, err
	}
	return r, nil
}

// Validate checks the structural rules a recipe must satisfy before any
// step runs. Every violation here is one that would otherwise surface
// mid-run, after earlier steps have already changed something.
func (r Recipe) Validate() error {
	var problems []string

	if strings.TrimSpace(r.Name) == "" {
		problems = append(problems, "name is required")
	}
	if len(r.Steps) == 0 {
		problems = append(problems, "steps is required and must not be empty")
	}

	declared := map[string]bool{}
	for _, p := range r.Params {
		if strings.TrimSpace(p.Name) == "" {
			problems = append(problems, "a param has no name")
			continue
		}
		if declared[p.Name] {
			problems = append(problems, fmt.Sprintf("duplicate param %q", p.Name))
		}
		declared[p.Name] = true
	}

	problems = append(problems, validateSteps("steps", r.Steps)...)
	problems = append(problems, validateSteps("rollback", r.Rollback)...)

	// Step IDs must be unique within `steps`: they are the key for
	// {{ steps.<id>.* }} and for --resume-from, and a duplicate silently
	// makes the later one win.
	seen := map[string]bool{}
	for _, s := range r.Steps {
		if s.ID == "" {
			continue // already reported by validateSteps
		}
		if seen[s.ID] {
			problems = append(problems, fmt.Sprintf("duplicate step id %q", s.ID))
		}
		seen[s.ID] = true
	}
	for _, section := range []struct {
		name  string
		steps []Step
	}{{"steps", r.Steps}, {"rollback", r.Rollback}} {
		for _, s := range section.steps {
			if s.ID != "" && !recipeIDPattern.MatchString(s.ID) {
				problems = append(problems, fmt.Sprintf("%s step id %q is invalid; use only [A-Za-z_][A-Za-z0-9_]* for {{ steps.<id>.* }} references", section.name, s.ID))
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid recipe:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

var recipeIDPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validOnFailure mirrors the runner's switch. Anything else falls to the
// default there, which is "abort" — so `on_failure: rollbck` would abort
// a recipe that was written to roll back.
var validOnFailure = map[string]bool{"": true, "abort": true, "continue": true, "rollback": true}

func validateSteps(section string, steps []Step) []string {
	var problems []string
	for i, s := range steps {
		where := fmt.Sprintf("%s[%d]", section, i)
		if s.ID != "" {
			where = fmt.Sprintf("%s (%s)", where, s.ID)
		}
		if strings.TrimSpace(s.ID) == "" {
			problems = append(problems, where+": id is required")
		}
		switch s.Kind {
		case "", KindCommand:
			if strings.TrimSpace(s.Command) == "" {
				problems = append(problems, where+": command is required")
			}
			if len(s.Run) > 0 || s.Script != "" || s.Dir != "" || len(s.Env) > 0 {
				problems = append(problems, where+
					": run/script/dir/env belong to `kind: host` steps")
			}
		case KindHost:
			// Exactly one of run/script. Accepting both would leave the
			// recipe's own text ambiguous about what it executes.
			switch {
			case len(s.Run) > 0 && s.Script != "":
				problems = append(problems, where+": set run or script, not both")
			case len(s.Run) == 0 && s.Script == "":
				problems = append(problems, where+": a host step needs run or script")
			case len(s.Run) > 0 && strings.TrimSpace(s.Run[0]) == "":
				problems = append(problems, where+": run[0] (the program) is empty")
			}
			if strings.Contains(s.Script, "{{") {
				problems = append(problems, where+
					": script is not substituted, and {{ }} in it would run as literal text. "+
					"Pass values through env: (which is substituted) and read them as shell "+
					`variables: env: {NODE_URL: "{{ params.node_url }}"} then "$NODE_URL"`)
			}
			if s.Command != "" {
				problems = append(problems, where+
					": command belongs to `kind: command` steps; a host step uses run or script")
			}
		default:
			problems = append(problems, fmt.Sprintf(
				"%s: kind %q is not one of command, host", where, s.Kind))
		}
		if !validOnFailure[s.OnFailure] {
			problems = append(problems, fmt.Sprintf(
				"%s: on_failure %q is not one of abort, continue, rollback", where, s.OnFailure))
		}
	}
	return problems
}
