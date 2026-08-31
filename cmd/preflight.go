package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/render"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

var preflightIntentPath string

var preflightCmd = &cobra.Command{
	Use:   "preflight",
	Short: "Check target readiness before deploying",
	Long:  "Verify the target has required software, ports, disk space, and memory for deployment.",
	RunE:  runPreflight,
}

func init() {
	preflightCmd.Flags().StringVar(&preflightIntentPath, "intent", "", "Path to intent.yaml (required)")
	mustMarkRequired(preflightCmd, "intent")
	rootCmd.AddCommand(preflightCmd)
}

type checkResult struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // pass, fail, warning
	Message string `json:"message"`
}

func runPreflight(cmd *cobra.Command, args []string) error {
	outputFmt, _ := cmd.Flags().GetString("output")

	parsed, err := intent.Load(preflightIntentPath)
	if err != nil {
		return exitWithError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
	}

	tgt, err := resolveTarget(parsed)
	if err != nil {
		return exitWithError("TARGET_UNREACHABLE", output.ExitTargetUnreachable, err.Error(),
			"Check SSH connectivity or Docker availability")
	}
	if closer, ok := tgt.(interface{ Close() error }); ok {
		defer closer.Close()
	}

	var checks []checkResult
	allPassed := true

	// Runtime check
	runtimeType := parsed.Target.Runtime
	if runtimeType == "" {
		runtimeType = "docker"
	}

	switch runtimeType {
	case "docker":
		checks = append(checks, checkDocker(cmd, tgt))
	case "jar":
		checks = append(checks, checkJDK(cmd, tgt))
	}

	// Disk space check
	checks = append(checks, checkDiskSpace(cmd, tgt, parsed))

	// Memory check
	checks = append(checks, checkMemory(cmd, tgt, parsed))
	checks = append(checks, checkMemoryRecommended(parsed)...)

	// Port check
	for _, node := range parsed.Nodes {
		checks = append(checks, checkPorts(cmd, tgt, &node)...)
	}

	// Build-side checks (LOCAL machine, not the target). Only runs
	// when at least one node has a `build:` block; build-less intents
	// pay no cost. See preflight_build.go for the policy details.
	checks = append(checks, preflightBuildChecks(cmd.Context(), parsed, preflightIntentPath)...)

	for _, c := range checks {
		if c.Status == "fail" {
			allPassed = false
		}
	}

	// target / overall are part of the published preflight schema
	// (schemas/output/preflight.schema.json) — agents key off both.
	targetType := parsed.Target.Type
	if targetType == "" {
		targetType = "local"
	}
	result := map[string]any{
		"target":  targetType,
		"checks":  checks,
		"overall": "pass",
	}
	if targetType == "ssh" {
		result["host"] = parsed.Target.Host
	}
	if !allPassed {
		result["overall"] = "fail"
	}

	if outputFmt == "json" {
		output.WriteJSON(os.Stdout, result)
	} else {
		for _, c := range checks {
			icon := "✓"
			if c.Status == "fail" {
				icon = "✗"
			} else if c.Status == "warning" {
				icon = "⚠"
			}
			fmt.Printf("%s %-20s %s\n", icon, c.Name, c.Message)
		}
	}

	if !allPassed {
		return output.NewError("PREFLIGHT_FAILURE", output.ExitPreflightFailure,
			"One or more preflight checks failed")
	}
	return nil
}

func checkDocker(cmd *cobra.Command, tgt target.Target) checkResult {
	out, err := tgt.Exec(cmd.Context(), "docker", "compose", "version")
	if err != nil {
		return checkResult{Name: "docker", Status: "fail", Message: "Docker Compose V2 not found"}
	}
	version := strings.TrimSpace(string(out))
	// Docker Compose V2+ (v2.x, v5.x, etc.) — reject only V1 or missing
	if strings.Contains(version, "docker-compose version 1.") {
		return checkResult{Name: "docker", Status: "fail", Message: "Docker Compose V2+ required, found V1: " + version}
	}
	return checkResult{Name: "docker", Status: "pass", Message: version}
}

func checkJDK(cmd *cobra.Command, tgt target.Target) checkResult {
	out, err := tgt.Exec(cmd.Context(), "java", "-version")
	if err != nil {
		return checkResult{Name: "jdk", Status: "fail", Message: "Java not found"}
	}
	version := strings.TrimSpace(string(out))
	firstLine := strings.Split(version, "\n")[0]
	return checkResult{Name: "jdk", Status: "pass", Message: firstLine}
}

