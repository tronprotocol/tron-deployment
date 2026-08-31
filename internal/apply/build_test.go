package apply

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/build"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// fakeBuilderRunner stands in for the docker-driven build inside
// apply tests. It plants a syntactically-valid FullNode JAR at the
// requested .tmp path so the validator + finalize path runs to
// completion without spinning up docker.
type fakeBuilderRunner struct {
	called bool
}

func (f *fakeBuilderRunner) RunDockerBuild(
	_ context.Context,
	_ /* sourcePath */, _ /* outDir */, outTmp string,
	_ /* gradleTask */ string,
	_ /* gradleArgs */ []string,
	_ /* env */ map[string]string,
) error {
	f.called = true
	return os.WriteFile(outTmp, makeMinimalFullNodeJAR(), 0o600)
}

// makeMinimalFullNodeJAR returns the bytes of a ZIP whose
// META-INF/MANIFEST.MF declares org.tron.program.FullNode as
// Main-Class. Just enough to pass build's FR-011 validator.
func makeMinimalFullNodeJAR() []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("META-INF/MANIFEST.MF")
	_, _ = w.Write([]byte("Manifest-Version: 1.0\nMain-Class: org.tron.program.FullNode\n"))
	_ = zw.Close()
	return buf.Bytes()
}

// initGitRepo creates a one-commit git fixture trond's source.go can
// resolve. Used by every test that drives the full build path.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "trond-test@example.com"},
		{"config", "user.name", "trond test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	for _, args := range [][]string{
		{"add", "."},
		{"commit", "-q", "-m", "initial"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// TestResolveBuildSource_AbsolutePath asserts an absolute build.source
// passes through unchanged regardless of intent path.
func TestResolveBuildSource_AbsolutePath(t *testing.T) {
	got, err := resolveBuildSource("/abs/path/to/source", "/some/intent.yaml")
	if err != nil {
		t.Fatalf("resolveBuildSource: %v", err)
	}
	if got != "/abs/path/to/source" {
		t.Errorf("absolute source mutated: %q", got)
	}
}

// TestResolveBuildSource_RelativeViaIntent is the FR-021 happy path:
// `build.source: ../java-tron` resolves relative to the intent file's
// parent directory.
func TestResolveBuildSource_RelativeViaIntent(t *testing.T) {
	got, err := resolveBuildSource("../java-tron", "/Users/me/projects/trond/examples/dev-local.yaml")
	if err != nil {
		t.Fatalf("resolveBuildSource: %v", err)
	}
	want := "/Users/me/projects/trond/java-tron"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

// TestResolveBuildSource_RelativeNoIntentPath falls back to CWD when
// the caller didn't pass an intent path (e.g. intent supplied via
// stdin).
func TestResolveBuildSource_RelativeNoIntentPath(t *testing.T) {
	got, err := resolveBuildSource("./relative", "")
	if err != nil {
		t.Fatalf("resolveBuildSource: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("expected absolute path; got %q", got)
	}
}

// TestApply_RecordsBuildCacheKeyInState is the end-to-end Phase 2
// regression guard: when intent carries a `build:` block, the
// resolved cache key gets persisted into state.ManagedNode so a
// future `trond build prune` can refuse to delete an in-use artifact.
//
// We can't run the docker runtime here (no daemon), so this test
// stops at the build resolution step and asserts the BuildSummary +
// state plumbing populated correctly. The downstream render+deploy
// path is exercised by cmd/apply_e2e_test.go.
func TestApply_BuildSummaryPopulated(t *testing.T) {
	// Isolate state + cache dirs.
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	src := filepath.Join(dir, "java-tron")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	initGitRepo(t, src)

	stub := &fakeBuilderRunner{}
	restore := build.SetTestRunner(stub)
	defer restore()

	ctx := context.Background()
	in := &intent.Intent{
		Name:    "dev-fullnode",
		Network: "nile",
		Nodes: []intent.NodeSpec{{
			Type: "fullnode",
			Build: &intent.BuildSpec{
				Source:               src,
				Revision:             "HEAD",
				JDK:                  "8",
				Artifact:             "jar",
				BuilderImageOverride: "test-image@sha256:abcdef1234567890",
			},
		}},
	}

	summary, jarPath, imageTag, err := resolveBuild(ctx, Options{Intent: in}, &in.Nodes[0])
	if err != nil {
		t.Fatalf("resolveBuild: %v", err)
	}
	if !stub.called {
		t.Error("builder runner not invoked")
	}
	if summary == nil {
		t.Fatal("BuildSummary nil")
		return // unreachable after Fatal, but satisfies staticcheck SA5011
	}
	if summary.CacheKey == "" {
		t.Error("CacheKey not populated")
	}
	if summary.SourceRevision == "" {
		t.Errorf("SourceRevision should be set; got %q", summary.SourceRevision)
	}
	if jarPath == "" {
		t.Error("builtJarPath should be set when artifact = jar")
	}
	if imageTag != "" {
		t.Errorf("builtImageTag should be empty in Phase 2 jar path; got %q", imageTag)
	}
}

// TestApply_NoBuildBlockLeavesSummaryNil covers the unchanged path:
// intents without a `build:` block produce no BuildSummary and never
// invoke the builder.
func TestApply_NoBuildBlockLeavesSummaryNil(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	stub := &fakeBuilderRunner{}
	restore := build.SetTestRunner(stub)
	defer restore()

	in := &intent.Intent{
		Name:    "no-build",
		Network: "nile",
		Nodes:   []intent.NodeSpec{{Type: "fullnode"}},
	}
	summary, jarPath, _, err := resolveBuild(context.Background(),
		Options{Intent: in}, &in.Nodes[0])
	if err != nil {
		t.Fatalf("resolveBuild: %v", err)
	}
	if summary != nil {
		t.Error("BuildSummary should be nil when no build block present")
	}
	if jarPath != "" {
		t.Error("builtJarPath should be empty when no build block present")
	}
	if stub.called {
		t.Error("builder runner invoked despite no build block")
	}
}

// TestApply_FullFlow_RecordsBuildCacheKey is the end-to-end Phase 2
// happy path: cobra-equivalent Apply() with a `build:` intent →
// build runs → systemd unit renders with built JAR → state.json
// records BuildCacheKey for FR-018 prune protection. Uses fakeTarget
// (Exec no-ops) so the jar runtime's systemctl calls don't fail.
func TestApply_FullFlow_RecordsBuildCacheKey(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	src := filepath.Join(dir, "java-tron")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initGitRepo(t, src)

	restore := build.SetTestRunner(&fakeBuilderRunner{})
	defer restore()

	in := &intent.Intent{
		Name:    "dev-node",
		Network: "nile",
		Target:  intent.Target{Type: "local", Runtime: "jar"},
		Nodes: []intent.NodeSpec{{
			Type:        "fullnode",
			Version:     "4.8.1",
			Resources:   intent.Resources{Memory: "4G"},
			Ports:       intent.PortMapping{HTTP: 8090, GRPC: 50051},
			InstallPath: filepath.Join(dir, "install"),
			Build: &intent.BuildSpec{
				Source:               src,
				Revision:             "HEAD",
				JDK:                  "8",
				Artifact:             "jar",
				BuilderImageOverride: "test-image@sha256:abcdef1234567890",
			},
		}},
	}

	store, st := freshStore(t)
	res, err := Apply(context.Background(), Options{
		Intent:         in,
		Target:         &fakeTarget{},
		Store:          store,
		State:          st,
		IntentHash:     "phase2-test-hash",
		TemplateDir:    "", // use embedded templates
		DeploymentsDir: filepath.Join(dir, "deployments"),
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if res.Build == nil {
		t.Fatal("Result.Build nil — build pipeline did not run")
	}
	if res.Build.CacheKey == "" {
		t.Error("Result.Build.CacheKey empty")
	}
	if res.Outcome != "created" {
		t.Errorf("Outcome = %q; want created", res.Outcome)
	}

	// C1: the result echoes the deployed network + safety fact, and a
	// nile node is NOT private.
	if res.Network != "nile" {
		t.Errorf("Result.Network = %q; want nile", res.Network)
	}
	if res.IsPrivate {
		t.Error("Result.IsPrivate = true for a nile node; want false")
	}

	// State persistence: ManagedNode.BuildCacheKey must equal what
	// Result.Build.CacheKey reports — Phase 5 prune relies on this.
	stored, err := store.Load()
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if len(stored.Nodes) != 1 {
		t.Fatalf("expected 1 stored node; got %d", len(stored.Nodes))
	}
	if stored.Nodes[0].BuildCacheKey != res.Build.CacheKey {
		t.Errorf("state.BuildCacheKey = %q; want %q (matching Result)",
			stored.Nodes[0].BuildCacheKey, res.Build.CacheKey)
	}
}

// TestApply_DirtySourceTriggersRebuild is the FR-002 + Phase 2
// regression guard: the dev-loop bug we fixed. Same intent.yaml,
// modified source tree → trond MUST NOT no-op as it did pre-fix.
func TestApply_DirtySourceTriggersRebuild(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	src := filepath.Join(dir, "java-tron")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initGitRepo(t, src)

	stub := &fakeBuilderRunner{}
	restore := build.SetTestRunner(stub)
	defer restore()

	in := &intent.Intent{
		Name:    "dev-node",
		Network: "nile",
		Target:  intent.Target{Type: "local", Runtime: "jar"},
		Nodes: []intent.NodeSpec{{
			Type:        "fullnode",
			Version:     "4.8.1",
			Resources:   intent.Resources{Memory: "4G"},
			Ports:       intent.PortMapping{HTTP: 8090, GRPC: 50051},
			InstallPath: filepath.Join(dir, "install"),
			Build: &intent.BuildSpec{
				Source:               src,
				Revision:             "HEAD",
				JDK:                  "8",
				BuilderImageOverride: "test-image@sha256:abcdef1234567890",
			},
		}},
	}

	store, st := freshStore(t)
	res1, err := Apply(context.Background(), Options{
		Intent:         in,
		Target:         &fakeTarget{},
		Store:          store,
		State:          st,
		IntentHash:     "same-intent-hash",
		TemplateDir:    "",
		DeploymentsDir: filepath.Join(dir, "deployments"),
	})
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if res1.Outcome != "created" {
		t.Fatalf("first apply outcome = %q; want created", res1.Outcome)
	}

	// Plant an untracked source file. intent.yaml does NOT change.
	if err := os.WriteFile(filepath.Join(src, "NEW.java"),
		[]byte("class Whatever {}\n"), 0o600); err != nil {
		t.Fatalf("plant dirty file: %v", err)
	}

	existing := state.ManagedNode{
		Name:          in.Name,
		IntentHash:    "same-intent-hash",
		BuildCacheKey: res1.Build.CacheKey,
		ConfigHash:    res1.ConfigHash,
		Version:       res1.Version,
		Runtime:       "jar",
	}
	ft2 := &fakeTarget{}
	res2, err := Apply(context.Background(), Options{
		Intent:         in,
		Target:         ft2,
		Store:          store,
		State:          st,
		IntentHash:     "same-intent-hash",
		Existing:       &existing,
		TemplateDir:    "",
		DeploymentsDir: filepath.Join(dir, "deployments"),
	})
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	// Source changed → build cache key MUST differ → outcome MUST be
	// the `updated` path, not no_change. Precise assertion (not just
	// !no_change) so a future bug that drops outcome entirely would
	// also trip this guard.
	if res2.Outcome != "updated" {
		t.Fatalf("dirty source should trigger updated; got %q (Phase 2 dev-loop bug regressed?)", res2.Outcome)
	}
	if res2.Build.CacheKey == res1.Build.CacheKey {
		t.Error("dirty source did not produce a new cache key")
	}
}

// TestApply_CleanCacheHitNoChange is the matched pair to
// TestApply_DirtySourceTriggersRebuild: with the source tree
// unchanged AND intent unchanged, a second Apply MUST short-circuit
// as no_change and surface CacheHit=true in the build summary. This
// pins the OTHER half of the new idempotency gate.
func TestApply_CleanCacheHitNoChange(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	src := filepath.Join(dir, "java-tron")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initGitRepo(t, src)

	restore := build.SetTestRunner(&fakeBuilderRunner{})
	defer restore()

	in := &intent.Intent{
		Name:    "dev-node",
		Network: "nile",
		Target:  intent.Target{Type: "local", Runtime: "jar"},
		Nodes: []intent.NodeSpec{{
			Type:        "fullnode",
			Version:     "4.8.1",
			Resources:   intent.Resources{Memory: "4G"},
			Ports:       intent.PortMapping{HTTP: 8090, GRPC: 50051},
			InstallPath: filepath.Join(dir, "install"),
			Build: &intent.BuildSpec{
				Source:               src,
				Revision:             "HEAD",
				JDK:                  "8",
				Artifact:             "jar",
				BuilderImageOverride: "test-image@sha256:abcdef1234567890",
			},
		}},
	}

	store, st := freshStore(t)
	res1, err := Apply(context.Background(), Options{
		Intent:         in,
		Target:         &fakeTarget{},
		Store:          store,
		State:          st,
		IntentHash:     "stable-hash",
		TemplateDir:    "",
		DeploymentsDir: filepath.Join(dir, "deployments"),
	})
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}

	existing := state.ManagedNode{
		Name:          in.Name,
		IntentHash:    "stable-hash",
		BuildCacheKey: res1.Build.CacheKey,
		ConfigHash:    res1.ConfigHash,
		Version:       res1.Version,
		Runtime:       "jar",
	}
	ft2 := &fakeTarget{}
	res2, err := Apply(context.Background(), Options{
		Intent:         in,
		Target:         ft2,
		Store:          store,
		State:          st,
		IntentHash:     "stable-hash",
		Existing:       &existing,
		TemplateDir:    "",
		DeploymentsDir: filepath.Join(dir, "deployments"),
	})
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	if res2.Outcome != "no_change" {
		t.Errorf("clean repeat apply should be no_change; got %q", res2.Outcome)
	}
	if res2.Build == nil {
		t.Fatal("no_change result must still carry the Build summary")
	}
	if !res2.Build.CacheHit {
		t.Error("Build.CacheHit should be true on cache-hit no-op path")
	}
	if res2.Build.CacheKey != res1.Build.CacheKey {
		t.Errorf("cache key drift: %q vs %q", res2.Build.CacheKey, res1.Build.CacheKey)
	}
	if len(ft2.chmodPaths) != 1 || ft2.chmodPaths[0] != filepath.Join(in.Nodes[0].InstallPath, "config.conf") {
		t.Errorf("Chmod calls = %v, want jar config path", ft2.chmodPaths)
	}
	if ft2.chmodPerms[0] != 0o600 {
		t.Errorf("Chmod mode = %#o, want 0600", ft2.chmodPerms[0])
	}
}

// TestApply_RuntimeMustMatchArtifact pins the Phase 2 limitation
// surfaced in self-review: `runtime: docker` with `build:` (default
// artifact=jar) would render a compose with empty `image:` field
// because the Image default is suppressed when Build is set.
// validateOptions rejects this until Phase 3 wires artifact=image
// into the docker render path.
func TestApply_RuntimeMustMatchArtifact(t *testing.T) {
	cases := []struct {
		name     string
		runtime  string
		artifact string
		wantErr  bool
	}{
		{"docker+jar rejected", "docker", "jar", true},
		{"jar+image rejected", "jar", "image", true},
		{"jar+jar accepted", "jar", "jar", false},
		// docker+image: would be accepted by validateOptions, but
		// Run() rejects with NOT_IMPLEMENTED in Phase 2 (build.go).
		// Test that limit elsewhere; this test is the validate gate.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := &intent.Intent{
				Name:    "x",
				Network: "nile",
				Target:  intent.Target{Type: "local", Runtime: tc.runtime},
				Nodes: []intent.NodeSpec{{
					Type: "fullnode",
					Build: &intent.BuildSpec{
						Source:   "/tmp/x",
						Artifact: tc.artifact,
						ImageTag: "trond-test:dev", // placate the artifact=image required check
					},
				}},
			}
			store, st := freshStore(t)
			// Test validateOptions directly — we only care about
			// the gate decision, not the downstream build resolution
			// (which would otherwise need a real git repo).
			err := validateOptions(Options{
				Intent:     in,
				Target:     &fakeTarget{},
				Store:      store,
				State:      st,
				IntentHash: "doesnt-matter",
			})
			got := err != nil
			if got != tc.wantErr {
				t.Errorf("err = %v (got=%v); wantErr=%v", err, got, tc.wantErr)
			}
		})
	}
}

// TestApply_RejectsBuildAndImageMutex is the defense-in-depth check
// at apply.Apply: callers bypassing intent.Validate() still get
// rejected if they wired both Build and Image on the same node.
func TestApply_RejectsBuildAndImageMutex(t *testing.T) {
	in := &intent.Intent{
		Name:    "bad",
		Network: "nile",
		Target:  intent.Target{Type: "local"},
		Nodes: []intent.NodeSpec{{
			Type:  "fullnode",
			Image: "tronprotocol/java-tron:latest",
			Build: &intent.BuildSpec{Source: "/tmp/x"},
		}},
	}
	store, st := freshStore(t)
	_, err := Apply(context.Background(), Options{
		Intent:     in,
		Target:     &fakeTarget{},
		Store:      store,
		State:      st,
		IntentHash: "doesnt-matter",
	})
	if err == nil {
		t.Fatal("expected mutex error from validateOptions")
	}
}

// TestApply_BuildKeyLandsInState exercises the state persistence
// branch via a synthetic Result construction. Confirms the
// ManagedNode.BuildCacheKey field is wired.
func TestApply_BuildKeyLandsInState(t *testing.T) {
	mn := state.ManagedNode{Name: "x"}
	mn.BuildCacheKey = "abc123def456-bd4e2a1a+dirty-7f2a3b9c"
	// Round-trip via JSON to assert the json tag is correct.
	st := state.DeploymentState{Version: 1, Nodes: []state.ManagedNode{mn}}
	tmpDir := t.TempDir()
	paths.SetBaseDir(tmpDir)
	t.Cleanup(func() { paths.SetBaseDir("") })
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Save(&st); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Nodes[0].BuildCacheKey != mn.BuildCacheKey {
		t.Errorf("BuildCacheKey not persisted: got %q; want %q",
			loaded.Nodes[0].BuildCacheKey, mn.BuildCacheKey)
	}
}
