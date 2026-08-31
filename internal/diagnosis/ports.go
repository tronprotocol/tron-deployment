package diagnosis

import (
	"context"
	"fmt"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/target"
)

// PortsChecker verifies expected ports are accepting TCP connections.
//
// We dial 127.0.0.1 through the target (SSH direct-tcpip when the target
// is remote, a plain host dial for local), instead of running `ss -tlnp`
// inside the target. This works the same on Linux and macOS (the previous
// `ss` invocation silently returned empty on Darwin, marking every port
// as "not listening" even when java-tron was healthy and serving traffic).
// For docker-runtime nodes, the host-side mapped port is exactly what test
// harnesses care about reaching, so this is also the right thing
// semantically.
type PortsChecker struct{}

func (c *PortsChecker) Name() string { return "port_listening" }

func (c *PortsChecker) Run(ctx context.Context, tgt target.Target, opts CheckOpts) CheckResult {
	// Preserve the original semantics exactly: defaults apply only when
	// BOTH ports are unset; a partially specified pair is checked as-is.
	ports := []int{opts.HTTPPort, opts.GRPCPort}
	if opts.HTTPPort == 0 && opts.GRPCPort == 0 {
		ports = []int{8090, 50051}
	}

	var missing []int
	for _, port := range ports {
		if port == 0 {
			continue
		}
		conn, err := target.DialContext(ctx, tgt, "tcp", fmt.Sprintf("127.0.0.1:%d", port), 1500*time.Millisecond)
		if err != nil {
			missing = append(missing, port)
			continue
		}
		_ = conn.Close()
	}

	if len(missing) > 0 {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusFail,
			Message: fmt.Sprintf("Ports not accepting connections: %v", missing),
			Suggestions: []string{
				fmt.Sprintf("Check node status: trond status %s", opts.NodeName),
				"Review node logs: trond logs " + opts.NodeName,
				"Verify firewall rules",
			},
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusPass,
		Message: fmt.Sprintf("All expected ports listening (%v)", ports),
	}
}
