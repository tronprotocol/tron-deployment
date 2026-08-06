package cmd

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/runtime"
	"github.com/tronprotocol/tron-deployment/internal/security"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// nodeContext bundles everything needed to operate on a managed node.
type nodeContext struct {
	Store   *state.Store
	State   *state.DeploymentState
	Node    *state.ManagedNode
	Target  target.Target
	Runtime runtime.Runtime
}

// Close releases resources (e.g., SSH connections).
func (nc *nodeContext) Close() {
	if closer, ok := nc.Target.(interface{ Close() error }); ok {
		closer.Close()
	}
}

// SaveState persists the current deployment state.
func (nc *nodeContext) SaveState() error {
	nc.Store.UpsertNode(nc.State, *nc.Node)
	return nc.Store.Save(nc.State)
}

// runtimeExec runs a command in the node's runtime context. For Docker nodes
// the command runs inside the container via "docker exec"; for jar nodes it
// runs on the target host. Used by wait probes and exec subcommand so the
// "where" of execution is consistent with each runtime.
func (nc *nodeContext) runtimeExec(ctx context.Context, bin string, args ...string) ([]byte, error) {
	if nc.Node.Runtime == "jar" {
		return nc.Target.Exec(ctx, bin, args...)
	}
	full := append([]string{"exec", nc.Node.Name, bin}, args...)
	return nc.Target.Exec(ctx, "docker", full...)
}

// requirePrivateForNode enforces the --require-private / TROND_REQUIRE_PRIVATE
// safety gate against a node's RECORDED network, using state ONLY — no target
// resolution. Mutators must call this BEFORE resolveNodeContext (which opens
// the target and can fail with TARGET_UNREACHABLE, masking the gate) and
// before any HUMAN_REQUIRED confirmation, so an agent learns "not private"
// first. Fast path: when the gate is off it reads no state at all. A missing
// node returns nil here — resolveNodeContext emits the canonical
// NODE_NOT_FOUND so the message isn't duplicated.
func requirePrivateForNode(name string) error {
	if !guard.Requested() {
		return nil
	}
	store, err := state.NewStore(statePath())
	if err != nil {
		return err
	}
	deployState, err := store.Load()
	if err != nil {
		return err
	}
	node := store.GetNode(deployState, name)
	if node == nil {
		return nil
	}
	return guard.Enforce(node.Network)
}

// requirePrivateForNodes is the multi-node analog of requirePrivateForNode
// (chaos partition/heal touch many nodes at once). It loads state ONCE,
// resolves each named node's recorded network, and enforces the gate across
// all of them up front — so a non-private node in the set refuses BEFORE any
// partial mutation. Missing nodes are skipped here; the command's own
// resolution emits the canonical NODE_NOT_FOUND. Fast path: no state read
// when the gate is off.
func requirePrivateForNodes(names ...string) error {
	if !guard.Requested() {
		return nil
	}
	store, err := state.NewStore(statePath())
	if err != nil {
		return err
	}
	deployState, err := store.Load()
	if err != nil {
		return err
	}
	refs := make([]guard.NodeRef, 0, len(names))
	for _, name := range names {
		if n := store.GetNode(deployState, name); n != nil {
			refs = append(refs, guard.NodeRef{Name: n.Name, Network: n.Network})
		}
	}
	return guard.EnforceNodes(refs)
}

// resolveNodeContext loads a node from state and constructs its target and runtime.
func resolveNodeContext(name string) (*nodeContext, error) {
	store, err := state.NewStore(statePath())
	if err != nil {
		return nil, err
	}

	deployState, err := store.Load()
	if err != nil {
		return nil, err
	}

	node := store.GetNode(deployState, name)
	if node == nil {
		return nil, exitWithError("NODE_NOT_FOUND", output.ExitGeneralError,
			fmt.Sprintf("Node %q not found in state", name),
			"Run: trond list",
			"Deploy first: trond apply --intent <file>")
	}

	tgt, err := resolveTargetFromNode(node)
	if err != nil {
		return nil, exitWithError("TARGET_UNREACHABLE", output.ExitTargetUnreachable, err.Error())
	}

	rt := resolveRuntimeForNode(node, tgt)

	return &nodeContext{
		Store:   store,
		State:   deployState,
		Node:    node,
		Target:  tgt,
		Runtime: rt,
	}, nil
}

func resolveTargetFromNode(node *state.ManagedNode) (target.Target, error) {
	switch node.Target.Type {
	case "ssh":
		t := target.NewSSHTarget(node.Target.Host, node.Target.Port, node.Target.User, node.Target.IdentityFile)
		if err := t.Connect(); err != nil {
			return nil, fmt.Errorf("ssh connect to %s: %w", node.Target.Host, err)
		}
		return t, nil
	default:
		return target.NewLocalTarget(), nil
	}
}

func resolveRuntimeForNode(node *state.ManagedNode, tgt target.Target) runtime.Runtime {
	switch node.Runtime {
	case "jar":
		jr := runtime.NewJarRuntime(tgt)
		// Tell the runtime where to purge from when remove --keep-data=false
		// runs. Without this, Remove(purge=true) silently does nothing.
		if node.InstallPath != "" {
			jr.SetPurgeInstallPath(node.InstallPath)
		}
		return jr
	default:
		return runtime.NewDockerRuntime(tgt, deploymentsDir())
	}
}

// auditEvent captures the arguments for an audit log write. Using a struct
// keeps call sites self-documenting — positional parameters were easy to
// reorder by mistake (e.g. swapping node and target).
type auditEvent struct {
	Command    string
	Node       string
	Target     string
	IntentHash string
	Result     string // "success", "error", "rollback"
	ErrorCode  string
	Detail     string // what was acted on; identifiers only, never payloads
	RunID      string // set by the process that mints the id; children inherit it via the environment
	Start      time.Time
}

// AuditRunIDEnv carries a recipe run's correlation id into the steps it
// re-execs. Read from the environment rather than passed as a flag: every
// trond command writes audit entries, and threading a parameter through
// all of them to serve one caller is the wrong shape.
const AuditRunIDEnv = "TROND_AUDIT_RUN_ID"

// writeAudit writes an audit log entry for a mutating command. Failures are
// logged but never propagated — losing an audit line should not break the
// command that triggered it.
func writeAudit(ev auditEvent) {
	al, err := security.NewAuditLog(auditLogPath())
	if err != nil {
		Log().Warn("audit log init failed", "error", err)
		return
	}
	entry := security.AuditEntry{
		Timestamp:  time.Now().UTC(),
		Command:    ev.Command,
		Node:       ev.Node,
		Target:     ev.Target,
		IntentHash: ev.IntentHash,
		Result:     ev.Result,
		DurationMs: time.Since(ev.Start).Milliseconds(),
		ErrorCode:  ev.ErrorCode,
		Detail:     ev.Detail,
		RunID:      cmp.Or(ev.RunID, os.Getenv(AuditRunIDEnv)),
	}
	if writeErr := al.Write(entry); writeErr != nil {
		Log().Warn("audit log write failed", "error", writeErr)
	}
}
