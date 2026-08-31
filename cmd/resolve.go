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

	// lock is held for the whole load-modify-save cycle. Every command
	// built on a nodeContext reads state here and writes it back through
	// SaveState, sometimes many seconds later, so the read and the write
	// have to sit inside one lock or a concurrent trond drops one of the
	// two updates.
	lock *state.Lock
}

// Close releases the state lock and any resources (e.g., SSH connections).
func (nc *nodeContext) Close() {
	if closer, ok := nc.Target.(interface{ Close() error }); ok {
		closer.Close()
	}
	if nc.lock != nil {
		nc.lock.Release()
		nc.lock = nil
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

// resolveNodeContext loads a node from state without keeping the state
// lock. Use it for commands that only read — logs, wait, exec, files,
// health, diagnose, verify-config. Holding the exclusive lock across
// those buys nothing, and `logs -f` or a long `wait` would keep every
// other trond process on the host blocked for as long as it runs.
func resolveNodeContext(name string) (*nodeContext, error) {
	return resolveNode(name, false)
}

// resolveNodeContextForWrite loads a node and keeps the state lock until
// Close. Commands that write the node list back — start, stop, restart,
// upgrade, rollback, heal, remove — need the read and the write inside
// one lock, or a concurrent trond drops one of the two updates.
func resolveNodeContextForWrite(name string) (*nodeContext, error) {
	return resolveNode(name, true)
}

func resolveNode(name string, forWrite bool) (*nodeContext, error) {
	store, err := state.NewStore(statePath())
	if err != nil {
		return nil, err
	}

	// A writer takes the lock before the read and holds it until Close,
	// so the load-modify-save cycle is atomic. A reader takes nothing:
	// the load below is a single Load() and nothing is written back.
	var lock *state.Lock
	if forWrite {
		lock = state.NewLock(stateDir())
		if err := acquireStateLock(lock); err != nil {
			return nil, err
		}
	}
	release := func() {
		if lock != nil {
			lock.Release()
		}
	}

	deployState, err := store.Load()
	if err != nil {
		release()
		return nil, err
	}

	node := store.GetNode(deployState, name)
	if node == nil {
		release()
		return nil, exitWithError("NODE_NOT_FOUND", output.ExitGeneralError,
			fmt.Sprintf("Node %q not found in state", name),
			"Run: trond list",
			"Deploy first: trond apply --intent <file>")
	}

	tgt, err := resolveTargetFromNode(node)
	if err != nil {
		release()
		return nil, exitWithError("TARGET_UNREACHABLE", output.ExitTargetUnreachable, err.Error())
	}

	rt := resolveRuntimeForNode(node, tgt)

	return &nodeContext{
		Store:   store,
		State:   deployState,
		Node:    node,
		Target:  tgt,
		Runtime: rt,
		lock:    lock,
	}, nil
}

func resolveTargetFromNode(node *state.ManagedNode) (target.Target, error) {
	t, err := target.FromManagedNode(node)
	if err != nil && node.Target.Type == "ssh" {
		return nil, fmt.Errorf("ssh connect to %s: %w", node.Target.Host, err)
	}
	return t, err
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

// stateLockTimeout bounds how long a command waits for the state lock.
// Long enough that a normal deploy finishing up is simply waited out,
// short enough that a stuck or forgotten process is reported rather
// than leaving the caller staring at a hung terminal.
const stateLockTimeout = 30 * time.Second

// acquireStateLock waits for the lock, but not forever. syscall.Flock
// with LOCK_EX blocks with no deadline, so the wait happens on a
// goroutine and the caller gives up after stateLockTimeout with an
// error that says what to do about it.
func acquireStateLock(lock *state.Lock) error {
	done := make(chan error, 1)
	go func() { done <- lock.Acquire() }()

	select {
	case err := <-done:
		if err != nil {
			return exitWithError("LOCK_ERROR", output.ExitGeneralError,
				"Failed to acquire state lock: "+err.Error(),
				"Check if another trond process is running")
		}
		return nil
	case <-time.After(stateLockTimeout):
		// The goroutine keeps waiting and will release on process exit;
		// the lock file is a shared resource, so abandoning the attempt
		// is safe.
		return exitWithError("LOCK_TIMEOUT", output.ExitGeneralError,
			fmt.Sprintf("Another trond process has held the state lock for %s", stateLockTimeout),
			"Find it with: ps aux | grep trond",
			"A stuck process can be ended; the lock is released when it exits")
	}
}
