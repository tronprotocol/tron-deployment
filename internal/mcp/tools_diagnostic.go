package mcp

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/diagnosis"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

var diagnoseCheckers = diagnosis.AllCheckers

// registerDiagnosticTools wires up read-only triage tools. Pure-data
// when possible; light HTTP probes where the data only exists at
// runtime.

func registerDiagnosticTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "doctor",
		Title:       "Self-check the trond install",
		Description: "Verify trond's local install: state dir permissions, lock file age, audit log perms, docker CLI presence, optional GitHub release-update probe. Equivalent to `trond doctor -o json`. Suggested first command to paste into a bug report.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, doctorTool)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "version",
		Title:       "trond version + build info",
		Description: "Trond version, commit, build time, Go runtime, platform. Equivalent to `trond version -o json`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, versionTool)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "health",
		Title:       "Quick HTTP probe of a node",
		Description: "Single-shot HTTP probe of a node's getnowblock API. Returns block_height + latency. Lighter than `diagnose`. Only works for running nodes. Equivalent to `trond health <name> -o json`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, healthTool)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "diagnose",
		Title:       "Structured health checks",
		Description: "Run trond's diagnostic suite against a node: sync_progress, peer_count, disk_space, port_listening, etc. Each check carries its own status (pass/warning/fail) and suggestions[]. Process failed checks first. Equivalent to `trond diagnose <name> -o json`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, diagnoseTool)
}

// versionInfo is set by cmd/mcp.go before Run starts the server, so
// the version tool returns the same string as `trond version`.
var versionInfo = struct {
	Version, Commit, BuildTime string
}{}

// SetVersionInfo lets the cobra layer hand its build-stamped values
// into this package without forcing the package to import cmd/.
func SetVersionInfo(version, commit, buildTime string) {
	versionInfo.Version = version
	versionInfo.Commit = commit
	versionInfo.BuildTime = buildTime
}

func versionTool(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
	return jsonResult(map[string]any{
		"version":    versionInfo.Version,
		"commit":     versionInfo.Commit,
		"build_time": versionInfo.BuildTime,
		"go_version": runtime.Version(),
		"platform":   runtime.GOOS + "/" + runtime.GOARCH,
	})
}

func doctorTool(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
	// Mirror cmd/doctor.go's check set, but as a pure function — no
	// dependence on cobra flag parsing. We skip --check-update here;
	// agents that need it can call the version tool with a future
	// `check_update` parameter or invoke the CLI directly.
	checks := []map[string]any{
		{"name": "trond version", "status": "pass",
			"message": versionInfo.Version + " (commit " + versionInfo.Commit + ")"},
	}
	checks = append(checks, doctorStateDir())
	checks = append(checks, doctorStateFile())
	checks = append(checks, doctorDockerCLI(ctx))

	overall := "pass"
	for _, c := range checks {
		st, _ := c["status"].(string)
		if st == "fail" {
			overall = "fail"
		} else if st == "warn" && overall != "fail" {
			overall = "warn"
		}
	}
	return jsonResult(map[string]any{"overall": overall, "checks": checks})
}

func doctorStateDir() map[string]any {
	dir := paths.BaseDir()
	return map[string]any{
		"name":    "state dir",
		"status":  "pass",
		"message": dir,
	}
}

func doctorStateFile() map[string]any {
	store, err := state.NewStore(paths.State())
	if err != nil {
		return map[string]any{
			"name": "state.json", "status": "warn",
			"message": "could not open: " + err.Error(),
		}
	}
	st, err := store.Load()
	if err != nil {
		return map[string]any{"name": "state.json", "status": "fail", "message": "parse: " + err.Error()}
	}
	return map[string]any{
		"name":    "state.json",
		"status":  "pass",
		"message": fmt.Sprintf("%d managed node(s)", len(st.Nodes)),
	}
}