func checkDiskSpace(cmd *cobra.Command, tgt target.Target, parsed *intent.Intent) checkResult {
	free, err := tgt.DiskFree(cmd.Context(), "/")
	if err != nil {
		return checkResult{Name: "disk", Status: "warning", Message: "Could not check disk space"}
	}
	freeGB := free / (1024 * 1024 * 1024)
	minGB := uint64(100) // Mainnet needs ~100GB+
	if parsed.Network == "nile" || parsed.Network == "private" {
		minGB = 10
	}
	if freeGB < minGB {
		return checkResult{Name: "disk", Status: "fail",
			Message: fmt.Sprintf("%dGB free, %dGB recommended", freeGB, minGB)}
	}
	return checkResult{Name: "disk", Status: "pass",
		Message: fmt.Sprintf("%dGB free", freeGB)}
}

func checkMemory(cmd *cobra.Command, tgt target.Target, parsed *intent.Intent) checkResult {
	mem, err := tgt.MemTotal(cmd.Context())
	if err != nil {
		return checkResult{Name: "memory", Status: "warning", Message: "Could not check memory"}
	}
	memGB := mem / (1024 * 1024 * 1024)
	// Take the largest memory requirement across all nodes in the intent.
	// Previously this loop was a no-op (`break` after first non-empty),
	// so the threshold stayed at the hardcoded 16 GB regardless of intent.
	minGB := uint64(0)
	for _, node := range parsed.Nodes {
		g, err := render.ParseMemoryGB(node.Resources.Memory)
		if err != nil {
			return checkResult{Name: "memory", Status: "fail",
				Message: fmt.Sprintf("node memory %q is invalid: %v", node.Resources.Memory, err)}
		}
		if g := uint64(g); g > minGB {
			minGB = g
		}
	}
	if minGB == 0 {
		minGB = 16 // safe default when no node requested any memory
	}
	if memGB < minGB {
		return checkResult{Name: "memory", Status: "fail",
			Message: fmt.Sprintf("%dGB total, %dGB recommended", memGB, minGB)}
	}
	return checkResult{Name: "memory", Status: "pass",
		Message: fmt.Sprintf("%dGB total", memGB)}
}

// checkMemoryRecommended warns about container memory below java-tron's
// official startup recommendation. This is an intent-side check: it does not
// inspect the target's available memory or alter the existing memory check.
func checkMemoryRecommended(parsed *intent.Intent) []checkResult {
	var checks []checkResult
	for i, node := range parsed.Nodes {
		memoryGB, err := render.ParseMemoryGB(node.Resources.Memory)
		if err != nil {
			checks = append(checks, checkResult{Name: "memory-recommended", Status: "fail",
				Message: fmt.Sprintf("node memory %q is invalid: %v", node.Resources.Memory, err)})
			continue
		}
		if memoryGB >= 8 {
			continue
		}
		nodeName := parsed.Name
		if len(parsed.Nodes) > 1 {
			nodeName = fmt.Sprintf("%s-node%d", parsed.Name, i)
		}
		checks = append(checks, checkResult{
			Name:    "memory-recommended",
			Status:  "warning",
			Message: fmt.Sprintf("node '%s' requests %s container memory, below java-tron's official minimum 8192MB (start.sh ALLOW_MIN_MEMORY); small containers risk JVM startup failure", nodeName, node.Resources.Memory),
		})
	}
	if len(checks) == 0 {
		return []checkResult{{
			Name:    "memory-recommended",
			Status:  "pass",
			Message: "all nodes meet java-tron's official 8192MB minimum",
		}}
	}
	return checks
}

// checkPorts probes every well-known port the intent will expose.
// Earlier this shelled out to `ss -tlnp` which doesn't exist on macOS,
// so the check silently reported every port as available. Use net.Dial
// instead — same behaviour as the diagnose port_listening checker, no
// runtime dependency.
func checkPorts(cmd *cobra.Command, tgt target.Target, node *intent.NodeSpec) []checkResult {
	ports := []struct {
		name string
		port int
	}{
		{"http", node.Ports.HTTP},
		{"grpc", node.Ports.GRPC},
		{"p2p", node.Ports.P2P},
	}

	var results []checkResult
	for _, p := range ports {
		if p.port == 0 {
			continue
		}
		conn, err := target.DialContext(cmd.Context(), tgt, "tcp", fmt.Sprintf("127.0.0.1:%d", p.port), 1500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			results = append(results, checkResult{
				Name:    fmt.Sprintf("port-%d", p.port),
				Status:  "fail",
				Message: fmt.Sprintf("Port %d (%s) already in use", p.port, p.name),
			})
		} else {
			results = append(results, checkResult{
				Name:    fmt.Sprintf("port-%d", p.port),
				Status:  "pass",
				Message: fmt.Sprintf("Port %d (%s) available", p.port, p.name),
			})
		}
	}
	return results
}
