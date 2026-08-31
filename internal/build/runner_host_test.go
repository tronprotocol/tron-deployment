package build

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllowedEnvPassthroughIntentOverridesHost(t *testing.T) {
	t.Setenv("GRADLE_OPTS", "host-value")
	got := allowedEnvPassthrough(map[string]string{"GRADLE_OPTS": "intent-value"})
	found := false
	for _, value := range got {
		if value == "GRADLE_OPTS=intent-value" {
			found = true
		}
		if value == "GRADLE_OPTS=host-value" {
			t.Fatalf("host value leaked after intent override: %v", got)
		}
	}
	if !found {
		t.Fatalf("intent value missing: %v", got)
	}
}

func TestHostBuildEnvIntentOverridesHostWithoutDuplicate(t *testing.T) {
	t.Setenv("GRADLE_OPTS", "host-value")
	got := hostBuildEnv(map[string]string{"GRADLE_OPTS": "intent-value"})
	count := 0
	for _, value := range got {
		if strings.HasPrefix(value, "GRADLE_OPTS=") {
			count++
			if value != "GRADLE_OPTS=intent-value" {
				t.Fatalf("host value retained: %v", got)
			}
		}
	}
	if count != 1 {
		t.Fatalf("GRADLE_OPTS entries = %d, want one: %v", count, got)
	}
}

// TestResolveHostIdentity pins the contract `realHostRunner` relies
// on: the function returns a stable digest with the canonical
// `sha256:` prefix + a human-readable ref starting with `host:`. Two
// calls in quick succession yield identical results (java -version
// output is deterministic on a given host) so the cache key is
// stable across same-host re-runs.
//
// Skipped when `java` isn't on PATH (CI runner without a JDK
// shouldn't fail on a host-builder unit test).
func TestResolveHostIdentity(t *testing.T) {
	if _, err := exec.LookPath("java"); err != nil {
		t.Skip("java not on PATH; host builder identity test needs a JVM")
	}

	ref1, digest1, err := resolveHostIdentity(context.Background())
	if err != nil {
		t.Fatalf("resolveHostIdentity: %v", err)
	}
	if !strings.HasPrefix(ref1, "host:") {
		t.Errorf("ref %q should start with 'host:' so it's visibly distinct from a docker ref", ref1)
	}
	if !strings.HasPrefix(digest1, "sha256:") {
		t.Errorf("digest %q should start with 'sha256:' (canonical OCI form)", digest1)
	}
	// Hex-suffix length: 64 chars for full sha256.
	if got := len(strings.TrimPrefix(digest1, "sha256:")); got != 64 {
		t.Errorf("digest hex length = %d; want 64 (sha256)", got)
	}

	// Stability: a second call returns the same digest. If this fails
	// the cache key would also flap and trond would never hit cache
	// for a host build.
	_, digest2, err := resolveHostIdentity(context.Background())
	if err != nil {
		t.Fatalf("second resolveHostIdentity: %v", err)
	}
	if digest1 != digest2 {
		t.Errorf("host identity digest drifted between calls: %q vs %q (cache would never hit)", digest1, digest2)
	}
}

