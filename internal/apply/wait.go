package apply

import (
	"context"
	"fmt"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/target"
)

// WaitForReady polls the node's HTTP API until it
// responds 2xx or the timeout elapses. Used by Apply when Wait=true
// and exposed publicly so callers (verify command, MCP tool, recipe
// step) can reuse the same probe shape without duplicating the curl
// invocation.
//
// Docker probes run inside the container so they see its network. Jar probes
// use target.Get, which tunnels to the target's loopback for remote targets.
func WaitForReady(ctx context.Context, tgt target.Target, name, runtimeName string, httpPort int, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := ProbeURL(httpPort, "/wallet/getnowblock")
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	var lastErr error
	for {
		var err error
		if runtimeName == "jar" {
			_, err = target.Get(ctx, tgt, url, 5*time.Second)
		} else {
			_, err = tgt.Exec(ctx, "docker", "exec", name, "curl", "-fsS", "--max-time", "5", url)
		}
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			// lastErr is always non-nil here because we only reach
			// the select after a failed probe; surface both for
			// debuggability.
			return fmt.Errorf("%s (last probe error: %v)", ctx.Err(), lastErr)
		case <-tick.C:
		}
	}
}
