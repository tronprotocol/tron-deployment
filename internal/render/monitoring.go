package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

// MonitoringTarget describes one java-tron node to scrape.
type MonitoringTarget struct {
	Name    string
	Address string // host:port for prometheus static_configs
	Labels  map[string]string
}

// monitoringComposeTmpl is the Go template for the monitoring docker-compose.
// Based on tron-docker's metric_monitor/docker-compose/docker-compose-quick-start.yml
// with dynamic names, ports, and optional network attachment.
const monitoringComposeTmpl = `services:
  prometheus:
    image: prom/prometheus:v3.10.0
    container_name: {{.Name}}-prometheus
    deploy:
      resources:
        limits:
          memory: 1g
{{- if .PrometheusPort }}
    ports:
      - "{{.PrometheusPort}}:9090"
{{- end }}
    volumes:
      - ./conf:/etc/prometheus
      - ./prometheus_data:/prometheus
    command:
      - "--config.file=/etc/prometheus/prometheus.yml"
      - "--storage.tsdb.path=/prometheus"
      - "--storage.tsdb.retention.time={{.Retention}}"
      - "--web.enable-lifecycle"
{{- if .NetworkName }}
    networks:
      - {{.NetworkName}}
{{- end }}

  grafana:
    image: grafana/grafana-oss:12.4.1
    container_name: {{.Name}}-grafana
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    deploy:
      resources:
        limits:
          memory: 2g
    ports:
      - "{{.GrafanaPortMapping}}"
{{- if .GrafanaAdminPasswordRef }}
    environment:
      - GF_SECURITY_ADMIN_PASSWORD={{.GrafanaAdminPasswordRef}}
{{- end }}
    volumes:
      - ./grafana_data:/var/lib/grafana
      - ./grafana/provisioning:/etc/grafana/provisioning
      - ./grafana/dashboards:/var/lib/grafana/dashboards
{{- if .NetworkName }}
    networks:
      - {{.NetworkName}}
{{- end }}

{{- if .NetworkName }}
networks:
  {{.NetworkName}}:
    external: true
{{- end }}
`

// composeTmpl is the parsed template, initialized once.
var composeTmpl = template.Must(template.New("monitoring").Parse(monitoringComposeTmpl))

// monitoringComposeData carries the fields needed by the template.
type monitoringComposeData struct {
	Name           string
	PrometheusPort int
	// GrafanaPortMapping is the full compose "ports" entry for Grafana,
	// including the host bind address — see grafanaPortMapping.
	GrafanaPortMapping string
	// GrafanaAdminPasswordRef is the compose interpolation reference that
	// supplies GF_SECURITY_ADMIN_PASSWORD, or "" when the operator did
	// not configure one.
	GrafanaAdminPasswordRef string
	Retention               string
	NetworkName             string
}

// RenderMonitoringCompose generates a docker-compose.yaml for the
// Prometheus + Grafana monitoring stack.
func RenderMonitoringCompose(name string, i *intent.Intent, targets []MonitoringTarget, networkName string) string {
	var buf bytes.Buffer
	g := i.Monitoring.Grafana
	data := monitoringComposeData{
		Name:                    name,
		PrometheusPort:          i.Monitoring.Prometheus.Port,
		GrafanaPortMapping:      grafanaPortMapping(g),
		GrafanaAdminPasswordRef: grafanaAdminPasswordRef(g),
		Retention:               i.Monitoring.Prometheus.Retention,
		NetworkName:             networkName,
	}
	_ = composeTmpl.Execute(&buf, data)
	return buf.String()
}

// grafanaPortMapping builds Grafana's compose "ports" entry.
//
// A bare "<port>:3000" publishes on 0.0.0.0, which on any host with a
// permissive firewall exposes the dashboard — and Grafana's datasource
// proxy — to the network with the grafana-oss image's default admin
// login. Bind to loopback instead; reaching it from elsewhere is then an
// SSH tunnel (or a reverse proxy the operator controls) away.
//
// monitoring.grafana.expose opts back into the host-wide bind, and is
// only honoured together with an admin password: intent validation
// rejects that combination up front, and this stays fail-closed for any
// caller that renders an unvalidated intent.
func grafanaPortMapping(g intent.GrafConfig) string {
	if g.Expose && g.AdminPasswordEnv != "" {
		return fmt.Sprintf("%d:3000", g.Port)
	}
	return fmt.Sprintf("127.0.0.1:%d:3000", g.Port)
}

