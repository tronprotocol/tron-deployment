package cmd

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// Shared helpers for package cmd tests. Both the hermetic tests
// (default suite) and the container tests (//go:build e2e) use these.
// Kept here (not in cmd/) so a `go build` of trond doesn't pull them in.
//
// Two design choices worth flagging:
//
//  1. We build the trond binary once per `make e2e` run, not once per
//     test. `go build` is fast but not free, and the lifecycle test
//     plus the recipe test plus the network test would otherwise pay
//     the cost three times. sync.Once makes it a single ~1s up-front
//     hit shared by every test in the package.
//
//  2. Every test gets its own TROND_STATE_DIR via t.TempDir(), so the
//     suite never touches the developer's real ~/.trond — and tests
//     can be parallelised in the future without state collisions.

var (
	binaryOnce sync.Once
	binaryPath string
	binaryErr  error
)

// e2eBinary builds the trond binary the first time it's called and
// returns the path on every subsequent call. The binary is dropped in
// a t.TempDir owned by the first caller, which Go cleans up at the
// end of the test run.
func e2eBinary(t *testing.T) string {
	t.Helper()
	binaryOnce.Do(func() {
		// Note: not t.TempDir(). t.TempDir is scoped to the test that
		// first calls it, and would be deleted at that test's end —
		// breaking the second test in a `make e2e` run. The OS reaps
		// /tmp eventually; the binary is small (a few MB) and the
		// leak is per-suite, not per-test.
		dir, err := os.MkdirTemp("", "trond-e2e-bin-*")
		if err != nil {
			binaryErr = err
			return
		}
		binaryPath = filepath.Join(dir, "trond")
		repoRoot, err := filepath.Abs("..")
		if err != nil {
			binaryErr = err
			return
		}
		cmd := exec.Command("go", "build", "-o", binaryPath, ".")
		cmd.Dir = repoRoot
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			binaryErr = err
			return
		}
	})
	if binaryErr != nil {
		t.Fatalf("build trond: %v", binaryErr)
	}
	return binaryPath
}

// e2eEnv returns a per-test trond config: a fresh state dir under
// t.TempDir() and a copy of the host environment with TROND_STATE_DIR
// pointing at it. Use this for any subprocess invocation that
// reads/writes state (apply, status, network, recipe, ...).
//
//nolint:unparam // stateDir is consumed by Docker-tagged tests.
func e2eEnv(t *testing.T) (stateDir string, env []string) {
	t.Helper()
	stateDir = t.TempDir()
	env = append(os.Environ(), "TROND_STATE_DIR="+stateDir)
	return stateDir, env
}

// runTrondCtx executes the prebuilt binary with the given args and
// env. Returns combined stdout+stderr; fails the test if the process
// exits non-zero. Use runTrondAllowFail for tests that want to assert
// on a non-zero exit code.
func runTrondCtx(ctx context.Context, t *testing.T, env []string, args ...string) []byte {
	t.Helper()
	out, err := runTrondAllowFail(ctx, t, env, args...)
	if err != nil {
		t.Fatalf("trond %v failed: %v\noutput: %s", args, err, out)
	}
	return out
}

func runTrondAllowFail(ctx context.Context, t *testing.T, env []string, args ...string) ([]byte, error) {
	t.Helper()
	if env == nil {
		// Fail loud instead of silently inheriting os.Environ() — the
		// inherited env would leak the developer's TROND_STATE_DIR
		// (or fall back to ~/.trond) and pollute their state.
		t.Fatal("e2e: env must be non-nil; pass e2eEnv(t)")
	}
	bin := e2eBinary(t)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return out, err
}

// mustUnmarshalJSON is the single-purpose JSON unmarshal helper used
// across e2e tests. It t.Fatal's with the raw output included so a
// regression in any subcommand's --output json shape is easy to read
// in CI logs.
func mustUnmarshalJSON(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal failed: %v\ndata: %s", err, data)
	}
}

// absExample returns the absolute path of an examples/ or recipes/ file
// relative to the repo root, regardless of the test's working dir.
func absExample(t *testing.T, rel string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", rel))
	if err != nil {
		t.Fatalf("abs %s: %v", rel, err)
	}
	return abs
}
