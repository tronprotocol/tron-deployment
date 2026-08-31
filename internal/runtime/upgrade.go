package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+\-]*$`)

func validateUpgradeVersion(version string) error {
	if !versionPattern.MatchString(version) {
		return fmt.Errorf("invalid version/image tag %q", version)
	}
	return nil
}

var composeImageLine = regexp.MustCompile(`(?m)^(\s*image:\s*)(\S+)`)

// swapComposeImageTag replaces the tag on the single image used by a node.
// Digests are deliberately discarded: an explicit version switch must select
// the requested tag rather than continue pinning the old artifact.
func swapComposeImageTag(compose []byte, newVersion string) (out []byte, oldImage, newImage string, err error) {
	matches := composeImageLine.FindAllSubmatchIndex(compose, -1)
	if len(matches) == 0 {
		return nil, "", "", fmt.Errorf("compose file has no image line")
	}
	for _, m := range matches {
		image := string(compose[m[4]:m[5]])
		if oldImage == "" {
			oldImage = image
		} else if image != oldImage {
			return nil, "", "", fmt.Errorf("compose file contains multiple different images: %q and %q", oldImage, image)
		}
	}
	base := oldImage
	if at := strings.IndexByte(base, '@'); at >= 0 {
		base = base[:at]
	}
	colon := strings.LastIndexByte(base, ':')
	slash := strings.LastIndexByte(base, '/')
	if colon > slash {
		base = base[:colon]
	}
	newImage = base + ":" + newVersion
	if oldImage == newImage {
		return compose, oldImage, newImage, nil
	}
	out = composeImageLine.ReplaceAllFunc(compose, func(line []byte) []byte {
		m := composeImageLine.FindSubmatchIndex(line)
		return append(append(append([]byte{}, line[:m[3]]...), newImage...), line[m[5]:]...)
	})
	return out, oldImage, newImage, nil
}

type dockerArtifactTransaction struct {
	r            *DockerRuntime
	name         string
	path         string
	old, updated []byte
}

func (t *dockerArtifactTransaction) Activate(ctx context.Context) error {
	return t.r.target.WriteFile(ctx, t.path, t.updated, 0644)
}
func (t *dockerArtifactTransaction) Start(ctx context.Context) error {
	_, err := t.r.target.Exec(ctx, "docker", "compose", "-f", t.path, "-p", t.name, "up", "-d", "--force-recreate")
	return err
}
func (t *dockerArtifactTransaction) Rollback(ctx context.Context) error {
	return t.r.target.WriteFile(ctx, t.path, t.old, 0644)
}
func (t *dockerArtifactTransaction) Cleanup(context.Context) error { return nil }

func (r *DockerRuntime) PrepareArtifact(ctx context.Context, name string, opts UpgradeOpts) (ArtifactTransaction, error) {
	if err := validateUpgradeVersion(opts.Version); err != nil {
		return nil, err
	}
	composePath := filepath.Join(r.workDir, name, "docker-compose.yaml")
	compose, err := r.target.ReadFile(ctx, composePath)
	if err != nil {
		return nil, fmt.Errorf("read compose: %w", err)
	}
	updated, _, newImage, err := swapComposeImageTag(compose, opts.Version)
	if err != nil {
		return nil, err
	}
	if string(updated) == string(compose) {
		return &dockerArtifactTransaction{r: r, name: name, path: composePath, old: compose, updated: updated}, nil
	}
	pullCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if _, err := r.target.Exec(pullCtx, "docker", "pull", newImage); err != nil {
		return nil, fmt.Errorf("docker pull %s: %w", newImage, err)
	}
	return &dockerArtifactTransaction{r: r, name: name, path: composePath, old: compose, updated: updated}, nil
}

type jarArtifactTransaction struct {
	r                             *JarRuntime
	name, candidate, live, backup string
	activated, restore            bool
}

func (t *jarArtifactTransaction) Activate(ctx context.Context) error {
	if t.restore {
		if _, err := t.r.target.Exec(ctx, "mv", t.live, t.candidate); err != nil {
			return fmt.Errorf("stage current jar: %w", err)
		}
		if _, err := t.r.target.Exec(ctx, "mv", t.backup, t.live); err != nil {
			_, _ = t.r.target.Exec(ctx, "mv", t.candidate, t.live)
			return fmt.Errorf("restore backup: %w", err)
		}
		t.activated = true
		return nil
	}
	if _, err := t.r.target.Exec(ctx, "mv", t.live, t.backup); err != nil {
		return fmt.Errorf("backup jar: %w", err)
	}
	if _, err := t.r.target.Exec(ctx, "mv", t.candidate, t.live); err != nil {
		if _, restoreErr := t.r.target.Exec(ctx, "mv", t.backup, t.live); restoreErr != nil {
			return fmt.Errorf("install jar %s: %w; restore backup %s to %s: %v", t.candidate, err, t.backup, t.live, restoreErr)
		}
		return fmt.Errorf("install jar: %w", err)
	}
	t.activated = true
	return nil
}
func (t *jarArtifactTransaction) Start(ctx context.Context) error {
	_, err := t.r.target.Exec(ctx, "systemctl", "start", fmt.Sprintf("tron-%s.service", t.name))
	return err
}
func (t *jarArtifactTransaction) Rollback(ctx context.Context) error {
	if !t.activated {
		return nil
	}
	if _, err := t.r.target.Exec(ctx, "mv", t.live, t.candidate); err != nil {
		return err
	}
	if _, err := t.r.target.Exec(ctx, "mv", t.backup, t.live); err != nil {
		if _, restoreErr := t.r.target.Exec(ctx, "mv", t.candidate, t.live); restoreErr != nil {
			return fmt.Errorf("restore backup %s to %s: %w; restore candidate %s to %s: %v", t.backup, t.live, err, t.candidate, t.live, restoreErr)
		}
		return fmt.Errorf("restore backup %s to %s: %w", t.backup, t.live, err)
	}
	return nil
}
func (t *jarArtifactTransaction) Cleanup(ctx context.Context) error {
	args := []string{"-f", t.candidate, t.backup}
	if os.Getenv("TROND_PRESERVE_BACKUP") == "1" {
		args = []string{"-f", t.candidate}
	}
	_, err := t.r.target.Exec(ctx, "rm", args...)
	return err
}

func (r *JarRuntime) PrepareArtifact(ctx context.Context, name string, opts UpgradeOpts) (ArtifactTransaction, error) {
	if err := validateUpgradeVersion(opts.Version); err != nil {
		return nil, err
	}
	if r.purgeInstallPath == "" {
		return nil, fmt.Errorf("install path is unavailable for artifact upgrade")
	}
	jarPath := filepath.Join(r.purgeInstallPath, "FullNode.jar")
	candidate := jarPath + ".candidate"
	backup := jarPath + ".upgrade.backup"
	if opts.JarURL == "" {
		if os.Getenv("TROND_NETWORK_UPGRADE") != "1" {
			return nil, fmt.Errorf("jar URL is required for artifact upgrade; provide --jar-url")
		}
		// A network rollback consumes the backup made by the preceding
		// upgrade. No download or operator-supplied URL is needed.
		if _, err := r.target.Exec(ctx, "test", "-e", backup); err != nil {
			return nil, fmt.Errorf("no upgrade backup found; cannot restore network upgrade")
		}
		return &jarArtifactTransaction{r: r, name: name, candidate: candidate, live: jarPath, backup: backup, restore: true}, nil
	}
	if err := r.downloadJar(ctx, opts.JarURL, candidate, opts.JarSHA256); err != nil {
		_, _ = r.target.Exec(ctx, "rm", "-f", candidate)
		return nil, fmt.Errorf("download jar: %w", err)
	}
	return &jarArtifactTransaction{r: r, name: name, candidate: candidate, live: jarPath, backup: backup}, nil
}

var _ ArtifactUpgrader = (*DockerRuntime)(nil)
var _ ArtifactUpgrader = (*JarRuntime)(nil)
