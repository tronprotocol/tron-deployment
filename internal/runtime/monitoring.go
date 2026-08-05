package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tronprotocol/tron-deployment/internal/target"
)

// MonitoringRuntime manages the Prometheus + Grafana monitoring stack.
type MonitoringRuntime struct {
	target  target.Target
	workDir string
}

// NewMonitoringRuntime creates a MonitoringRuntime.
func NewMonitoringRuntime(t target.Target, workDir string) *MonitoringRuntime {
	return &MonitoringRuntime{
		target:  t,
		workDir: workDir,
	}
}

// MonitoringDeployOpts carries everything needed to deploy a monitoring stack.
type MonitoringDeployOpts struct {
	Name              string
	ComposeData       []byte
	PrometheusConfig  []byte
	GrafanaDatasource []byte
	GrafanaProvider   []byte
	Dashboards        map[string][]byte // filename → JSON content
}

// Deploy creates the monitoring directory, writes all configs and dashboards,
// then starts the stack via docker compose.
func (r *MonitoringRuntime) Deploy(ctx context.Context, opts MonitoringDeployOpts) error {
	dir := filepath.Join(r.workDir, opts.Name+"-monitoring")

	// Create base directory
	if _, err := r.target.Exec(ctx, "mkdir", "-p", dir); err != nil {
		return fmt.Errorf("create monitoring dir: %w", err)
	}

	// Prometheus and Grafana read these from bind mounts and parse them at
	// process start, so compose's recreate decision cannot see a change in
	// any of them. Tracked; the compose file itself is not, because
	// compose diffs that on its own.
	tracker := newChangeTracker(r.target)

	// Write compose file
	composePath := filepath.Join(dir, "docker-compose.yaml")
	if err := r.target.WriteFile(ctx, composePath, opts.ComposeData, 0644); err != nil {
		return fmt.Errorf("write monitoring compose: %w", err)
	}

	// Write prometheus config
	confDir := filepath.Join(dir, "conf")
	if _, err := r.target.Exec(ctx, "mkdir", "-p", confDir); err != nil {
		return fmt.Errorf("create monitoring conf dir: %w", err)
	}
	promPath := filepath.Join(confDir, "prometheus.yml")
	if err := tracker.write(ctx, promPath, opts.PrometheusConfig, 0644); err != nil {
		return fmt.Errorf("write prometheus config: %w", err)
	}

	// Write grafana datasource provisioning
	dsDir := filepath.Join(dir, "grafana", "provisioning", "datasources")
	if _, err := r.target.Exec(ctx, "mkdir", "-p", dsDir); err != nil {
		return fmt.Errorf("create grafana ds dir: %w", err)
	}
	dsPath := filepath.Join(dsDir, "prometheus.yml")
	if err := tracker.write(ctx, dsPath, opts.GrafanaDatasource, 0644); err != nil {
		return fmt.Errorf("write grafana datasource: %w", err)
	}

	// Write grafana dashboard provider
	provDir := filepath.Join(dir, "grafana", "provisioning", "dashboards")
	if _, err := r.target.Exec(ctx, "mkdir", "-p", provDir); err != nil {
		return fmt.Errorf("create grafana provider dir: %w", err)
	}
	provPath := filepath.Join(provDir, "dashboards.yml")
	if err := tracker.write(ctx, provPath, opts.GrafanaProvider, 0644); err != nil {
		return fmt.Errorf("write grafana provider: %w", err)
	}

	// Write dashboard JSONs
	dashDir := filepath.Join(dir, "grafana", "dashboards")
	if _, err := r.target.Exec(ctx, "mkdir", "-p", dashDir); err != nil {
		return fmt.Errorf("create dashboard dir: %w", err)
	}
	for fname, data := range opts.Dashboards {
		dashPath := filepath.Join(dashDir, fname)
		if err := tracker.write(ctx, dashPath, data, 0644); err != nil {
			return fmt.Errorf("write dashboard %s: %w", fname, err)
		}
	}

	// Docker compose up
	args := []string{"compose", "-f", composePath, "-p", opts.Name + "-monitoring", "up", "-d"}
	if tracker.changed {
		// A changed scrape config or dashboard otherwise sits on disk
		// while Prometheus keeps serving what it parsed at start.
		args = append(args, "--force-recreate")
	}
	if _, err := r.target.Exec(ctx, "docker", args...); err != nil {
		return fmt.Errorf("monitoring compose up: %w", err)
	}

	return nil
}

// Remove stops and removes the monitoring stack, optionally purging data.
func (r *MonitoringRuntime) Remove(ctx context.Context, name string, purge bool) error {
	composePath := filepath.Join(r.workDir, name+"-monitoring", "docker-compose.yaml")
	// No compose file means the monitoring stack was never deployed for this
	// network; nothing to tear down. Skip silently so destroy doesn't emit a
	// spurious warning when monitoring was disabled.
	if sum, err := r.target.Sha256IfExists(ctx, composePath); err == nil && sum == "" {
		return nil
	}
	args := []string{"compose", "-f", composePath, "-p", name + "-monitoring", "down"}
	if purge {
		args = append(args, "-v")
	}
	if _, err := r.target.Exec(ctx, "docker", args...); err != nil {
		return fmt.Errorf("monitoring compose down: %w", err)
	}
	if purge {
		if _, err := r.target.Exec(ctx, "rm", "-rf", filepath.Join(r.workDir, name+"-monitoring")); err != nil {
			return fmt.Errorf("purge monitoring dir: %w", err)
		}
	}
	return nil
}

// Status returns the current status of the monitoring stack.
func (r *MonitoringRuntime) Status(ctx context.Context, name string) (*NodeStatus, error) {
	composePath := filepath.Join(r.workDir, name+"-monitoring", "docker-compose.yaml")
	out, err := r.target.Exec(ctx, "docker", "compose", "-f", composePath, "-p", name+"-monitoring", "ps", "--format", "json")
	if err != nil {
		return &NodeStatus{Name: name + "-monitoring", Status: "unknown"}, nil
	}
	// Parse docker compose ps JSON output. The output is a JSON array of container objects.
	// We check if any container is not "running" to determine the overall status.
	outStr := string(out)
	if strings.Contains(outStr, `"State":"running"`) {
		return &NodeStatus{Name: name + "-monitoring", Status: "running"}, nil
	}
	if strings.Contains(outStr, `"State":"exited"`) {
		return &NodeStatus{Name: name + "-monitoring", Status: "stopped"}, nil
	}
	return &NodeStatus{Name: name + "-monitoring", Status: "unknown"}, nil
}
