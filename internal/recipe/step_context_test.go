package recipe

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A recipe step is a re-exec of the trond binary, and it inherits none of
// the parent's flags. That made a step act in a different place, and under
// a different safety policy, than the run that launched it — with nothing
// in the output to say so.
//
// These tests assert on the argv the runner actually builds, because argv
// is the only thing that decides what the child does.

// argvRecorder returns a fake "trond" that appends its own argv, one JSON
// array per invocation, to a file — plus the path of that file.
func argvRecorder(t *testing.T) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "argv.jsonl")
	bin = filepath.Join(dir, "fake-trond")
	script := "#!/bin/sh\n" +
		"printf '[' >> " + log + "\n" +
		"sep=''\n" +
		"for a in \"$@\"; do printf '%s\"%s\"' \"$sep\" \"$a\" >> " + log + "; sep=','; done\n" +
		"printf ']\\n' >> " + log + "\n" +
		"echo '{}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return bin, log
}

func readArgv(t *testing.T, log string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var argv []string
		if err := json.Unmarshal([]byte(line), &argv); err != nil {
			t.Fatalf("argv line %q: %v", line, err)
		}
		out = append(out, argv)
	}
	return out
}

func TestRun_ForwardsStateDirAndGateToSteps(t *testing.T) {
	bin, log := argvRecorder(t)
	r := Recipe{Name: "t", Steps: []Step{{ID: "s1", Command: "status", Args: []string{"n0"}}}}

	if _, err := Run(context.Background(), r, RunOptions{
		Binary: bin, Out: io.Discard, Err: io.Discard,
		StateDir: "/tmp/isolated-state", RequirePrivate: true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	argv := readArgv(t, log)
	if len(argv) != 1 {
		t.Fatalf("want 1 invocation, got %d: %v", len(argv), argv)
	}
	joined := strings.Join(argv[0], " ")
	for _, want := range []string{"--state-dir /tmp/isolated-state", "--require-private"} {
		if !strings.Contains(joined, want) {
			t.Errorf("child argv missing %q — the step would act outside the parent's "+
				"state/safety context.\ngot: %v", want, argv[0])
		}
	}
}

// TestRun_OmitsUnsetForwarding keeps the forwarding honest: a run with no
// state dir and no gate must not invent either.
func TestRun_OmitsUnsetForwarding(t *testing.T) {
	bin, log := argvRecorder(t)
	r := Recipe{Name: "t", Steps: []Step{{ID: "s1", Command: "status", Args: []string{"n0"}}}}

	if _, err := Run(context.Background(), r, RunOptions{
		Binary: bin, Out: io.Discard, Err: io.Discard,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	joined := strings.Join(readArgv(t, log)[0], " ")
	if strings.Contains(joined, "--state-dir") || strings.Contains(joined, "--require-private") {
		t.Errorf("forwarded a flag that was never set: %s", joined)
	}
}

// TestRun_GlobalFlagsPrecedeDoubleDash is the argv-position property.
// Every `exec` step carries a "--", and everything after it belongs to the
// inner command: trond's own flags appended at the end were handed to the
// program being exec'd instead, while trond stayed in text mode.
func TestRun_GlobalFlagsPrecedeDoubleDash(t *testing.T) {
	bin, log := argvRecorder(t)
	r := Recipe{Name: "t", Steps: []Step{{
		ID: "s1", Command: "exec",
		Args: []string{"n0", "--", "/bin/echo", "hello"},
	}}}

	if _, err := Run(context.Background(), r, RunOptions{
		Binary: bin, Out: io.Discard, Err: io.Discard,
		StateDir: "/tmp/sd", RequirePrivate: true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	argv := readArgv(t, log)[0]
	dash := -1
	for i, a := range argv {
		if a == "--" {
			dash = i
			break
		}
	}
	if dash == -1 {
		t.Fatalf("the step's own -- vanished from argv: %v", argv)
	}
	for i, a := range argv {
		switch a {
		case "--output", "--state-dir", "--require-private":
			if i > dash {
				t.Errorf("%s at index %d is AFTER the -- at %d; it would be passed to the "+
					"inner command instead of to trond.\nargv: %v", a, i, dash, argv)
			}
		}
	}
	// And the inner command must arrive intact.
	if got := strings.Join(argv[dash+1:], " "); got != "/bin/echo hello" {
		t.Errorf("inner command = %q, want %q", got, "/bin/echo hello")
	}
}

// TestRun_ResumeFromUnknownStepFails: a --resume-from that matches nothing
// skipped every step and returned status "success" with no work done.
func TestRun_ResumeFromUnknownStepFails(t *testing.T) {
	bin, _ := argvRecorder(t)
	r := Recipe{Name: "t", Steps: []Step{
		{ID: "first", Command: "status"},
		{ID: "second", Command: "status"},
	}}

	res, err := Run(context.Background(), r, RunOptions{
		Binary: bin, Out: io.Discard, Err: io.Discard,
		ResumeFrom: "secnod", // typo
	})
	if err == nil {
		t.Fatal("want an error for an unmatched --resume-from, got nil (a run that did " +
			"nothing at all reported success)")
	}
	if res.Status != "failed" {
		t.Errorf("status = %q, want failed", res.Status)
	}
	for _, want := range []string{"secnod", "first, second"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %q so the typo is obvious", err, want)
		}
	}
}

// TestRun_RollbackSeesFailedStepOutput: rollback exists to clean up after
// the step that failed, so it needs that step's output. Persisting after
// the failure switch meant the one case the reference was written for was
// the one case where the key was missing.
func TestRun_RollbackSeesFailedStepOutput(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.jsonl")
	bin := filepath.Join(dir, "fake-trond")
	// First call emits a name then fails; later calls (the rollback)
	// record their argv so we can see what was substituted.
	script := "#!/bin/sh\n" +
		"if [ ! -f " + dir + "/ran ]; then touch " + dir + "/ran; echo '{\"name\":\"node-7\"}'; exit 1; fi\n" +
		"printf '[' >> " + log + "; sep=''\n" +
		"for a in \"$@\"; do printf '%s\"%s\"' \"$sep\" \"$a\" >> " + log + "; sep=','; done\n" +
		"printf ']\\n' >> " + log + "\n" +
		"echo '{}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	r := Recipe{
		Name:  "t",
		Steps: []Step{{ID: "apply", Command: "apply", OnFailure: "rollback"}},
		Rollback: []Step{{
			ID: "stop-failed-node", Command: "stop",
			Args: []string{"{{ steps.apply.name }}"},
		}},
	}
	res, err := Run(context.Background(), r, RunOptions{Binary: bin, Out: io.Discard, Err: io.Discard})
	if err == nil {
		t.Fatal("want the apply failure to surface")
	}
	if !res.RollbackRan {
		t.Fatal("rollback did not run")
	}
	if len(res.RollbackSteps) != 1 || res.RollbackSteps[0].Error != "" {
		t.Fatalf("rollback step errored: %+v", res.RollbackSteps)
	}
	argv := readArgv(t, log)
	if len(argv) != 1 {
		t.Fatalf("want 1 rollback invocation, got %v", argv)
	}
	if got := strings.Join(argv[0], " "); !strings.Contains(got, "node-7") {
		t.Errorf("rollback argv = %v; {{ steps.apply.name }} did not resolve to the failed "+
			"step's output", argv[0])
	}
}

// TestRun_ContinueKeepsStepOutput: on_failure: continue jumped the persist
// block too, so a later step referencing the failed one got a missing key.
func TestRun_ContinueKeepsStepOutput(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.jsonl")
	bin := filepath.Join(dir, "fake-trond")
	script := "#!/bin/sh\n" +
		"if [ ! -f " + dir + "/ran ]; then touch " + dir + "/ran; echo '{\"name\":\"node-9\"}'; exit 1; fi\n" +
		"printf '[' >> " + log + "; sep=''\n" +
		"for a in \"$@\"; do printf '%s\"%s\"' \"$sep\" \"$a\" >> " + log + "; sep=','; done\n" +
		"printf ']\\n' >> " + log + "\n" +
		"echo '{}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	r := Recipe{Name: "t", Steps: []Step{
		{ID: "probe", Command: "health", OnFailure: "continue"},
		{ID: "after", Command: "status", Args: []string{"{{ steps.probe.name }}"}},
	}}
	if _, err := Run(context.Background(), r, RunOptions{Binary: bin, Out: io.Discard, Err: io.Discard}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	argv := readArgv(t, log)
	if len(argv) != 1 {
		t.Fatalf("want the second step to have run, got %v", argv)
	}
	if got := strings.Join(argv[0], " "); !strings.Contains(got, "node-9") {
		t.Errorf("argv = %v; a continued step's output was dropped", argv[0])
	}
}
