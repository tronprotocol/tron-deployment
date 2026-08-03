package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F3 regression test for mcpLineDiff. Its output is returned as an MCP
// tool result, i.e. straight into an LLM transcript held by a
// third-party model provider — the worst-case disclosure path for an
// SR signing key. Both sides carry the real key (the live conf is read
// off the deployed host, the desired side is the deployable render),
// and the differ is positional and LCS-free, so any line-count change
// above the assignment misaligns the tail and emits it.

const (
	mcpKeyA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mcpKeyB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func assertNoMCPKeys(t *testing.T, diffs []string) {
	t.Helper()
	for _, l := range diffs {
		for _, k := range []string{mcpKeyA, mcpKeyB} {
			if strings.Contains(l, k) {
				t.Errorf("mcpLineDiff leaked a witness private key over MCP: %q", l)
			}
		}
	}
}

func TestMCPLineDiff_RedactsWitnessKey(t *testing.T) {
	live := "a = 1\n" + `localwitness = ["` + mcpKeyA + `"]` + "\nz = 9\n"

	t.Run("key-rotation", func(t *testing.T) {
		desired := "a = 1\n" + `localwitness = ["` + mcpKeyB + `"]` + "\nz = 9\n"
		diffs := mcpLineDiff(live, desired, 0)
		assertNoMCPKeys(t, diffs)
		if len(diffs) != 2 {
			t.Errorf("a rotated witness key must still be reported as drift: %v", diffs)
		}
		for _, d := range diffs {
			if !strings.Contains(d, `localwitness = ["<REDACTED>"]`) {
				t.Errorf("expected a redacted marker, got %q", d)
			}
		}
	})

	t.Run("line-shift", func(t *testing.T) {
		desired := "a = 1\nseed.node.ip.list = []\n" + `localwitness = ["` + mcpKeyA + `"]` + "\nz = 9\n"
		diffs := mcpLineDiff(live, desired, 0)
		assertNoMCPKeys(t, diffs)
		if len(diffs) == 0 {
			t.Error("a line shift must still be reported as drift")
		}
	})

	t.Run("context-lines", func(t *testing.T) {
		desired := "a = 1\n" + `localwitness = ["` + mcpKeyA + `"]` + "\nz = CHANGED\n"
		diffs := mcpLineDiff(live, desired, 2)
		assertNoMCPKeys(t, diffs)
		if !strings.Contains(strings.Join(diffs, "\n"), `  localwitness = ["<REDACTED>"]`) {
			t.Errorf("matching --context lines must be redacted too, got:\n%s", strings.Join(diffs, "\n"))
		}
	})

	t.Run("identical-is-in-sync", func(t *testing.T) {
		if diffs := mcpLineDiff(live, live, 0); len(diffs) != 0 {
			t.Errorf("identical configs must stay in_sync, got %v", diffs)
		}
	})
}

// TestRenderTool_RedactsWitnessKey covers the other MCP surface named
// by F3: config_render returns the whole rendered HOCON inline, so an
// un-redacted witness key would be handed straight to the model
// provider. The result must also flag itself as a preview.
func TestRenderTool_RedactsWitnessKey(t *testing.T) {
	t.Setenv("PROBE_SR_KEY", mcpKeyA)

	dir := t.TempDir()
	path := filepath.Join(dir, "witness.yaml")
	body := `name: probe-witness
target:
  type: local
  runtime: docker
network: mainnet
nodes:
  - type: witness
    version: latest
    witness_key:
      private_key_env: PROBE_SR_KEY
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write intent: %v", err)
	}

	res, _, err := renderTool(context.Background(), nil, renderArg{Path: path})
	if err != nil {
		t.Fatalf("renderTool: %v", err)
	}
	// The wire body is what actually reaches the model provider.
	if strings.Contains(extractText(t, res), mcpKeyA) {
		t.Fatal("config_render returned the raw witness private key over MCP")
	}

	var payload struct {
		Redacted bool `json:"redacted"`
		Nodes    []struct {
			HOCON    string `json:"hocon"`
			Redacted bool   `json:"redacted"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(extractText(t, res)), &payload); err != nil {
		t.Fatalf("tool body is not JSON: %v", err)
	}
	if len(payload.Nodes) != 1 {
		t.Fatalf("expected 1 rendered node, got %d", len(payload.Nodes))
	}
	if strings.Contains(payload.Nodes[0].HOCON, mcpKeyA) {
		t.Fatal("rendered hocon carries the raw witness private key")
	}
	if !strings.Contains(payload.Nodes[0].HOCON, `localwitness = ["<REDACTED:PROBE_SR_KEY>"]`) {
		t.Error("rendered hocon is missing the redaction placeholder")
	}
	if !payload.Redacted || !payload.Nodes[0].Redacted {
		t.Errorf("result must flag itself as a redacted preview (envelope=%t node=%t)",
			payload.Redacted, payload.Nodes[0].Redacted)
	}
}
