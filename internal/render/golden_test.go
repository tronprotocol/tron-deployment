package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

// TestRenderHOCON_Golden compares the HOCON + compose output for
// each example intent against a checked-in "golden" copy under
// testdata/golden/. A drift here means the template or render path
// changed in a way that affects output bytes — not necessarily a
// bug, but always a deliberate review checkpoint.
//
// The same pattern as the schema-baseline + ajv tests: the golden
// files are content-addressed, drift is a signal, and the developer
// regenerates them in lockstep with intentional changes:
//
//	make update-render-golden       # regenerate all golden files
//	go test ./internal/render/...   # confirm green
//
// Catches: template typos that change rendered output (a HOCON
// curly brace moves, a default value flips, JVM args reorder) —
// things the round-trip regex test misses because it only verifies
// presence, not exact placement.
//
// Skipped for environment-sensitive intents (any that interpolate
// $HOSTNAME, time.Now, or random ports) — none of the examples
// currently do.
func TestRenderHOCON_Golden(t *testing.T) {
	cases := []struct {
		intent string
		// golden file basename. We use a stable short name rather
		// than the intent filename so renames don't leave orphan
		// goldens behind.
		golden string
	}{
		{"nile-fullnode.yaml", "nile-fullnode"},
		{"mainnet-fullnode.yaml", "mainnet-fullnode"},
		{"mainnet-witness.yaml", "mainnet-witness"},
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}

	for _, tc := range cases {
		t.Run(tc.intent, func(t *testing.T) {
			intentPath := filepath.Join(repoRoot, "examples", tc.intent)
			parsed, err := intent.Load(intentPath)
			if err != nil {
				t.Fatalf("intent.Load: %v", err)
			}
			node := &parsed.Nodes[0]

			gotHOCON, err := RenderHOCON("", parsed, node)
			if err != nil {
				t.Fatalf("RenderHOCON: %v", err)
			}
			memGB, err := ParseMemoryGB(node.Resources.Memory)
			if err != nil {
				t.Fatalf("ParseMemoryGB: %v", err)
			}
			jvmArgs := JVMArgsString(memGB, 17, node.JVM)
			gotCompose := RenderCompose(parsed.Name, parsed, node, "", jvmArgs, "")

			compareGolden(t, tc.golden+".conf", gotHOCON)
			compareGolden(t, tc.golden+".compose.yaml", gotCompose)
		})
	}
}

// compareGolden compares the actual rendered text against the golden
// file at testdata/golden/<name>. When TROND_UPDATE_GOLDEN=1 the
// golden is rewritten with the actual content (or created on first
// run); otherwise a mismatch fails the test with the goldenName +
// instructions.
func compareGolden(t *testing.T, name, got string) {
	t.Helper()
	dir := filepath.Join("testdata", "golden")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir testdata/golden: %v", err)
	}
	path := filepath.Join(dir, name)

	if updateGolden() {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}
		t.Logf("updated %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v\nrun `make update-render-golden` to create it",
			path, err)
	}
	if string(want) != got {
		t.Errorf(
			"golden mismatch for %s.\n"+
				"If the change is intentional, regenerate via:\n"+
				"  make update-render-golden\n"+
				"Otherwise something in the render path drifted.\n\n"+
				"--- want (first 400 chars) ---\n%s\n\n"+
				"--- got (first 400 chars) ---\n%s",
			name, snippetForGolden(string(want), 400), snippetForGolden(got, 400))
	}
}

func updateGolden() bool {
	for _, a := range os.Args {
		if a == "-update" {
			return true
		}
	}
	return strings.EqualFold(os.Getenv("TROND_UPDATE_GOLDEN"), "1")
}

func snippetForGolden(s string, n int) string {
	if len(s) > n {
		return s[:n] + "...(truncated)"
	}
	return s
}
