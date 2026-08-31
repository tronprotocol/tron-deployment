package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// resolveHostIdentity computes the "builder identity" for a
// host-builder build: a human-readable ref + a content-addressed
// digest, both derived from `java -version` output.
//
// Java prints its version line to STDERR (a quirk historically tied
// to `-version` being a non-zero exit on very old JVMs). We capture
// stderr, take the first line as the ref (e.g. `host:openjdk 17.0.10`),
// and sha256 the full output as the digest. Two hosts whose
// `java -version` byte-strings are identical produce the same digest,
// hence the same cache key, hence cache reuse across machines with
// matching JDK installs. Differing JDK vendors, versions, or build
// numbers fall into distinct cache slots automatically — no silent
// stale-artifact reuse.
//
// We do NOT try to capture gradle wrapper version too: the wrapper
// lives in the source tree and is content-addressed via the
// PatchHash dimension of CacheKey (a wrapper bump shows up as a
// dirty patch on a clean source tree, or as a normal git-tracked
// change otherwise). The JVM is the only build-time dependency
// that's NOT part of the source tree, so it's the only one we
// need to hash externally.
func resolveHostIdentity(ctx context.Context) (ref, digest string, err error) {
	cmd := exec.CommandContext(ctx, "java", "-version")
	// java -version writes to stderr; capture both stdout and stderr
	// into the same buffer so a future JDK that switches conventions
	// doesn't silently produce an empty hash input.
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return "", "", fmt.Errorf("java -version: %w", runErr)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return "", "", fmt.Errorf("java -version produced no output")
	}

	// Ref = the first line, prefixed `host:` so it visibly differs
	// from a pinned docker ref in manifests / `trond build list`
	// tables.
	firstLine := strings.SplitN(trimmed, "\n", 2)[0]
	ref = "host:" + strings.TrimSpace(firstLine)

	sum := sha256.Sum256(out)
	digest = "sha256:" + hex.EncodeToString(sum[:])
	return ref, digest, nil
}

// realHostRunner runs the build on the host instead of inside a
// pinned eclipse-temurin container. It's selected by
// `--builder host` (or `build.builder: host` in intent) and trades
// reproducibility for speed + zero-docker-dependency: trond no longer
// needs to pull or run any image, it just invokes the source tree's
// ./gradlew wrapper directly.
//
// The host's JDK + gradle wrapper version + native toolchain
// participate in the result, so the build is only as reproducible
// as the host. The cache-key derivation in resolveBuild folds a hash
// of `java -version` into the BuilderImageDigest field so the cache
// invalidates when the host JDK changes — different host = different
// cache slot, no silent stale-artifact reuse.
//
// Supported artifact_kinds:
//
//   - jar:   run ./gradlew $task $args; find the fat jar under
//     */build/libs/; copy to outTmp. Same find heuristic as
//     the docker runner so cross-builder cache reuse is
//     possible (when the host JDK happens to match a pinned
//     digest, the JARs are byte-identical).
//   - image (gradle strategy): snapshot the host's tagged images
//     before, run ./gradlew, snapshot after. image.go's diff
//     logic then identifies the new image ID. No docker.sock
//     mount needed — we're already on the host.
//   - image (jar-wrap strategy): the inner JAR build runs through
//     this same runner (recursion in buildImageJarWrap);
//     buildImageFromJAR then docker-builds the wrap image
//     on the host. The image step itself doesn't see this
//     runner.
type realHostRunner struct{}

func (realHostRunner) RunBuild(ctx context.Context, r *resolved, outDir, outTmp string) error {
	// Sanity: ./gradlew must exist in the source tree. Without it
	// we'd fall back to whatever `gradle` the PATH gives — that's
	// surprising and breaks the "version pinned by source" guarantee
	// gradle wrappers provide.
	gradlewPath := filepath.Join(r.buildSourceDir, "gradlew")
	if _, err := os.Stat(gradlewPath); err != nil {
		return fmt.Errorf("host builder requires %s; gradle wrapper not present "+
			"(run 'gradle wrapper' in the source tree, or use --builder docker)",
			gradlewPath)
	}

	switch r.req.ArtifactKind {
	case "image":
		return hostBuildImage(ctx, r, outDir)
	default:
		return hostBuildJAR(ctx, r, outDir, outTmp)
	}
}

// hostBuildJAR runs ./gradlew, then walks the source tree for the fat
// JAR and copies it into outTmp. The find heuristic matches the
// docker runner's shell script (largest *.jar under */build/libs/)
// so a host-built JAR and a docker-built JAR pick the same file out
// of a multi-module gradle layout.
func hostBuildJAR(ctx context.Context, r *resolved, _ /* outDir */, outTmp string) error {
	if err := runGradleHost(ctx, r); err != nil {
		return err
	}
	jarPath, err := findLargestFatJAR(r.buildSourceDir)
	if err != nil {
		return err
	}
	if err := copyHostFile(jarPath, outTmp); err != nil {
		return fmt.Errorf("stage host-built jar into cache: %w", err)
	}
	return nil
}

