package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/target"
)

// JarRuntime manages nodes via direct jar execution + systemd.
type JarRuntime struct {
	target           target.Target
	purgeInstallPath string // set by SetPurgeInstallPath; empty means "skip purge"
}

// NewJarRuntime creates a JarRuntime with the given target.
func NewJarRuntime(t target.Target) *JarRuntime {
	return &JarRuntime{target: t}
}

func (r *JarRuntime) Deploy(ctx context.Context, opts DeployOpts) error {
	installPath := filepath.Dir(opts.JarPath)

	// Create installation directory
	if _, err := r.target.Exec(ctx, "mkdir", "-p", installPath); err != nil {
		return fmt.Errorf("create install dir: %w", err)
	}

	tracker := newChangeTracker(r.target)
	// Apply skips a pinned artifact already installed; upgrades download first.
	if opts.JarURL != "" {
		before, err := r.target.Sha256IfExists(ctx, opts.JarPath)
		if err != nil {
			return fmt.Errorf("hash existing jar: %w", err)
		}
		if opts.JarSHA256 != "" && before == opts.JarSHA256 && before == opts.ArtifactSHA256 {
			// The artifact and recorded state agree: clean no-op.
		} else {
			// A matching file with missing/stale state may represent an
			// interrupted deploy. Reinstall and restart so the process converges.
			staleArtifactState := opts.JarSHA256 != "" && before == opts.JarSHA256
			if err := r.downloadJar(ctx, opts.JarURL, opts.JarPath, opts.JarSHA256); err != nil {
				return fmt.Errorf("download jar: %w", err)
			}
			after, err := r.target.Sha256IfExists(ctx, opts.JarPath)
			if err != nil {
				return fmt.Errorf("hash installed jar: %w", err)
			}
			tracker.changed = before != after || staleArtifactState
		}
	}

	// Write config file. Tracked: systemd has no idea this file exists,
	// and java-tron parses it once at JVM startup.
	configPath := filepath.Join(installPath, "config.conf")
	if err := tracker.write(ctx, configPath, opts.ConfigData, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	// Write systemd unit file. Also tracked: daemon-reload below makes
	// systemd read the new unit, but it does not restart a unit that is
	// already running, so a changed ExecStart (new JVM args, new jar
	// path) would not reach the process.
	unitName := fmt.Sprintf("tron-%s.service", opts.Name)
	unitPath := filepath.Join("/etc/systemd/system", unitName)
	if err := tracker.write(ctx, unitPath, opts.SystemdData, 0644); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}

	// Reload systemd and start service
	if _, err := r.target.Exec(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}

	// Set environment variables for the service.
	//
	// All of them go into one drop-in. The previous version wrote the file
	// once per key inside the loop, each write replacing the last, so only
	// one variable survived — and which one was whatever Go's randomised
	// map iteration visited last. A witness node whose key arrives via
	// EnvVars alongside anything else would start with the key missing on
	// some deploys and present on others.
	if len(opts.EnvVars) > 0 {
		overridePath := fmt.Sprintf("/etc/systemd/system/%s.d", unitName)
		if _, err := r.target.Exec(ctx, "mkdir", "-p", overridePath); err != nil {
			return fmt.Errorf("create override dir: %w", err)
		}
		var sb strings.Builder
		sb.WriteString("[Service]\n")
		for _, key := range slices.Sorted(maps.Keys(opts.EnvVars)) {
			fmt.Fprintf(&sb, "Environment=%s=%s\n", key, opts.EnvVars[key])
		}
		// Tracked for the same reason as the unit file: a changed
		// Environment= only reaches the process across a restart.
		envPath := filepath.Join(overridePath, "env.conf")
		if err := tracker.write(ctx, envPath, []byte(sb.String()), 0600); err != nil {
			return fmt.Errorf("write env override: %w", err)
		}
	} else {
		// An empty EnvVars map means the service must not inherit a drop-in
		// from an earlier deploy. Remove it and track the change so a running
		// JVM cannot retain the old environment.
		envPath := filepath.Join(
			fmt.Sprintf("/etc/systemd/system/%s.d", unitName), "env.conf")
		if err := tracker.remove(ctx, envPath); err != nil {
			return fmt.Errorf("remove env override: %w", err)
		}
	}

	if _, err := r.target.Exec(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload after env: %w", err)
	}

	if _, err := r.target.Exec(ctx, "systemctl", "enable", "--now", unitName); err != nil {
		return fmt.Errorf("enable + start service: %w", err)
	}

	// enable --now starts a stopped unit; it does nothing to a running
	// one. Without this an existing node would keep its old config, unit
	// and environment while the deploy reported success.
	if tracker.changed {
		if _, err := r.target.Exec(ctx, "systemctl", "restart", unitName); err != nil {
			return fmt.Errorf("restart after config change: %w", err)
		}
	}

	return nil
}

