package runtime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"

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

	// Download jar if URL provided and jar not yet present
	if opts.JarURL != "" {
		if err := r.downloadJar(ctx, opts.JarURL, opts.JarPath, opts.JarSHA256); err != nil {
			return fmt.Errorf("download jar: %w", err)
		}
	}

	// Write config file. Tracked: systemd has no idea this file exists,
	// and java-tron parses it once at JVM startup.
	tracker := newChangeTracker(r.target)
	configPath := filepath.Join(installPath, "config.conf")
	if err := tracker.write(ctx, configPath, opts.ConfigData, 0644); err != nil {
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

	out, err := r.target.Exec(ctx, "journalctl", args...)
	if err != nil {
		return nil, fmt.Errorf("journalctl: %w", err)
	}

	return io.NopCloser(bytes.NewReader(out)), nil
}

// downloadJar downloads the jar file and verifies its SHA256 hash.
func (r *JarRuntime) downloadJar(ctx context.Context, url, destPath, expectedSHA256 string) error {
	// Check if jar already exists with correct hash
	if expectedSHA256 != "" {
		out, err := r.target.Exec(ctx, "sha256sum", destPath)
		if err == nil {
			fields := strings.Fields(string(out))
			if len(fields) > 0 && fields[0] == expectedSHA256 {
				return nil // Already downloaded and verified
			}
		}
	}

	// Download
	if _, err := r.target.Exec(ctx, "curl", "-fSL", "-o", destPath, url); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}

	// Verify hash
	if expectedSHA256 != "" {
		out, err := r.target.Exec(ctx, "sha256sum", destPath)
		if err != nil {
			return fmt.Errorf("sha256sum: %w", err)
		}
		fields := strings.Fields(string(out))
		if len(fields) == 0 || fields[0] != expectedSHA256 {
			return fmt.Errorf("SHA256 mismatch: expected %s, got %s", expectedSHA256, fields[0])
		}
	}

	return nil
}

// Ensure JarRuntime implements Runtime
var _ Runtime = (*JarRuntime)(nil)
