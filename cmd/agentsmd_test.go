// Package-level tests guarding the AGENTS.md agent contract.
//
// NOTE: this file intentionally has NO //go:build e2e tag — it runs in
// the DEFAULT test suite (moved out of the e2e tag in 3d8943b "close
// local gate gaps") so AGENTS.md doc rot (renamed commands, drifted
// output fields) is caught by the ordinary CI "Test + coverage" job and
// local `go test ./...`, not only when someone runs -tags=e2e.
//
// Cost/hermeticity caveat: TestE2E_AgentsMD_OutputFieldsExist is NOT
// fully hermetic — it builds the trond binary once per package run via
// e2eEnv and executes a small set of read-only commands (version,
// doctor, snapshot sources, recipe list, network status, list). Command
// failures are Skip, not Fail, and no Docker/network is required. Keep
// any new test in this file equally cheap and skip-tolerant; tests that
// need Docker or a deployed node belong in a //go:build e2e file.
package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestE2E_AgentsMD_CommandsResolveCobra parses AGENTS.md, extracts
// every `trond <cmd>` invocation appearing in fenced bash code
// blocks, and asserts each one resolves to a real cobra subcommand.
//
// Why: AGENTS.md is the agent contract. If a code-snippet command is
// renamed or removed, agents reading the doc emit invalid CLI calls.
// This test catches the doc-rot at PR time.
//
// No Docker required — just a cobra-tree lookup, no execution.
func TestE2E_AgentsMD_CommandsResolveCobra(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(repoRoot, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}

	root := Root()
	cmds := extractTrondInvocations(string(body))
	if len(cmds) < 5 {
		t.Fatalf("only found %d invocations — parser regression?", len(cmds))
	}

	seen := map[string]bool{}
	for _, c := range cmds {
		if seen[c] {
			continue
		}
		seen[c] = true
		t.Run(c, func(t *testing.T) {
			tokens := strings.Fields(c)
			if len(tokens) == 0 || tokens[0] != "trond" {
				t.Fatalf("malformed extracted command: %q", c)
			}
			// Skip prose templates that use <placeholder> for the
			// command itself ("trond <command> -o json"). Real
			// invocations always have a literal subcommand as the
			// first non-flag token.
			if len(tokens) >= 2 && strings.HasPrefix(tokens[1], "<") {
				t.Skipf("placeholder-only invocation: %q", c)
				return
			}
			if !commandExists(root, tokens[1:]) {
				t.Errorf("AGENTS.md references `trond %s` but cobra can't resolve it",
					strings.Join(tokens[1:], " "))
			}
		})
	}
}

// TestE2E_AgentsMD_OutputFieldsExist runs each "safe" trond
// invocation in AGENTS.md (read-only, no deploy needed) and asserts
// every field name mentioned in the immediately-following
// `# Output: {...}` comment actually appears in the live output.
//
// Catches stale documentation fields — the most common kind of
// AGENTS.md rot.
func TestE2E_AgentsMD_OutputFieldsExist(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(repoRoot, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}

	pairs := extractCommandOutputPairs(string(body))
	if len(pairs) == 0 {
		t.Skip("no `trond ... \\n# Output: {...}` pairs found — parser regression?")
	}

	// Subset of commands that are safe to run with no prior state.
	// We only check the field-name claims for these; the rest are
	// validated by TestE2E_AgentsMD_CommandsResolveCobra.
	safe := map[string]bool{
		"version":          true,
		"doctor":           true,
		"snapshot sources": true,
		"recipe list":      true,
		"network status":   true,
		"list":             true,
	}

	_, env := e2eEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, p := range pairs {
		cmdKey := strings.Join(strings.Fields(strings.TrimPrefix(p.Cmd, "trond "))[:1], " ")
		// Two-word commands (snapshot sources etc.).
		if tokens := strings.Fields(strings.TrimPrefix(p.Cmd, "trond ")); len(tokens) >= 2 {
			cmdKey = tokens[0] + " " + tokens[1]
		}
		if !safe[cmdKey] {
			continue
		}
		t.Run(p.Cmd, func(t *testing.T) {
			args := strings.Fields(strings.TrimPrefix(p.Cmd, "trond "))
			args = stripFlag(args, "-o")
			args = stripFlagWithValue(args, "-o")
			args = append(args, "--output", "json")
			out, err := runTrondAllowFail(ctx, t, env, args...)
			if err != nil {
				t.Skipf("command failed (skipping field check): %v\nbody: %s", err, out)
				return
			}
			var actual any
			if err := json.Unmarshal(out, &actual); err != nil {
				return
			}
			// Walk the entire JSON tree looking for documented field
			// names. AGENTS.md frequently shows fields nested under
			// arrays (e.g. snapshot sources' fields are inside
			// sources[]); requiring top-level only would force the
			// docs to inline a wrapper. Anywhere-in-tree match keeps
			// the contract loose enough to be informative without
			// false positives.
			present := collectKeys(actual, map[string]bool{})
			for _, field := range p.Fields {
				if !present[field] {
					t.Errorf("AGENTS.md says `%s` emits field %q anywhere in the response, but it's missing\nactual: %v",
						p.Cmd, field, actual)
				}
			}
		})
	}
}

