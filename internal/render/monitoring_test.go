package render

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

func TestRenderMonitoringCompose_Basic(t *testing.T) {
	i := &intent.Intent{
		Name: "test",
		Monitoring: &intent.Monitoring{
			Enabled:    intent.BoolPtr(true),
			Prometheus: intent.PromConfig{Port: 9090, Retention: "7d"},
			Grafana:    intent.GrafConfig{Port: 3000},
		},
	}
	targets := []MonitoringTarget{{Name: "test", Address: "test:9527"}}
	out := RenderMonitoringCompose("test", i, targets, "test_default")

	if !strings.Contains(out, "prometheus:") {
		t.Error("missing prometheus service")
	}
	if !strings.Contains(out, "grafana:") {
		t.Error("missing grafana service")
	}
	if !strings.Contains(out, "9090:9090") {
		t.Error("missing prometheus port mapping")
	}
	// Grafana is bound to loopback unless the operator opts into a
	// host-wide bind (which additionally requires an admin password).
	if !strings.Contains(out, `"127.0.0.1:3000:3000"`) {
		t.Errorf("missing loopback-bound grafana port mapping, got:\n%s", out)
	}
	if !strings.Contains(out, "test_default:") {
		t.Error("missing external network reference")
	}
	if !strings.Contains(out, "test-prometheus") {
		t.Error("missing prometheus container name")
	}
	if !strings.Contains(out, "v3.10.0") {
		t.Error("missing prometheus image version")
	}
}

func TestRenderMonitoringCompose_NoNetwork(t *testing.T) {
	i := &intent.Intent{
		Name: "test",
		Monitoring: &intent.Monitoring{
			Enabled:    intent.BoolPtr(true),
			Prometheus: intent.PromConfig{Port: 9090, Retention: "7d"},
			Grafana:    intent.GrafConfig{Port: 3000},
		},
	}
	out := RenderMonitoringCompose("test", i, nil, "")
	if strings.Contains(out, "networks:") {
		t.Error("should not contain networks when networkName is empty")
	}
}

