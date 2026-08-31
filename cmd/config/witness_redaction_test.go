package config

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/render"
)

const (
	cfgKeyA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cfgKeyB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// TestSimpleDiff_RedactsWitnessKey is the F3 regression for `trond
// config diff`. It compares the deployed .conf (which carries the real
// SR key) against a fresh render (which also carries it, so that
// genuine drift is still detected) with a positional, LCS-free walk —
// so any line-count change above the assignment misaligns the tail and
// would otherwise print the key to stdout and into diffs[].
func TestSimpleDiff_RedactsWitnessKey(t *testing.T) {
	cases := []struct {
		name         string
		old, new     []string
		wantDiffs    int
		wantRedacted int
	}{
		{
			name:         "key-rotation",
			old:          []string{"a = 1", `localwitness = ["` + cfgKeyA + `"]`},
			new:          []string{"a = 1", `localwitness = ["` + cfgKeyB + `"]`},
			wantDiffs:    2,
			wantRedacted: 2,
		},
		{
			name:         "line-shift-same-key",
			old:          []string{"a = 1", `localwitness = ["` + cfgKeyA + `"]`},
			new:          []string{"a = 1", "seed.node.ip.list = []", `localwitness = ["` + cfgKeyA + `"]`},
			wantDiffs:    3,
			wantRedacted: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diffs := render.DiffLines(tc.old, tc.new, 0)
			for _, d := range diffs {
				if strings.Contains(d, cfgKeyA) || strings.Contains(d, cfgKeyB) {
					t.Errorf("simpleDiff leaked a witness private key: %q", d)
				}
			}
			if len(diffs) != tc.wantDiffs {
				t.Errorf("drift reporting changed: got %d lines, want %d: %v",
					len(diffs), tc.wantDiffs, diffs)
			}
			n := 0
			for _, d := range diffs {
				if strings.Contains(d, `localwitness = ["<REDACTED>"]`) {
					n++
				}
			}
			if n != tc.wantRedacted {
				t.Errorf("expected %d redacted markers, got %d: %v", tc.wantRedacted, n, diffs)
			}
		})
	}

	// Redacting before comparing would swallow real drift; redacting
	// after must not.
	same := []string{`localwitness = ["` + cfgKeyA + `"]`}
	if diffs := render.DiffLines(same, same, 0); len(diffs) != 0 {
		t.Errorf("unchanged key must not be reported as drift: %v", diffs)
	}
}

// TestRunRender_WitnessRedactionIsSignalled covers objection 2: the
// artifact `--output-dir` writes is a preview java-tron rejects, so the
// caller has to be told — on stderr and in the JSON envelope.
func TestRunRender_WitnessRedactionIsSignalled(t *testing.T) {
	t.Setenv("PROBE_SR_KEY", cfgKeyA)

	dir := t.TempDir()
	intentPath := filepath.Join(dir, "witness.yaml")
	if err := os.WriteFile(intentPath, []byte(`name: probe-witness
target:
  type: local
  runtime: docker
network: mainnet
nodes:
  - type: witness
    version: latest
    witness_key:
      private_key_env: PROBE_SR_KEY
`), 0o600); err != nil {
		t.Fatalf("write intent: %v", err)
	}

	outDir := filepath.Join(dir, "out")
	stdout, stderr := runRenderCapture(t, intentPath, outDir, "json")

	if strings.Contains(stdout, cfgKeyA) {
		t.Fatal("config render -o json emitted the raw witness private key on stdout")
	}
	if !strings.Contains(stderr, "PREVIEW") {
		t.Errorf("expected a preview warning on stderr, got %q", stderr)
	}

	var payload struct {
		Redacted bool `json:"redacted"`
		Nodes    []struct {
			Redacted bool `json:"redacted"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	if !payload.Redacted {
		t.Error("JSON envelope must carry redacted: true")
	}
	if len(payload.Nodes) != 1 || !payload.Nodes[0].Redacted {
		t.Error("the rendered node must carry redacted: true")
	}

	// The file on disk fails closed: placeholder, never the key.
	written, err := os.ReadFile(filepath.Join(outDir, "probe-witness.conf"))
	if err != nil {
		t.Fatalf("read written conf: %v", err)
	}
	if strings.Contains(string(written), cfgKeyA) {
		t.Fatal("--output-dir wrote the raw witness private key to disk")
	}
	if !strings.Contains(string(written), `localwitness = ["<REDACTED:PROBE_SR_KEY>"]`) {
		t.Error("written conf is missing the redaction placeholder")
	}
}

// TestRunRender_NoWitnessNoSignal keeps the flag and the warning off
// the overwhelmingly common non-witness path.
func TestRunRender_NoWitnessNoSignal(t *testing.T) {
	dir := t.TempDir()
	intentPath := filepath.Join(dir, "fullnode.yaml")
	if err := os.WriteFile(intentPath, []byte(`name: probe-fullnode
target:
  type: local
  runtime: docker
network: nile
nodes:
  - type: fullnode
    version: latest
`), 0o600); err != nil {
		t.Fatalf("write intent: %v", err)
	}

	stdout, stderr := runRenderCapture(t, intentPath, "", "json")
	if stderr != "" {
		t.Errorf("no redaction happened, so stderr must stay quiet, got %q", stderr)
	}
	if strings.Contains(stdout, `"redacted"`) {
		t.Error("redacted must be omitted entirely when nothing was redacted")
	}
}

// runRenderCapture invokes runRender with os.Stdout / os.Stderr piped,
// returning what each stream received.
func runRenderCapture(t *testing.T, intentPath, outputDir, format string) (string, string) {
	t.Helper()

	prevOutDir, prevOverlay, prevFilter := renderOutputDir, renderOverlay, renderNodeFilter
	renderOutputDir, renderOverlay, renderNodeFilter = outputDir, "", -1
	t.Cleanup(func() {
		renderOutputDir, renderOverlay, renderNodeFilter = prevOutDir, prevOverlay, prevFilter
	})

	cmd := &cobra.Command{}
	cmd.Flags().String("output", format, "")

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW

	runErr := runRender(cmd, []string{intentPath})

	os.Stdout, os.Stderr = origOut, origErr
	outW.Close()
	errW.Close()
	outBytes, _ := io.ReadAll(outR)
	errBytes, _ := io.ReadAll(errR)

	if runErr != nil {
		t.Fatalf("runRender: %v", runErr)
	}
	return string(outBytes), string(errBytes)
}