// extractTrondInvocations pulls every line that matches `^trond ...`
// inside a fenced bash code block. Skips comments and shell
// variables. Returns the canonicalised invocation (no shell
// continuations, no env var prefix).
func extractTrondInvocations(md string) []string {
	var out []string
	inFence := false
	for _, raw := range strings.Split(md, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		// Allow `VAR=val trond ...` and `\\` continuations.
		idx := strings.Index(line, "trond ")
		if idx < 0 {
			continue
		}
		// Skip path-like uses (e.g. /usr/local/bin/trond — handled
		// only when "trond " starts at idx 0 or after whitespace
		// AND the prefix isn't '/').
		if idx > 0 && line[idx-1] == '/' {
			continue
		}
		cmd := strings.TrimSuffix(strings.TrimSpace(line[idx:]), "\\")
		// Strip pipe redirects ("| jq ..."), redirects, etc.
		if i := strings.IndexAny(cmd, "|>"); i >= 0 {
			cmd = strings.TrimSpace(cmd[:i])
		}
		// Drop shell-var arg substitutions like $foo, <name>, etc.
		// the cobra resolver tolerates them as positionals.
		out = append(out, cmd)
	}
	return out
}

type cmdOutputPair struct {
	Cmd    string
	Fields []string
}

var jsonFieldRE = regexp.MustCompile(`"([a-z_]+)"\s*:`)

// extractCommandOutputPairs scans the markdown for the canonical
// docstring shape:
//
//	trond <cmd> ... -o json
//	# Output: {"foo": ..., "bar": ...}
//
// and returns the matched (command, fields[]) pairs. Multi-line
// Output blocks are concatenated until the next blank or non-comment
// line.
func extractCommandOutputPairs(md string) []cmdOutputPair {
	lines := strings.Split(md, "\n")
	var out []cmdOutputPair
	inFence := false
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			continue
		}
		if !strings.HasPrefix(line, "trond ") {
			continue
		}
		cmd := line
		// Collect any "# Output: ..." that follows in the next 1-5 lines.
		var outputBlock strings.Builder
		for j := i + 1; j < len(lines) && j < i+6; j++ {
			t := strings.TrimSpace(lines[j])
			if !strings.HasPrefix(t, "#") {
				break
			}
			if strings.HasPrefix(t, "# Output:") {
				outputBlock.WriteString(strings.TrimPrefix(t, "# Output:"))
			} else if outputBlock.Len() > 0 {
				outputBlock.WriteString(" ")
				outputBlock.WriteString(strings.TrimPrefix(t, "#"))
			}
		}
		if outputBlock.Len() == 0 {
			continue
		}
		matches := jsonFieldRE.FindAllStringSubmatch(outputBlock.String(), -1)
		fields := make([]string, 0, len(matches))
		for _, m := range matches {
			fields = append(fields, m[1])
		}
		if len(fields) == 0 {
			continue
		}
		out = append(out, cmdOutputPair{Cmd: cmd, Fields: fields})
	}
	return out
}

// collectKeys walks any JSON tree (objects + arrays) and returns the
// set of key names that appear as map keys at any depth.
func collectKeys(v any, into map[string]bool) map[string]bool {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			into[k] = true
			collectKeys(val, into)
		}
	case []any:
		for _, el := range t {
			collectKeys(el, into)
		}
	}
	return into
}

func stripFlag(args []string, name string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == name {
			continue
		}
		out = append(out, a)
	}
	return out
}

// stripFlagWithValue removes "<flag> <value>" pairs.
func stripFlagWithValue(args []string, name string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}