// TestFindLargestFatJAR is the unit-test for the host runner's
// equivalent of the docker script's `find ... | xargs ls -S |
// head -n1`. The largest jar under any `*/build/libs/*.jar` wins.
func TestFindLargestFatJAR(t *testing.T) {
	srcDir := t.TempDir()

	// Multi-module layout, two candidates:
	//   framework/build/libs/FullNode.jar  (1 KB, the fat jar)
	//   common/build/libs/common.jar       (100 B, thin module)
	// Plus a decoy under a path that doesn't match the glob.
	mkJAR := func(rel string, size int) {
		t.Helper()
		p := filepath.Join(srcDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mkJAR("framework/build/libs/FullNode.jar", 1024)
	mkJAR("common/build/libs/common.jar", 100)
	mkJAR("not-build/not-libs/decoy.jar", 9999) // largest, but wrong path → must NOT be picked

	got, err := findLargestFatJAR(srcDir)
	if err != nil {
		t.Fatalf("findLargestFatJAR: %v", err)
	}
	want := filepath.Join(srcDir, "framework/build/libs/FullNode.jar")
	if got != want {
		t.Errorf("findLargestFatJAR returned %q; want %q (fat jar in correct path)", got, want)
	}
}

// TestFindLargestFatJAR_NestedAndDecoyPaths is the review-pass-4
// guard: the path filter MUST match `find -path '*/build/libs/*.jar'`
// exactly, NOT just contain `/build/libs/` anywhere. Without this
// rigour a host-built JAR and a docker-built JAR could pick
// different files out of an unusual layout, breaking the cross-
// builder cache-reuse invariant the host runner relies on.
func TestFindLargestFatJAR_NestedAndDecoyPaths(t *testing.T) {
	srcDir := t.TempDir()
	mkJAR := func(rel string, size int) {
		t.Helper()
		p := filepath.Join(srcDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Correct path — small file. Should win because the others
	// don't match the glob.
	mkJAR("framework/build/libs/FullNode.jar", 500)
	// `build/libs/sub/x.jar` — `build/libs` is in the path but not
	// the immediate parent. `find -path '*/build/libs/*.jar'`
	// matches only one level under libs/, so this should NOT win.
	mkJAR("framework/build/libs/sub/nested.jar", 9999)
	// `staging/build/libs-archive/old.jar` — has `build/` and
	// `libs-archive/` but no `build/libs/` segment. Must NOT match.
	mkJAR("staging/build/libs-archive/old.jar", 9999)
	// `foo/libs/build/x.jar` — has segments out of order. Must NOT
	// match (docker glob requires build/libs/ in that order).
	mkJAR("foo/libs/build/wrong-order.jar", 9999)

	got, err := findLargestFatJAR(srcDir)
	if err != nil {
		t.Fatalf("findLargestFatJAR: %v", err)
	}
	want := filepath.Join(srcDir, "framework/build/libs/FullNode.jar")
	if got != want {
		t.Errorf("strict glob mismatch: got %q; want %q (cross-builder cache would diverge)", got, want)
	}
}

// TestFindLargestFatJAR_NoMatches: when gradle produced nothing
// under */build/libs/*.jar, return a specific error so the caller
// can surface the right BUILD_FAILED message.
func TestFindLargestFatJAR_NoMatches(t *testing.T) {
	srcDir := t.TempDir()
	_, err := findLargestFatJAR(srcDir)
	if err == nil {
		t.Fatal("expected error when no jars present")
	}
	if !strings.Contains(err.Error(), "no .jar") {
		t.Errorf("error %q should mention 'no .jar' so the message is actionable", err)
	}
}

// TestHostRunner_RequiresGradlew: if the source tree doesn't ship a
// ./gradlew wrapper, the runner refuses up-front with a clear
// message rather than silently falling back to whatever `gradle`
// the host's PATH has. Pins the contract documented in the runner.
func TestHostRunner_RequiresGradlew(t *testing.T) {
	srcDir := t.TempDir()
	// Intentionally no gradlew file present.

	r := &resolved{
		req:         Request{Builder: "host", ArtifactKind: "jar"},
		src:         Source{Path: srcDir},
		cacheKeyStr: "test-key",
	}
	err := realHostRunner{}.RunBuild(context.Background(), r, srcDir, "")
	if err == nil {
		t.Fatal("expected error when ./gradlew is missing")
	}
	if !strings.Contains(err.Error(), "gradle wrapper not present") {
		t.Errorf("error %q should explain why; got %v", err, err)
	}
}

// TestResolveBuild_HostBuilder_JDKVersionDoesNotFragmentCacheKey
// pins the review-pass-4 fix: with Builder=host, the host's actual
// JDK is captured in BuilderImageDigest (sha256 of `java -version`).
// Including req.JDKVersion in the CacheKey on top of that would
// fragment the cache pointlessly — two host builds with --jdk 8 vs
// --jdk 17 on the SAME host (same actual JVM) would rebuild
// identically. The fix omits JDKVersion from the key when
// Builder=host so cache hits work as expected.
func TestResolveBuild_HostBuilder_JDKVersionDoesNotFragmentCacheKey(t *testing.T) {
	if _, err := exec.LookPath("java"); err != nil {
		t.Skip("java not on PATH; host builder cache-key test needs a JVM")
	}

	srcDir := initGitRepo(t)
	mk := func(jdk string) string {
		r, err := resolveBuild(context.Background(), Request{
			SourcePath:   srcDir,
			RevisionSpec: "HEAD",
			ArtifactKind: "jar",
			JDKVersion:   jdk,
			Builder:      "host",
			GradleTask:   "shadowJar",
			Platform:     "linux/amd64",
		})
		if err != nil {
			t.Fatalf("resolveBuild jdk=%q: %v", jdk, err)
		}
		return r.cacheKeyStr
	}

	keyA := mk("8")
	keyB := mk("17")
	if keyA != keyB {
		t.Errorf("host-builder cache key changes with --jdk: %q vs %q "+
			"(BuilderImageDigest already captures the actual JVM; --jdk should be ignored)",
			keyA, keyB)
	}
}

// TestResolveBuild_HostBuilderSkipsPins is the integration-level
// guard: with Builder=host, resolveBuild succeeds even when the JDK
// version has no pinned docker image. Without this branch the host
// build would fail at the pin lookup before ever reaching the host
// runner.
func TestResolveBuild_HostBuilderSkipsPins(t *testing.T) {
	if _, err := exec.LookPath("java"); err != nil {
		t.Skip("java not on PATH; host builder resolve test needs a JVM")
	}

	srcDir := initGitRepo(t)
	req := Request{
		SourcePath:   srcDir,
		RevisionSpec: "HEAD",
		ArtifactKind: "jar",
		JDKVersion:   "99", // No such pinned image — must NOT block host builds.
		Builder:      "host",
		GradleTask:   "shadowJar",
		Platform:     "linux/amd64",
	}
	r, err := resolveBuild(context.Background(), req)
	if err != nil {
		t.Fatalf("resolveBuild with Builder=host should NOT consult pins; got: %v", err)
	}
	if !strings.HasPrefix(r.imageRef, "host:") {
		t.Errorf("imageRef = %q; want 'host:'-prefixed", r.imageRef)
	}
	if !strings.HasPrefix(r.imageDigest, "sha256:") {
		t.Errorf("imageDigest = %q; want sha256:-prefixed", r.imageDigest)
	}
	if r.cacheKeyStr == "" {
		t.Error("cache key not materialized for host build")
	}
}
