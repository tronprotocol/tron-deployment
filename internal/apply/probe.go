package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/render"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// LiveStatus issues a small set of cheap HTTP probes against a
// running node and returns the discovered fields (block_height,
// is_synced, peer_count). Errors are silently dropped — the caller
// sees a key appear or not, never a failure on the whole call.
//
// The probe path differs by runtime: docker nodes get reached via
// `docker exec <name> curl ...` so the request sees the container's
// network (host port mapping may be delayed on first start). Jar
// nodes hit the host-side curl directly.
//
// Lives in internal/apply alongside WaitForReady because both are
// "operate on a deployed node via target.Target" primitives. Used
// by `trond status`, the MCP `status` tool, and any caller that
// wants the same combined state + live view.
func LiveStatus(ctx context.Context, tgt target.Target, node *state.ManagedNode) map[string]any {
	out := map[string]any{}
	if tgt == nil || node == nil {
		return out
	}
	port := PortOrDefault(node.HTTPPort, 8090)

	// probe issues a request against the node's HTTP API. body=="" is a
	// GET (TRON endpoints that take no params, e.g. getnowblock); a
	// non-empty body is POSTed as JSON (e.g. getblockbynum needs
	// {"num":N}). Jar nodes go through the target-aware HTTP client
	// (SSH-tunnelled for remote targets); docker nodes curl inside the
	// container via `docker exec`.
	probe := func(path, body string) ([]byte, error) {
		url := ProbeURL(port, path)
		if node.Runtime == "jar" {
			if body == "" {
				return target.Get(ctx, tgt, url, 2*time.Second)
			}
			return target.Post(ctx, tgt, url, []byte(body), 2*time.Second)
		}
		args := []string{"-fsS", "--max-time", "2"}
		if body != "" {
			args = append(args, "-X", "POST", "-H", "Content-Type: application/json", "-d", body)
		}
		args = append(args, url)
		return tgt.Exec(ctx, "docker", append([]string{"exec", node.Name, "curl"}, args...)...)
	}

	if data, err := probe("/wallet/getnowblock", ""); err == nil {
		var block struct {
			BlockHeader struct {
				RawData struct {
					Number    int64 `json:"number"`
					Timestamp int64 `json:"timestamp"`
				} `json:"raw_data"`
			} `json:"block_header"`
		}
		if json.Unmarshal(data, &block) == nil {
			ts := block.BlockHeader.RawData.Timestamp
			// Gate `healthy` on a real block, not just parseable JSON. An
			// empty body `{}` or an error-shaped 200 (`{"Error":"..."}`)
			// unmarshals cleanly into the zero value — number 0, timestamp
			// 0 — and must NOT read as healthy. Every genuine getnowblock
			// (including a private-net genesis) carries a non-zero block
			// timestamp, so ts>0 is the integrity check that the node
			// actually served a block. healthy is liveness only ("RPC is
			// serving blocks"), NOT a sync guarantee — is_synced/block_height
			// carry that. Only ever set true here; callers seed false so the
			// field is always present and fail-safe.
			if ts > 0 {
				out["block_height"] = block.BlockHeader.RawData.Number
				out["healthy"] = true
				// "synced" heuristic: tip within 60s of now. Good enough
				// for dashboards; not a consensus-level claim.
				lag := time.Since(time.UnixMilli(ts))
				out["is_synced"] = lag < 60*time.Second
			}
		}
	}

	if data, err := probe("/wallet/listnodes", ""); err == nil {
		var nodes struct {
			Nodes []any `json:"nodes"`
		}
		if json.Unmarshal(data, &nodes) == nil {
			out["peer_count"] = len(nodes.Nodes)
		}
	}

	// genesis_block_id — the chain's identity fingerprint (the block-0 TRON
	// block id). Probed LAST and treated as optional metadata: if the 3s
	// budget is already spent on the primary signals above, this drops
	// rather than starving healthy/block_height. The genesis never changes,
	// so the value is stable for the node's lifetime.
	//
	// NB: this is the TRON *block id* (the first 8 bytes are the block
	// number — zero for block 0 — not the raw header hash), and it is NOT
	// the TVM CHAINID value: CHAINID returns the full id only when certain
	// VM flags are off, the last 4 bytes when on. We deliberately expose the
	// unambiguous full id, not a flag-dependent chain_id (see TODOS.md).
	if data, err := probe("/wallet/getblockbynum", `{"num":0}`); err == nil {
		var block struct {
			BlockID string `json:"blockID"`
		}
		if json.Unmarshal(data, &block) == nil && isHex64(block.BlockID) {
			out["genesis_block_id"] = block.BlockID
		}
	}

	return out
}

