package runtime

import (
	"context"
	"os"
	"testing"
)

// fakeTarget is a minimal in-memory target for unit testing.
type fakeTarget struct {
	files map[string][]byte
	perms map[string]os.FileMode
	cmds  [][]string
}

func newFakeTarget() *fakeTarget {
	return &fakeTarget{files: make(map[string][]byte), perms: make(map[string]os.FileMode)}
}

func (f *fakeTarget) Exec(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	f.cmds = append(f.cmds, append([]string{cmd}, args...))
	return []byte{}, nil
}

func (f *fakeTarget) Upload(ctx context.Context, localPath, remotePath string) error   { return nil }
func (f *fakeTarget) Download(ctx context.Context, remotePath, localPath string) error { return nil }
func (f *fakeTarget) ReadFile(ctx context.Context, path string) ([]byte, error) {
	data, ok := f.files[path]
	if !ok {
		// Faithful to the real targets: reading a file that is not there
		// is an error, not an empty read. Deploy distinguishes the two.
		return nil, os.ErrNotExist
	}
	return data, nil
}
func (f *fakeTarget) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	f.files[path] = data
	f.perms[path] = perm
	return nil
}
func (f *fakeTarget) DiskFree(ctx context.Context, path string) (uint64, error)       { return 0, nil }
func (f *fakeTarget) MemTotal(ctx context.Context) (uint64, error)                    { return 0, nil }
func (f *fakeTarget) PutFile(ctx context.Context, localPath, remotePath string) error { return nil }
func (f *fakeTarget) Sha256IfExists(ctx context.Context, path string) (string, error) {
	if _, ok := f.files[path]; ok {
		return "deadbeef", nil
	}
	return "", nil
}
func (f *fakeTarget) String() string                                      { return "fake" }
func (f *fakeTarget) CommandExists(ctx context.Context, name string) bool { return true }

func TestMonitoringRuntime_Deploy(t *testing.T) {
	ft := newFakeTarget()
	rt := NewMonitoringRuntime(ft, "/tmp/monitoring")

	opts := MonitoringDeployOpts{
		Name:              "test",
		ComposeData:       []byte("services:\n  prometheus:\n"),
		PrometheusConfig:  []byte("scrape_configs:\n"),
		GrafanaDatasource: []byte("datasources:\n"),
		GrafanaProvider:   []byte("providers:\n"),
		Dashboards: map[string][]byte{
			"test.json": []byte(`{"title":"Test"}`),
		},
	}

	if err := rt.Deploy(context.Background(), opts); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	// Verify files were written
	expectFiles := []string{
		"/tmp/monitoring/test-monitoring/docker-compose.yaml",
		"/tmp/monitoring/test-monitoring/conf/prometheus.yml",
		"/tmp/monitoring/test-monitoring/grafana/provisioning/datasources/prometheus.yml",
		"/tmp/monitoring/test-monitoring/grafana/provisioning/dashboards/dashboards.yml",
		"/tmp/monitoring/test-monitoring/grafana/dashboards/test.json",
	}
	for _, path := range expectFiles {
		if _, ok := ft.files[path]; !ok {
			t.Errorf("missing file: %s", path)
		}
	}

	// Verify docker compose up was called
	foundUp := false
	for _, cmd := range ft.cmds {
		if len(cmd) > 0 && cmd[0] == "docker" {
			for _, arg := range cmd {
				if arg == "up" {
					foundUp = true
					break
				}
			}
		}
	}
	if !foundUp {
		t.Error("docker compose up not called")
	}
}

func TestMonitoringRuntime_Remove(t *testing.T) {
	ft := newFakeTarget()
	rt := NewMonitoringRuntime(ft, "/tmp/monitoring")

	// Simulate a previously deployed stack so the compose file exists.
	ft.files["/tmp/monitoring/test-monitoring/docker-compose.yaml"] = []byte("services:\n")

	if err := rt.Remove(context.Background(), "test", true); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	foundDown := false
	for _, cmd := range ft.cmds {
		if len(cmd) > 0 && cmd[0] == "docker" {
			for _, arg := range cmd {
				if arg == "down" {
					foundDown = true
					break
				}
			}
		}
	}
	if !foundDown {
		t.Error("docker compose down not called")
	}
}

// Remove is a no-op when no monitoring stack was ever deployed (no compose
// file), so `network destroy` doesn't emit a spurious warning.
func TestMonitoringRuntime_Remove_SkipsWhenAbsent(t *testing.T) {
	ft := newFakeTarget()
	rt := NewMonitoringRuntime(ft, "/tmp/monitoring")

	if err := rt.Remove(context.Background(), "test", true); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	for _, cmd := range ft.cmds {
		if len(cmd) > 0 && cmd[0] == "docker" {
			t.Errorf("docker should not be invoked when stack absent, got %v", cmd)
		}
	}
}
