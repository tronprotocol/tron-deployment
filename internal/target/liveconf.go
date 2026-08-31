package target

import (
	"context"
	"path/filepath"

	"github.com/tronprotocol/tron-deployment/internal/render"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// ReadLiveConfig reads the active HOCON configuration from a jar host or
// Docker container. Callers retain ownership of the target lifecycle.
func ReadLiveConfig(ctx context.Context, tgt Target, node *state.ManagedNode) (string, error) {
	if node.Runtime == "jar" {
		path := filepath.Join(node.InstallPath, "conf", node.Name+".conf")
		out, err := tgt.Exec(ctx, "cat", path)
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
	out, err := tgt.Exec(ctx, "docker", "exec", node.Name, "cat", render.ContainerConfDir+"/"+node.Name+".conf")
	if err != nil {
		return "", err
	}
	return string(out), nil
}