// ArtifactSHA256 returns the digest of a deployed JAR.
func (r *JarRuntime) ArtifactSHA256(ctx context.Context, path string) (string, error) {
	return r.target.Sha256IfExists(ctx, path)
}

func (r *JarRuntime) Start(ctx context.Context, name string) error {
	unitName := fmt.Sprintf("tron-%s.service", name)
	_, err := r.target.Exec(ctx, "systemctl", "start", unitName)
	return err
}

func (r *JarRuntime) Stop(ctx context.Context, name string) error {
	unitName := fmt.Sprintf("tron-%s.service", name)
	_, err := r.target.Exec(ctx, "systemctl", "stop", unitName)
	return err
}

// Remove tears down a jar-runtime node: stops + disables the service,
// removes the unit file and any drop-in overrides, reloads systemd, and
// (when purge is set) wipes the install directory.
//
// Failures of stop/disable are best-effort — the node may already be
// down — but failures to remove the unit file or to reload systemd are
// surfaced because they leave the system in a partially-removed state.
//
// The caller passes installPath via DeployOpts.JarPath (its parent dir)
// for purge to delete; a previous version of this method silently
// dropped purge with a TODO.
func (r *JarRuntime) Remove(ctx context.Context, name string, purge bool) error {
	unitName := fmt.Sprintf("tron-%s.service", name)

	// Best-effort stop + disable. Both can legitimately fail if the
	// service is already in that state.
	_, _ = r.target.Exec(ctx, "systemctl", "stop", unitName)
	_, _ = r.target.Exec(ctx, "systemctl", "disable", unitName)

	unitPath := filepath.Join("/etc/systemd/system", unitName)
	if _, err := r.target.Exec(ctx, "rm", "-f", unitPath); err != nil {
		return fmt.Errorf("remove unit file %s: %w", unitPath, err)
	}

	overridePath := fmt.Sprintf("/etc/systemd/system/%s.d", unitName)
	if _, err := r.target.Exec(ctx, "rm", "-rf", overridePath); err != nil {
		return fmt.Errorf("remove override dir %s: %w", overridePath, err)
	}

	if _, err := r.target.Exec(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload after remove: %w", err)
	}

	if purge && r.purgeInstallPath != "" {
		// rm -rf the install root. Refuse "/" or "" out of paranoia —
		// callers always derive this from intent.install_path which
		// defaults to /opt/tron, but a misconfigured intent shouldn't
		// nuke the host.
		p := r.purgeInstallPath
		if p == "/" || p == "" {
			return fmt.Errorf("refusing to purge install_path %q", p)
		}
		if _, err := r.target.Exec(ctx, "rm", "-rf", p); err != nil {
			return fmt.Errorf("purge install dir %s: %w", p, err)
		}
	}

	return nil
}

// SetPurgeInstallPath records the install root that Remove(purge=true)
// should wipe. Callers that have access to the managed-node state set
// this before invoking Remove; absent it, purge is a no-op (preferable
// to guessing).
func (r *JarRuntime) SetPurgeInstallPath(p string) {
	r.purgeInstallPath = p
}

func (r *JarRuntime) Status(ctx context.Context, name string) (*NodeStatus, error) {
	unitName := fmt.Sprintf("tron-%s.service", name)
	out, err := r.target.Exec(ctx, "systemctl", "is-active", unitName)
	if err != nil {
		// systemctl exits non-zero for inactive/failed
		output := strings.TrimSpace(string(out))
		switch output {
		case "inactive":
			return &NodeStatus{Name: name, Status: "stopped"}, nil
		case "failed":
			return &NodeStatus{Name: name, Status: "error"}, nil
		default:
			return &NodeStatus{Name: name, Status: "unknown"}, nil
		}
	}

	return &NodeStatus{Name: name, Status: "running"}, nil
}