// grafanaAdminPasswordRef returns the compose interpolation that feeds
// GF_SECURITY_ADMIN_PASSWORD from the operator's environment, keeping the
// secret out of the rendered file (the same shape composeEnvLines uses
// for the witness keystore password).
//
// The ":?" form makes `docker compose up` fail loudly when the variable
// is unset or empty rather than silently starting Grafana with an empty
// or default admin password. The name is constrained to env-name
// characters by intent validation.
func grafanaAdminPasswordRef(g intent.GrafConfig) string {
	if g.AdminPasswordEnv == "" {
		return ""
	}
	return fmt.Sprintf("${%s:?set %s to the Grafana admin password (monitoring.grafana.admin_password_env)}",
		g.AdminPasswordEnv, g.AdminPasswordEnv)
}

// RenderPrometheusConfig generates prometheus.yml content.
//
// The retention parameter is currently unused: Prometheus TSDB retention is
// configured via the container's command-line args in docker-compose.yaml
// ("--storage.tsdb.retention.time=..."), not in prometheus.yml.  The
// parameter is kept in the signature for backward compat with existing
// callers and tests; pass an empty string when regeneration is triggered by
// network-add (where compose is not rewritten).
func RenderPrometheusConfig(targets []MonitoringTarget, retention string) string {
	var sb strings.Builder
	sb.WriteString("global:\n")
	sb.WriteString("  scrape_interval: 6s\n")
	sb.WriteString("  scrape_timeout: 4s\n")
	sb.WriteString("  evaluation_interval: 6s\n")
	sb.WriteString("\n")
	sb.WriteString("scrape_configs:\n")
	sb.WriteString("  - job_name: java-tron\n")
	sb.WriteString("    honor_timestamps: true\n")
	sb.WriteString("    scrape_interval: 3s\n")
	sb.WriteString("    scrape_timeout: 2s\n")
	sb.WriteString("    metrics_path: /metrics\n")
	sb.WriteString("    scheme: http\n")
	sb.WriteString("    static_configs:\n")
	for _, t := range targets {
		sb.WriteString("      - targets:\n")
		sb.WriteString(fmt.Sprintf("          - %s\n", t.Address))
		sb.WriteString("        labels:\n")
		for k, v := range t.Labels {
			sb.WriteString(fmt.Sprintf("          %s: %s\n", k, v))
		}
	}
	return sb.String()
}

// RenderGrafanaProvisioning returns the datasource and dashboard provider YAMLs.
func RenderGrafanaProvisioning(prometheusURL string) (datasourceYAML, providerYAML string) {
	ds := fmt.Sprintf(`apiVersion: 1
datasources:
  - name: prometheus
    type: prometheus
    access: proxy
    url: %s
    isDefault: true
    editable: false
`, prometheusURL)

	prov := `apiVersion: 1
providers:
  - name: java-tron
    orgId: 1
    folder: ''
    type: file
    disableDeletion: false
    editable: true
    options:
      path: /var/lib/grafana/dashboards
`
	return ds, prov
}

// DashboardNames returns the list of embedded dashboard filenames.
func DashboardNames() []string {
	return []string{
		"java-tron-server.json",
		"java-tron-api.json",
		"java-tron-api-statistic.json",
		"java-tron-mechanism.json",
		"node-exporter-full.json",
	}
}

// NormalizeDashboard replaces hardcoded datasource UIDs in the raw Grafana
// dashboard JSON with the provisioned datasource name "prometheus".
func NormalizeDashboard(data []byte) []byte {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		// If unmarshal fails, return raw data unchanged.
		return data
	}

	// Walk all panels and replace datasource uid references.
	walkJSON(doc, func(m map[string]any) {
		if ds, ok := m["datasource"].(map[string]any); ok {
			if _, hasUID := ds["uid"]; hasUID {
				ds["uid"] = "prometheus"
			}
		}
		// template.list may also hold datasource references.
		if tlist, ok := m["templating"].(map[string]any); ok {
			if list, ok := tlist["list"].([]any); ok {
				for _, item := range list {
					if im, ok := item.(map[string]any); ok {
						switch im["type"] {
						case "datasource":
							im["current"] = map[string]any{
								"text":     "prometheus",
								"value":    "prometheus",
								"selected": false,
							}
						case "query":
							// Clear hardcoded defaults so Grafana picks the
							// first available label value at render time.
							im["current"] = map[string]any{
								"text":     "",
								"value":    "",
								"selected": false,
							}
						}
					}
				}
			}
		}
	})

	out, err := json.Marshal(doc)
	if err != nil {
		return data
	}
	return out
}

// walkJSON recursively visits every map[string]any in the JSON document.
func walkJSON(v any, fn func(map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		fn(x)
		for _, child := range x {
			walkJSON(child, fn)
		}
	case []any:
		for _, child := range x {
			walkJSON(child, fn)
		}
	}
}