func doctorDockerCLI(ctx context.Context) map[string]any {
	if _, err := exec.LookPath("docker"); err != nil {
		return map[string]any{
			"name": "docker CLI", "status": "warn",
			"message": "not on PATH (only matters for runtime: docker)",
		}
	}
	c := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Client.Version}}")
	out, err := c.Output()
	if err != nil {
		return map[string]any{
			"name": "docker CLI", "status": "warn",
			"message": "found but `docker version` failed: " + strings.TrimSpace(err.Error()),
		}
	}
	return map[string]any{
		"name":    "docker CLI",
		"status":  "pass",
		"message": "v" + strings.TrimSpace(string(out)),
	}
}

func healthTool(ctx context.Context, _ *mcp.CallToolRequest, args nodeArg) (*mcp.CallToolResult, any, error) {
	store, st, err := state.Load(paths.State())
	if err != nil {
		return errResult(err)
	}
	node := store.GetNode(st, args.Name)
	if node == nil {
		return errResult(notFound("health", args.Name))
	}
	port := apply.PortOrDefault(node.HTTPPort, 8090)
	// The probe URL stays on loopback: target.HTTPClient dials through the
	// SSH tunnel, which lands on the remote host's loopback.
	url := apply.ProbeURL(port, "/wallet/getnowblock")
	endpoint := apply.HTTPURL(target.EndpointHost(node.Target.Type, node.Target.Host), port) + "/wallet/getnowblock"

	// Probe through the node's target (SSH-tunnelled for remote nodes),
	// not via http.DefaultClient against this host's loopback — that
	// bypassed SSH entirely and always failed for remote rigs.
	tgt, err := target.FromManagedNode(node)
	if err != nil {
		return errResult(err)
	}
	if c, ok := any(tgt).(interface{ Close() error }); ok {
		defer c.Close()
	}

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return errResult(err)
	}
	start := time.Now()
	resp, err := target.HTTPClient(tgt, 5*time.Second).Do(req)
	if err != nil {
		return jsonResult(map[string]any{
			"name": args.Name, "healthy": false,
			"endpoint": endpoint, "error": err.Error(),
		})
	}
	defer resp.Body.Close()
	healthy := resp.StatusCode == http.StatusOK
	return jsonResult(map[string]any{
		"name":       args.Name,
		"healthy":    healthy,
		"endpoint":   endpoint,
		"latency_ms": time.Since(start).Milliseconds(),
		"status":     resp.StatusCode,
	})
}

func diagnoseTool(ctx context.Context, _ *mcp.CallToolRequest, args nodeArg) (*mcp.CallToolResult, any, error) {
	// Runs the same full check suite (sync_progress, peer_count,
	// disk_space, port_listening, memory_usage, version) that
	// `trond diagnose <name>` does, by reaching into
	// internal/diagnosis directly. The earlier MCP version returned
	// only a state-only subset; that gap is now closed.
	store, st, err := state.Load(paths.State())
	if err != nil {
		return errResult(err)
	}
	node := store.GetNode(st, args.Name)
	if node == nil {
		return errResult(notFound("diagnose", args.Name))
	}

	tgt, err := target.FromManagedNode(node)
	if err != nil {
		return errResult(err)
	}
	if c, ok := any(tgt).(interface{ Close() error }); ok {
		defer c.Close()
	}

	opts := diagnosis.CheckOpts{
		NodeName: node.Name,
		Network:  node.Network,
		Runtime:  node.Runtime,
		HTTPPort: node.HTTPPort,
		GRPCPort: node.GRPCPort,
	}

	checkers := diagnoseCheckers()
	results := make([]diagnosis.CheckResult, 0, len(checkers))
	for _, c := range checkers {
		results = append(results, c.Run(ctx, tgt, opts))
	}

	return jsonResult(map[string]any{
		"name":    node.Name,
		"overall": diagnosis.OverallStatus(results),
		"checks":  results,
	})
}
