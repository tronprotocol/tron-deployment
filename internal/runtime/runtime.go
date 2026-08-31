package runtime

import (
	"bytes"
	"context"
	"io"
	"os"

	"github.com/tronprotocol/tron-deployment/internal/target"
)

// changeTracker writes deployment files and remembers whether any of them
// replaced different content.
//
// It exists because neither orchestrator notices a changed config on its
// own. `docker compose up -d` recreates a container only when the
// *compose spec* changes, and a config delivered by bind mount appears in
// that spec as a path, never as content. `systemctl enable --now` starts
// a stopped unit but never restarts a running one, so even a reloaded
// unit file leaves the old process in place. Either way the file lands on
// disk, the deploy reports success, and the running process keeps serving
// the config it parsed at startup — with nothing in the output to say so.
//
// So Deploy tracks the files the orchestrator cannot see for itself and
// escalates the start into a recreate/restart when one of them changed.
// Files the orchestrator *does* diff (the compose file itself) are
// written directly and deliberately not tracked.
type changeTracker struct {
	target  target.Target
	changed bool
}

func newChangeTracker(t target.Target) *changeTracker {
	return &changeTracker{target: t}
}

// write stores data at path, recording whether it displaced different
// bytes. A missing or unreadable file does not count as a change: there
// is no running process holding the old content, and Deploy is about to
// create one from the new.
func (c *changeTracker) write(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	if !c.changed {
		// ReadFile's contract for a missing file differs by target
		// implementation, so treat "no bytes" as absent either way.
		if existing, err := c.target.ReadFile(ctx, path); err == nil && len(existing) > 0 {
			c.changed = !bytes.Equal(existing, data)
		}
	}
	return c.target.WriteFile(ctx, path, data, perm)
}

// remove deletes path, recording a change when the file existed. A removed
// systemd drop-in must cause a restart just like a rewritten one, otherwise
// the running process keeps the old environment until its next restart.
func (c *changeTracker) remove(ctx context.Context, path string) error {
	if _, err := c.target.ReadFile(ctx, path); err == nil {
		c.changed = true
	}
	_, err := c.target.Exec(ctx, "rm", "-f", path)
	return err
}

// LogOpts configures log retrieval.
type LogOpts struct {
	Tail   int
	Follow bool
}

// NodeStatus represents the current status of a deployed node.
type NodeStatus struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // running, stopped, error, unknown
	Uptime  string `json:"uptime,omitempty"`
	Version string `json:"version,omitempty"`
}

// Runtime abstracts the deployment runtime (Docker or Jar+systemd).
type Runtime interface {
	// Deploy deploys the node to the target.
	Deploy(ctx context.Context, opts DeployOpts) error

	// Start starts a previously stopped node.
	Start(ctx context.Context, name string) error

	// Stop stops a running node.
	Stop(ctx context.Context, name string) error

	// Remove removes a deployed node. If purge is true, also removes data.
	Remove(ctx context.Context, name string, purge bool) error

	// Status returns the current node status.
	Status(ctx context.Context, name string) (*NodeStatus, error)

	// Logs returns a reader for node logs.
	Logs(ctx context.Context, name string, opts LogOpts) (io.ReadCloser, error)
}

// UpgradeOpts carries the artifact coordinates for a version switch.
type UpgradeOpts struct {
	Version   string
	JarURL    string // Jar runtime only: URL of the target-version JAR
	JarSHA256 string // Jar runtime only: optional integrity pin
}

// ArtifactUpgrader is an optional runtime capability. Callers must prepare
// before stopping the node, then activate and rollback the prepared artifact
// as one transaction. Runtimes without it cannot safely be upgraded.
type ArtifactUpgrader interface {
	PrepareArtifact(ctx context.Context, name string, opts UpgradeOpts) (ArtifactTransaction, error)
}

// ArtifactTransaction changes only the artifact. The caller owns stopping and
// starting the service, and must Rollback when activation or startup fails.
type ArtifactTransaction interface {
	Activate(context.Context) error
	Start(context.Context) error
	Rollback(context.Context) error
	Cleanup(context.Context) error
}

// DeployOpts contains everything needed for a deployment.
type DeployOpts struct {
	Name           string
	ConfigData     []byte
	ComposeData    []byte // Docker runtime only
	SystemdData    []byte // Jar runtime only
	JarPath        string // Jar runtime only
	JarURL         string // Jar runtime only
	JarSHA256      string // Jar runtime only
	ArtifactSHA256 string // previously recorded Jar artifact digest
	EnvVars        map[string]string
}