// grafanaService parses the rendered compose and returns the grafana
// service block, so the assertions below check what docker compose would
// actually see rather than a substring of the template.
func grafanaService(t *testing.T, compose string) map[string]any {
	t.Helper()
	var doc struct {
		Services map[string]struct {
			Ports       []string `yaml:"ports"`
			Environment []string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(compose), &doc); err != nil {
		t.Fatalf("rendered compose is not valid YAML: %v\n%s", err, compose)
	}
	g, ok := doc.Services["grafana"]
	if !ok {
		t.Fatalf("no grafana service in rendered compose:\n%s", compose)
	}
	return map[string]any{"ports": g.Ports, "environment": g.Environment}
}

func monitoringIntent(g intent.GrafConfig) *intent.Intent {
	return &intent.Intent{
		Name: "test",
		Monitoring: &intent.Monitoring{
			Enabled:    intent.BoolPtr(true),
			Prometheus: intent.PromConfig{Retention: "7d"},
			Grafana:    g,
		},
	}
}

// The grafana-oss image ships a default admin login, so the rendered
// stack must not publish Grafana beyond the host's loopback interface
// unless the operator explicitly opts in with an admin password set.
func TestRenderMonitoringCompose_GrafanaBinding(t *testing.T) {
	cases := []struct {
		name     string
		grafana  intent.GrafConfig
		wantPort string
		wantEnv  bool
	}{
		{
			name:     "default is loopback only",
			grafana:  intent.GrafConfig{Port: 3000},
			wantPort: "127.0.0.1:3000:3000",
		},
		{
			name:     "custom port stays loopback",
			grafana:  intent.GrafConfig{Port: 13000},
			wantPort: "127.0.0.1:13000:3000",
		},
		{
			name:     "admin password alone does not widen the bind",
			grafana:  intent.GrafConfig{Port: 3000, AdminPasswordEnv: "GRAFANA_ADMIN_PASSWORD"},
			wantPort: "127.0.0.1:3000:3000",
			wantEnv:  true,
		},
		{
			name:     "expose with a password publishes on all interfaces",
			grafana:  intent.GrafConfig{Port: 3000, Expose: true, AdminPasswordEnv: "GRAFANA_ADMIN_PASSWORD"},
			wantPort: "3000:3000",
			wantEnv:  true,
		},
		{
			// Rejected by intent validation; the renderer stays
			// fail-closed for any caller that skips it.
			name:     "expose without a password falls back to loopback",
			grafana:  intent.GrafConfig{Port: 3000, Expose: true},
			wantPort: "127.0.0.1:3000:3000",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GRAFANA_ADMIN_PASSWORD", "s3cret")
			out := RenderMonitoringCompose("test", monitoringIntent(tc.grafana), nil, "")
			svc := grafanaService(t, out)

			ports := svc["ports"].([]string)
			if len(ports) != 1 || ports[0] != tc.wantPort {
				t.Errorf("grafana ports = %v, want [%s]", ports, tc.wantPort)
			}

			env := svc["environment"].([]string)
			var adminPassword string
			for _, e := range env {
				if v, ok := strings.CutPrefix(e, "GF_SECURITY_ADMIN_PASSWORD="); ok {
					adminPassword = v
				}
			}
			switch {
			case tc.wantEnv && adminPassword == "":
				t.Errorf("GF_SECURITY_ADMIN_PASSWORD missing from grafana environment %v", env)
			case tc.wantEnv && !strings.HasPrefix(adminPassword, "${GRAFANA_ADMIN_PASSWORD:?"):
				// The value must reach the container from the
				// operator's environment, and compose must refuse
				// to start the stack when it is unset or empty.
				t.Errorf("GF_SECURITY_ADMIN_PASSWORD = %q, want a required ${GRAFANA_ADMIN_PASSWORD:?...} reference", adminPassword)
			case !tc.wantEnv && adminPassword != "":
				t.Errorf("unexpected GF_SECURITY_ADMIN_PASSWORD = %q", adminPassword)
			}

			// The password itself never belongs in the rendered file.
			if strings.Contains(out, "s3cret") {
				t.Error("rendered compose inlined a secret value")
			}
		})
	}
}

func TestRenderPrometheusConfig(t *testing.T) {
	targets := []MonitoringTarget{
		{Name: "node1", Address: "node1:9527", Labels: map[string]string{"instance": "node1"}},
	}
	out := RenderPrometheusConfig(targets, "7d")
	if !strings.Contains(out, "job_name: java-tron") {
		t.Error("missing job name")
	}
	if !strings.Contains(out, "node1:9527") {
		t.Error("missing target address")
	}
	if !strings.Contains(out, "instance: node1") {
		t.Error("missing label")
	}
}

func TestRenderGrafanaProvisioning(t *testing.T) {
	ds, prov := RenderGrafanaProvisioning("http://prometheus:9090")
	if !strings.Contains(ds, "prometheus") {
		t.Error("datasource missing prometheus name")
	}
	if !strings.Contains(ds, "http://prometheus:9090") {
		t.Error("datasource missing prometheus url")
	}
	if !strings.Contains(prov, "java-tron") {
		t.Error("provider missing name")
	}
}

func TestLoadDashboard(t *testing.T) {
	for _, name := range DashboardNames() {
		data, err := LoadDashboard(name)
		if err != nil {
			t.Errorf("LoadDashboard(%q): %v", name, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("LoadDashboard(%q): empty data", name)
		}
	}
}

func TestNormalizeDashboard(t *testing.T) {
	input := []byte(`{"panels":[{"datasource":{"uid":"old-uid"}}]}`)
	out := NormalizeDashboard(input)
	if !strings.Contains(string(out), `"uid":"prometheus"`) {
		t.Errorf("expected uid replaced with prometheus, got: %s", string(out))
	}
}
