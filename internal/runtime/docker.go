package runtime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/tronprotocol/tron-deployment/internal/target"
)

// DockerRuntime manages nodes via docker compose.
type DockerRuntime struct {
	target  target.Target
	workDir string // Directory containing compose files
}

// NewDockerRuntime creates a DockerRuntime with the given target and working directory.
func NewDockerRuntime(t target.Target, workDir string) *DockerRuntime {
	return &DockerRuntime{
		target:  t,
		workDir: workDir,
	}
}

func (r *DockerRuntime) Deploy(ctx context.Context, opts DeployOpts) error {
	dir := filepath.Join(r.workDir, opts.Name)

	// Create deployment directory, owner-only.
	//
	// The mode is set by `mkdir -m` at creation instead of a follow-up
	// `chmod 0700 <dir>`: a separate chmod would leave a window in which
	// the directory (and the secret-bearing config written into it) is
	// world-readable, and it would apply the mode to a path that could
	// have been substituted in the meantime. A bare `mkdir -p` takes the
	// target's umask, which is 0755 on a typical host — on an SSH target
	// or a shared --state-dir that makes the config below reachable by
	// every local account. `-p` still succeeds on an existing directory
	// (whose mode it leaves alone), so redeploys are unaffected.
	if _, err := r.target.Exec(ctx, "mkdir", "-p", "-m", "0700", dir); err != nil {
		return fmt.Errorf("create deploy dir: %w", err)
	}

	// Write config file, owner-only: for witness nodes the rendered HOCON
	// inlines the block-signing private key (localwitness = ["<hex>"]),
	// plus whatever secrets config_overrides carries. Nothing needs it
	// world-readable — compose mounts it into the container read-only and
	// the java-tron image declares no USER, so the process that reads it
	// is root inside the container.
	configPath := filepath.Join(dir, opts.Name+".conf")
	if err := r.target.WriteFile(ctx, configPath, opts.ConfigData, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	// Write compose file
	composePath := filepath.Join(dir, "docker-compose.yaml")
	if err := r.target.WriteFile(ctx, composePath, opts.ComposeData, 0644); err != nil {
		return fmt.Errorf("write compose: %w", err)
	}

	// Docker compose up
	args := []string{"compose", "-f", composePath, "-p", opts.Name, "up", "-d"}
	if _, err := r.target.Exec(ctx, "docker", args...); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}

	return nil
}

func (r *DockerRuntime) Start(ctx context.Context, name string) error {
	composePath := filepath.Join(r.workDir, name, "docker-compose.yaml")
	_, err := r.target.Exec(ctx, "docker", "compose", "-f", composePath, "-p", name, "start")
	return err
}

func (r *DockerRuntime) Stop(ctx context.Context, name string) error {
	composePath := filepath.Join(r.workDir, name, "docker-compose.yaml")
	_, err := r.target.Exec(ctx, "docker", "compose", "-f", composePath, "-p", name, "stop")
	return err
}

func (r *DockerRuntime) Remove(ctx context.Context, name string, purge bool) error {
	composePath := filepath.Join(r.workDir, name, "docker-compose.yaml")
	args := []string{"compose", "-f", composePath, "-p", name, "down"}
	if purge {
		args = append(args, "-v") // Remove volumes too
	}
	_, err := r.target.Exec(ctx, "docker", args...)
	if err != nil {
		return fmt.Errorf("docker compose down: %w", err)
	}

	if purge {
		// Remove the deploy directory
		dir := filepath.Join(r.workDir, name)
		if _, err := r.target.Exec(ctx, "rm", "-rf", dir); err != nil {
			return fmt.Errorf("remove deploy dir: %w", err)
		}
	}

	return nil
}

func (r *DockerRuntime) Status(ctx context.Context, name string) (*NodeStatus, error) {
	composePath := filepath.Join(r.workDir, name, "docker-compose.yaml")
	out, err := r.target.Exec(ctx, "docker", "compose", "-f", composePath, "-p", name, "ps", "--format", "json")
	if err != nil {
		return &NodeStatus{Name: name, Status: "unknown"}, nil
	}

	output := strings.TrimSpace(string(out))
	if output == "" || output == "[]" {
		return &NodeStatus{Name: name, Status: "stopped"}, nil
	}

	// Simple status detection from docker compose ps output
	status := "unknown"
	if strings.Contains(output, "running") {
		status = "running"
	} else if strings.Contains(output, "exited") {
		status = "stopped"
	}

	return &NodeStatus{Name: name, Status: status}, nil
}

// Logs streams java-tron's application log out of the container.
//
// The official tronprotocol/java-tron image's entrypoint execs ./bin/FullNode
// directly and FullNode's logback config writes everything to
// /java-tron/logs/tron.log — almost nothing reaches stdout/stderr. So
// `docker compose logs` returns empty by default. We tail the file inside
// the container instead. This also matches what trond's storage volume
// already persists (so users see the same data either way).
//
// docker compose logs is kept as the fallback for the rare case where the
// log file isn't there yet (very first second of container life, or a
// non-standard image). On any error from `tail`, we return the docker
// compose output instead of failing.
func (r *DockerRuntime) Logs(ctx context.Context, name string, opts LogOpts) (io.ReadCloser, error) {
	tail := opts.Tail
	if tail <= 0 {
		tail = 100
	}
	tailArgs := []string{"exec", name, "tail", "-n", fmt.Sprintf("%d", tail)}
	if opts.Follow {
		tailArgs = append(tailArgs, "-f")
	}
	tailArgs = append(tailArgs, "/java-tron/logs/tron.log")

	if out, err := r.target.Exec(ctx, "docker", tailArgs...); err == nil {
		return io.NopCloser(bytes.NewReader(out)), nil
	}

	// Fallback: docker compose logs (covers non-standard images or pre-startup state).
	composePath := filepath.Join(r.workDir, name, "docker-compose.yaml")
	args := []string{"compose", "-f", composePath, "-p", name, "logs"}
	if opts.Tail > 0 {
		args = append(args, "--tail", fmt.Sprintf("%d", opts.Tail))
	}
	if opts.Follow {
		args = append(args, "-f")
	}
	out, err := r.target.Exec(ctx, "docker", args...)
	if err != nil {
		return nil, fmt.Errorf("docker compose logs: %w", err)
	}
	return io.NopCloser(bytes.NewReader(out)), nil
}

// WorkDir returns the path to the deployment work directory for this node.
func (r *DockerRuntime) WorkDir(name string) string {
	return filepath.Join(r.workDir, name)
}

// Ensure DockerRuntime implements Runtime
var _ Runtime = (*DockerRuntime)(nil)