func (r *JarRuntime) Logs(ctx context.Context, name string, opts LogOpts) (io.ReadCloser, error) {
	unitName := fmt.Sprintf("tron-%s.service", name)
	args := []string{"-u", unitName, "--no-pager"}
	if opts.Tail > 0 {
		args = append(args, "-n", fmt.Sprintf("%d", opts.Tail))
	}
	if opts.Follow {
		args = append(args, "-f")
	}
	if opts.Follow {
		if stream, ok := r.target.(target.StreamExec); ok {
			if reader, err := stream.StreamExec(ctx, "journalctl", args...); err == nil {
				return reader, nil
			}
		}
	}

	out, err := r.target.Exec(ctx, "journalctl", args...)
	if err != nil {
		return nil, fmt.Errorf("journalctl: %w", err)
	}

	return io.NopCloser(bytes.NewReader(out)), nil
}

// downloadJar downloads the jar file and verifies its SHA256 hash.
func (r *JarRuntime) downloadJar(ctx context.Context, url, destPath, expectedSHA256 string) error {
	// Download to a temporary path so a failed transfer or checksum never
	// destroys the currently running artifact.
	tmpPath := destPath + ".upgrade.tmp"
	if remote, ok := r.target.(interface{ IsRemote() bool }); ok && remote.IsRemote() {
		return r.downloadJarLocally(ctx, url, tmpPath, destPath, expectedSHA256)
	}
	if _, err := r.target.Exec(ctx, "curl", "-fSL", "-o", tmpPath, url); err != nil {
		_, _ = r.target.Exec(ctx, "rm", "-f", tmpPath)
		return fmt.Errorf("download %s: %w", url, err)
	}

	// Verify hash
	if expectedSHA256 != "" {
		out, err := r.target.Exec(ctx, "sha256sum", tmpPath)
		if err != nil {
			_, _ = r.target.Exec(ctx, "rm", "-f", tmpPath)
			return fmt.Errorf("sha256sum: %w", err)
		}
		fields := strings.Fields(string(out))
		if len(fields) == 0 {
			_, _ = r.target.Exec(ctx, "rm", "-f", tmpPath)
			return fmt.Errorf("SHA256 verification returned no digest; expected %s", expectedSHA256)
		}
		if fields[0] != expectedSHA256 {
			_, _ = r.target.Exec(ctx, "rm", "-f", tmpPath)
			return fmt.Errorf("SHA256 mismatch: expected %s, got %s", expectedSHA256, fields[0])
		}
	}
	if _, err := r.target.Exec(ctx, "mv", tmpPath, destPath); err != nil {
		_, _ = r.target.Exec(ctx, "rm", "-f", tmpPath)
		return fmt.Errorf("install downloaded jar: %w", err)
	}

	return nil
}

func (r *JarRuntime) downloadJarLocally(ctx context.Context, url, remoteTmp, destPath, expectedSHA256 string) error {
	const maxJARBytes int64 = 512 << 20
	f, err := os.CreateTemp("", "trond-jar-")
	if err != nil {
		return fmt.Errorf("create local download: %w", err)
	}
	localPath := f.Name()
	defer os.Remove(localPath)
	defer f.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("download %s: http %s", url, resp.Status)
	}
	if resp.ContentLength > maxJARBytes {
		return fmt.Errorf("download %s exceeds maximum JAR size of %d bytes", url, maxJARBytes)
	}
	if written, err := io.Copy(f, io.LimitReader(resp.Body, maxJARBytes+1)); err != nil {
		return fmt.Errorf("save local download: %w", err)
	} else if written > maxJARBytes {
		return fmt.Errorf("download %s exceeds maximum JAR size of %d bytes", url, maxJARBytes)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close local download: %w", err)
	}
	if expectedSHA256 != "" {
		check, err := os.Open(localPath)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, copyErr := io.Copy(h, check)
		_ = check.Close()
		if copyErr != nil {
			return copyErr
		}
		got := fmt.Sprintf("%x", h.Sum(nil))
		if got != expectedSHA256 {
			return fmt.Errorf("SHA256 mismatch: expected %s, got %s", expectedSHA256, got)
		}
	}
	if err := r.target.PutFile(ctx, localPath, remoteTmp); err != nil {
		r.cleanupRemoteTmp(remoteTmp)
		return fmt.Errorf("upload jar: %w", err)
	}
	if _, err := r.target.Exec(ctx, "mv", remoteTmp, destPath); err != nil {
		r.cleanupRemoteTmp(remoteTmp)
		return fmt.Errorf("install downloaded jar: %w", err)
	}
	return nil
}

func (r *JarRuntime) cleanupRemoteTmp(path string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = r.target.Exec(cleanupCtx, "rm", "-f", path)
}

// Ensure JarRuntime implements Runtime
var _ Runtime = (*JarRuntime)(nil)
