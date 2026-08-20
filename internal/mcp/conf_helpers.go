package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/tronprotocol/tron-deployment/internal/render"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// readLiveConfigForMCP returns the bytes of the conf file currently
// in use by the running node, regardless of runtime. Shared by
// resources.go (trond://nodes/<name>/conf) and the future
// verify_config tool.
func readLiveConfigForMCP(ctx context.Context, tgt target.Target, node *state.ManagedNode) (string, error) {
	if node.Runtime == "jar" {
		out, err := tgt.Exec(ctx, "cat", node.InstallPath+"/conf/"+node.Name+".conf")
		if err != nil {
			return "", fmt.Errorf("read jar conf: %w", err)
		}
		return string(out), nil
	}
	out, err := tgt.Exec(ctx, "docker", "exec", node.Name, "cat",
		"/java-tron/conf/"+node.Name+".conf")
	if err != nil {
		return "", fmt.Errorf("docker exec cat: %w", err)
	}
	return string(out), nil
}

// redactConfText removes witness signing keys from a whole conf before
// it is handed out. Split/join around render.RedactWitnessLines, which
// finds the key values by parsing rather than by line shape, so the
// formatting of the node's live conf does not matter.
func redactConfText(conf string) string {
	if conf == "" {
		return conf
	}
	// Splitting on \n and rejoining preserves \r\n line endings, since
	// the \r stays attached to the line content.
	return strings.Join(render.RedactWitnessLines(strings.Split(conf, "\n")), "\n")
}