// hostBuildImage runs the gradle docker plugin natively (gradle has
// direct access to the host docker daemon without any bind-mount —
// that's the whole point of running on the host). The before/after
// image-ID snapshots that image.go relies on are written to outDir
// just like the docker runner's wrapper script does, so the
// downstream diff + tagging logic stays unchanged.
func hostBuildImage(ctx context.Context, r *resolved, outDir string) error {
	beforePath := filepath.Join(outDir, r.cacheKeyStr+"-images-before")
	afterPath := filepath.Join(outDir, r.cacheKeyStr+"-images-after")

	if err := snapshotDockerImages(ctx, beforePath); err != nil {
		return fmt.Errorf("snapshot images before build: %w", err)
	}
	if err := runGradleHost(ctx, r); err != nil {
		return err
	}
	if err := snapshotDockerImages(ctx, afterPath); err != nil {
		return fmt.Errorf("snapshot images after build: %w", err)
	}
	return nil
}

// runGradleHost is the single point where we shell out to the source
// tree's gradle wrapper. argv-only — no shell interpretation of
// gradleTask/gradleArgs (FR-022). The wrapper itself handles version
// pinning per the source's gradle/wrapper/gradle-wrapper.properties.
func runGradleHost(ctx context.Context, r *resolved) error {
	argv := []string{r.req.GradleTask}
	argv = append(argv, r.req.GradleArgs...)

	cmd := exec.CommandContext(ctx, "./gradlew", argv...)
	cmd.Dir = r.buildSourceDir
	// In `-o json` mode the CLI redirects stdout to a JSON buffer;
	// gradle's chatter belongs on stderr regardless so it never
	// corrupts the JSON envelope. Mirrors the docker runner's choice.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Env = hostBuildEnv(r.req.Env)

	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		return fmt.Errorf("./gradlew %s: %w", strings.Join(argv, " "), err)
	}
	return nil
}

// hostBuildEnv produces the host process's env with trond's
// allowlisted passthrough applied. Same allowlist the docker runner
// uses, so semantics line up across builders: an `org.gradle.project.*`
// override or a whitelisted KEY=VALUE flows through identically.
func hostBuildEnv(intent map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	for _, entry := range allowedEnvPassthrough(intent) {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	merged := make([]string, 0, len(values))
	for key, value := range values {
		merged = append(merged, key+"="+value)
	}
	sort.Strings(merged)
	return merged
}

// findLargestFatJAR walks the source tree for jars whose IMMEDIATE
// parent is `build/libs/` and returns the largest one. Matches the
// docker runner's `find ... -path '*/build/libs/*.jar'` glob: that
// glob requires `/build/libs/` to be the path segment immediately
// preceding the jar filename (gradle's standard output layout), NOT
// just any segment in the path. A nested `build/libs/sub/x.jar`
// is intentionally ignored by both runners — and a
// `staging/build/libs-archive/old.jar` is correctly excluded too —
// so a host-built JAR and a docker-built JAR pick the same file out
// of a multi-module gradle layout (cross-builder cache reuse depends
// on byte-identical artifacts).
//
// The shadow plugin's fat JAR is always larger than thin module
// jars of the same task; ValidateJARMainClass downstream rejects
// any non-FullNode JAR that wins this size heuristic.
func findLargestFatJAR(srcPath string) (string, error) {
	type cand struct {
		path string
		size int64
	}
	var cands []cand
	walkErr := filepath.WalkDir(srcPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip unreadable subtrees rather than aborting; the
			// outer error wrapping surfaces "no jar found" if every
			// candidate failed to read.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".jar") {
			return nil
		}
		// Match `find -path '*/build/libs/*.jar'` exactly: the
		// jar's IMMEDIATE parent directory must be `build/libs`
		// (relative to some ancestor). filepath.Dir trims the
		// filename; the parent's last two components must be
		// "build" and "libs" in that order.
		parent := filepath.Dir(p)
		if filepath.Base(parent) != "libs" {
			return nil
		}
		if filepath.Base(filepath.Dir(parent)) != "build" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		cands = append(cands, cand{path: p, size: info.Size()})
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("walk source tree for built jar: %w", walkErr)
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("gradle produced no .jar under any */build/libs/ in %s", srcPath)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].size > cands[j].size
	})
	return cands[0].path, nil
}

// snapshotDockerImages writes the host's tagged image IDs to path,
// one per line, sorted-unique. Equivalent to the docker runner's
// `docker images -q --no-trunc --filter dangling=false | sort -u`.
func snapshotDockerImages(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, "docker", "images", "-q",
		"--no-trunc", "--filter", "dangling=false")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("docker images: %w", err)
	}
	// Sort+uniq in Go so we don't shell out a second time. The diff
	// logic in image.go reads these as deterministic line sets.
	seen := map[string]bool{}
	var lines []string
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		lines = append(lines, line)
	}
	sort.Strings(lines)
	body := strings.Join(lines, "\n") + "\n"
	return os.WriteFile(path, []byte(body), 0o600)
}

// copyHostFile is a minimal io.Copy wrapper (we already have
// copyFileForWrap in image_wrap.go for the same use case, but
// importing across runner_host.go's narrower surface would tangle
// dependencies). Local to this file.
func copyHostFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}
