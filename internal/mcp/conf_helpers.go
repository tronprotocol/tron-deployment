package mcp

import (
	"context"
	"fmt"

	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// readLiveConfigForMCP returns the bytes of the conf file currently
// in use by the running node, regardless of runtime. Shared by
// resources.go (trond://nodes/<name>/conf) and the future
// verify_config tool.
func readLiveConfigForMCP(ctx context.Context, tgt target.Target, node *state.ManagedNode) (string, error) {
	out, err := target.ReadLiveConfig(ctx, tgt, node)
	if err != nil {
		if node.Runtime == "jar" {
			return "", fmt.Errorf("read jar conf: %w", err)
		}
		return "", fmt.Errorf("docker exec cat: %w", err)
	}
	return out, nil
}
