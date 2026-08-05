package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

var execCmd = &cobra.Command{
	Use:   "exec <node> -- <cmd> [args...]",
	Short: "Execute a command on a managed node",
	Long: `Run an arbitrary command inside a managed node and stream its output.

For Docker runtime nodes the command runs inside the container via
"docker exec". For jar runtime nodes the command runs on the host where the
service is deployed (local target or SSH target depending on intent).

Use "--" to separate trond flags from the command line passed to the node:

    trond exec my-fullnode -- curl -s http://127.0.0.1:8090/wallet/getnowblock
    trond exec my-fullnode -- ls /var/log/tron`,
	Args: cobra.MinimumNArgs(2),
	RunE: runExec,
}

func init() {
	rootCmd.AddCommand(execCmd)
}

func runExec(cmd *cobra.Command, args []string) error {
	// Cobra parses global flags normally, then everything after "--" lands
	// in args verbatim. ArgsLenAtDash() tells us the position of "--": all
	// args before it are positional, after are the inner command.
	dashIdx := cmd.ArgsLenAtDash()
	var nodeName string
	var rest []string
	if dashIdx == -1 {
		// No "--" — fall back to "node is first arg, rest is the command".
		nodeName = args[0]
		rest = args[1:]
	} else {
		if dashIdx != 1 {
			return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
				"usage: trond exec <node> -- <cmd> [args...]")
		}
		nodeName = args[0]
		rest = args[1:]
	}
	if nodeName == "" {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			"usage: trond exec <node> -- <cmd> [args...]")
	}

	// exec runs a caller-supplied program against the node: on a jar node
	// that is rest[0] on the target host, on a docker node it is
	// `docker exec <node> ...` inside the container. Either way it is at
	// least as capable as the verbs that already gate — `trond exec
	// <mainnet-node> -- rm -rf <datadir>` is a mutation by any reading —
	// so it takes the same gate.
	//
	// Placed here, at the earliest point the node name is known and ahead
	// of the remaining usage check, so that `--require-private` reports
	// "not private" rather than a usage error: an agent should learn the
	// safety fact first. Ahead of resolveNodeContext for the reason
	// documented on requirePrivateForNode — otherwise an unreachable
	// mainnet node masks the gate with TARGET_UNREACHABLE.
	start := time.Now()
	if err := requirePrivateForNode(nodeName); err != nil {
		return err
	}

	if len(rest) == 0 {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			"usage: trond exec <node> -- <cmd> [args...]")
	}

	nc, err := resolveNodeContext(nodeName)
	if err != nil {
		return err
	}
	defer nc.Close()

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// For Docker nodes, wrap with `docker exec` so the command runs inside
	// the container. Jar nodes execute directly on the target host.
	var bin string
	var fullArgs []string
	if nc.Node.Runtime == "jar" {
		bin = rest[0]
		fullArgs = rest[1:]
	} else {
		bin = "docker"
		fullArgs = append([]string{"exec", nodeName}, rest...)
	}

	out, execErr := nc.Target.Exec(ctx, bin, fullArgs...)
	// Always emit captured output even on error — the caller usually wants it.
	os.Stdout.Write(out)
	if execErr != nil {
		writeAudit(auditEvent{Command: "exec", Node: nodeName, Target: nc.Target.String(),
			Result: "error", ErrorCode: "EXEC_ERROR", Start: start})
		return output.NewError("EXEC_ERROR", output.ExitGeneralError,
			fmt.Sprintf("exec on %s failed: %v", nodeName, execErr))
	}
	// Audited like every other verb that touches a node. The entry records
	// that an exec happened, not what ran: AuditEntry has no field for it,
	// and the argv is the one place a caller is most likely to have put a
	// token or key. Recording the program name would need a schema change.
	writeAudit(auditEvent{Command: "exec", Node: nodeName, Target: nc.Target.String(),
		Result: "success", Start: start})
	return nil
}
