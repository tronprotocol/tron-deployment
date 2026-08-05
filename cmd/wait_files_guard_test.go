package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/output"
)

// `trond wait --exec` and `trond files put` were the two capabilities that
// survived gating `trond exec`: --exec runs `sh -c <caller string>` on the
// node (cmd/wait.go probeExec) and files put writes caller bytes to a
// caller path on it. Both reached a mainnet node with --require-private
// set, and neither left an audit entry.
//
// The asymmetry each pair pins is the point: the mutating half refuses,
// the reading half must keep working. A gate that also blocks reads would
// be turned off, and then it protects nothing.

// newWaitCmd is newCmd plus the flags runWait reads directly off the
// command (json-gt, whose Changed bit disambiguates "0" from "unset").
func newWaitCmd() *cobra.Command {
	c := newCmd()
	c.Flags().Float64("json-gt", 0, "")
	return c
}

// resetWaitFlags clears the package-level probe selectors between cases —
// runWait requires exactly one to be set.
func resetWaitFlags(t *testing.T) {
	t.Helper()
	oPort, oHTTP, oExec := waitPort, waitHTTP, waitExec
	oTimeout, oInterval := waitTimeout, waitInterval
	waitPort, waitHTTP, waitExec = 0, "", ""
	waitTimeout, waitInterval = 300*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() {
		waitPort, waitHTTP, waitExec = oPort, oHTTP, oExec
		waitTimeout, waitInterval = oTimeout, oInterval
	})
}

func TestWaitExec_RefusesNonPrivateNodeUnderGate(t *testing.T) {
	resetWaitFlags(t)
	seedMainnetNode(t, "n0")
	waitExec = "echo owned"

	// Pre-fix this returned "ready", exit 0, having run the command.
	wantPrivateRequired(t, runWait(newWaitCmd(), []string{"n0"}))
}

// TestWaitReadProbes_StillAllowedUnderGate is the half that must NOT
// regress: waiting for a mainnet node to answer is a read, and the gate
// is documented to permit non-mutating calls (the `auto-heal --dry-run`
// precedent). These probes fail here — nothing is listening — but they
// must fail as probes, not as PRIVATE_NETWORK_REQUIRED.
func TestWaitReadProbes_StillAllowedUnderGate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func()
	}{
		{"port", func() { waitPort = 1 }},
		{"http", func() { waitHTTP = "http://127.0.0.1:1/health" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetWaitFlags(t)
			seedMainnetNode(t, "n0")
			tc.apply()

			err := runWait(newWaitCmd(), []string{"n0"})
			if err == nil {
				return // nothing is listening, but a pass is not a gate failure
			}
			var se *output.StructuredError
			if errors.As(err, &se) && se.Code == "PRIVATE_NETWORK_REQUIRED" {
				t.Fatalf("read probe --%s refused by the private gate; the gate is for "+
					"mutation, and blocking reads makes it something people switch off", tc.name)
			}
		})
	}
}

func TestFilesPut_RefusesNonPrivateNodeUnderGate(t *testing.T) {
	seedMainnetNode(t, "n0")
	src := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "written")

	// Pre-fix this wrote the file and reported success.
	wantPrivateRequired(t, runFilesPut(newCmd(), []string{"n0", src, dst}))

	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("destination exists after a refused put — the guard ran too late")
	}
}

// TestFilesGet_AllowedUnderGate pins the read counterpart. get pulls a
// file off the node and changes nothing on it, so the gate must not
// refuse it.
func TestFilesGet_AllowedUnderGate(t *testing.T) {
	seedLocalJarNode(t, "mainnet")
	old := guard.FlagValue
	guard.FlagValue = true
	t.Cleanup(func() { guard.FlagValue = old })

	src := filepath.Join(t.TempDir(), "on-node")
	if err := os.WriteFile(src, []byte("readable"), 0o600); err != nil {
		t.Fatalf("seed remote file: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "pulled")

	if err := runFilesGet(newCmd(), []string{"n0", src, dst}); err != nil {
		t.Fatalf("files get refused under the gate: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "readable" {
		t.Errorf("get did not deliver the file: %q, %v", got, err)
	}
}

func TestFilesPut_WritesAuditEntry(t *testing.T) {
	seedLocalJarNode(t, "private")
	src := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "written")

	if err := runFilesPut(newCmd(), []string{"n0", src, dst}); err != nil {
		t.Fatalf("runFilesPut: %v", err)
	}
	got := auditCommands(t)
	if len(got) != 1 || got[0] != "files put" {
		t.Fatalf("audit commands = %v, want exactly [\"files put\"] — writing bytes to a "+
			"node is a change to it and has to be recoverable from the log", got)
	}
}

// TestWaitExec_GateOffStillRuns is the no-regression case for wait.
func TestWaitExec_GateOffStillRuns(t *testing.T) {
	resetWaitFlags(t)
	seedLocalJarNode(t, "mainnet")
	marker := filepath.Join(t.TempDir(), "ran")
	waitExec = "touch " + marker

	if err := runWait(newWaitCmd(), []string{"n0"}); err != nil {
		t.Fatalf("wait --exec without the gate must still work: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("wait --exec did not run the command: %v", err)
	}
}
