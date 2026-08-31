package recipe

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// substitute renders a single arg/string against the current scope.
// Scope keys are "params" (map[string]string) and "steps"
// (map[string]map[string]any). Unknown references error explicitly so
// recipe authors get a loud signal instead of empty-string surprises.
//
// Example template forms supported:
//
//	{{ params.node_name }}
//	{{ steps.stage_snapshot.job_id }}
//	{{ params.storage_root }}/output-directory
//
// We deliberately keep this thin — full Sprig-style helpers are
// overkill and recipes should stay declarative. If a step needs
// computation, write a new internal/* helper and wrap it as a step
// kind, don't grow the template language.
func substitute(raw string, params map[string]string, steps map[string]map[string]any) (string, error) {
	if !strings.Contains(raw, "{{") {
		return raw, nil
	}
	// Recipes write {{ params.foo }} / {{ steps.id.field }} for
	// readability — the helm / sprig / mustache convention. Go's
	// stdlib text/template requires a leading dot for field access
	// (otherwise `params` parses as a function call). We rewrite the
	// two top-level scopes here so recipe authors don't have to think
	// about Go-template internals. This is a deliberate one-line
	// translation, not a generic preprocessor.
	// Cover the four shapes Go template parsers accept: with/without
	// inner whitespace, and with the trim modifier (`{{-` / `-}}`).
	// Future recipes that use trim get the same translation as plain
	// `{{ params.X }}` does today.
	rewritten := raw
	for _, scope := range []string{"params", "steps"} {
		rewritten = strings.ReplaceAll(rewritten, "{{ "+scope+".", "{{ ."+scope+".")
		rewritten = strings.ReplaceAll(rewritten, "{{"+scope+".", "{{."+scope+".")
		rewritten = strings.ReplaceAll(rewritten, "{{- "+scope+".", "{{- ."+scope+".")
		rewritten = strings.ReplaceAll(rewritten, "{{-"+scope+".", "{{-."+scope+".")
	}

	t, err := template.New("step").Option("missingkey=error").Parse(rewritten)
	if err != nil {
		return "", fmt.Errorf("template parse %q: %w", raw, err)
	}
	scope := map[string]any{
		"params": params,
		"steps":  steps,
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, scope); err != nil {
		return "", fmt.Errorf("template execute %q: %w", raw, err)
	}
	return buf.String(), nil
}

// substituteAll runs substitute over a slice. Used for step args.
func substituteAll(raws []string, params map[string]string, steps map[string]map[string]any) ([]string, error) {
	out := make([]string, len(raws))
	for i, r := range raws {
		s, err := substitute(r, params, steps)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}