// ContainerID returns the full 64-hex docker container ID for a node, or
// "" when the node isn't a docker node, the daemon can't be reached, or
// no container exists yet. Best-effort: callers treat "" as unknown and
// omit the field rather than surface a probe error. Works for both local
// and ssh targets via tgt.Exec. An agent uses this to attach/exec against
// the exact container backing the rig (A1: container_id).
func ContainerID(ctx context.Context, tgt target.Target, node *state.ManagedNode) string {
	if tgt == nil || node == nil || node.Runtime != "docker" {
		return ""
	}
	out, err := tgt.Exec(ctx, "docker", "inspect", "-f", "{{.Id}}", node.Name)
	if err != nil {
		return ""
	}
	return NormalizeContainerID(string(out))
}

// isHex64 reports whether s is exactly 64 lowercase hex chars — the shape of
// a TRON block id (and a docker container id). Rejects empty/error-shaped
// responses so a malformed getblockbynum reply doesn't set genesis_block_id.
func isHex64(s string) bool {
	return len(s) == 64 && strings.TrimLeft(s, "0123456789abcdef") == ""
}

// NormalizeContainerID trims and validates raw `docker inspect -f {{.Id}}`
// output, returning the clean 64-hex container ID or "" if the input isn't
// exactly 64 lowercase hex chars. docker prints error text (or nothing) to
// stdout in some states, so this rejects anything malformed. Single source
// of truth shared by apply.ContainerID (target.Exec → []byte) and cmd's
// dockerContainerID (localDockerExec → string) so the format rule can't
// drift between the two call sites.
func NormalizeContainerID(s string) string {
	id := strings.TrimSpace(s)
	if len(id) != 64 || strings.TrimLeft(id, "0123456789abcdef") != "" {
		return ""
	}
	return id
}

// LogsDescriptor returns a machine-readable locator telling an external
// log consumer (e.g. mcp-logs) exactly how to read this node's logs
// without screen-scraping. The shape is runtime-discriminated because the
// retrieval mechanism genuinely differs — a single "log_path" string would
// be wrong for half the runtimes:
//
//   - docker: java-tron logs to a file INSIDE the container
//     (/java-tron/logs/tron.log); read it via `docker exec <container>`.
//   - jar: the systemd unit's output lands in the journal; read it via
//     `journalctl -u <unit>`.
//
// Static (no I/O) and always present, so a caller can plan retrieval even
// for a stopped node (A1: node-log paths — the mcp-logs unblocker).
func LogsDescriptor(node *state.ManagedNode) map[string]any {
	if node == nil {
		return nil
	}
	switch node.Runtime {
	case "jar":
		return map[string]any{
			"runtime": "jar",
			"unit":    fmt.Sprintf("tron-%s.service", node.Name),
		}
	case "docker":
		return map[string]any{
			"runtime":   "docker",
			"container": node.Name,
			"path":      render.ContainerLogPath,
		}
	default:
		// Unknown/unrecorded runtime (e.g. a legacy node from before the
		// field was stored, or a future runtime). Don't assert a concrete
		// locator we can't stand behind — mislabeling such a node as
		// "docker" would point a log consumer at a container path that
		// doesn't exist. Report the runtime honestly so the caller sees it
		// can't act on this descriptor.
		return map[string]any{"runtime": node.Runtime}
	}
}
