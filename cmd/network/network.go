package network

import (
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/security"
)

// Cmd is the parent `trond network` command.
var Cmd = &cobra.Command{
	Use:   "network",
	Short: "Manage multi-node private networks",
}

func init() {
	Cmd.AddCommand(createCmd)
	Cmd.AddCommand(statusCmd)
	Cmd.AddCommand(destroyCmd)
	// upgradeCmd registers itself in upgrade.go's init(), next to the
	// MarkFlagRequired calls that must run with it. Adding it here too put
	// it in the parent's child list twice, so `network --help` printed the
	// row twice. `add` follows the same self-registering pattern.
}

// auditEvent mirrors the struct in the cmd package. The network subcommand
// lives in its own package for cobra wiring, so the helper is duplicated
// here rather than pulled out into an internal audit package — the cost of
// the extra file is lower than a new import cycle.
type auditEvent struct {
	Command   string
	Node      string
	Target    string
	Result    string
	ErrorCode string
	Start     time.Time
}

func writeAudit(ev auditEvent) {
	al, err := security.NewAuditLog(paths.AuditLog())
	if err != nil {
		log().Warn("audit log init failed", "error", err)
		return
	}
	entry := security.AuditEntry{
		Timestamp:  time.Now().UTC(),
		Command:    ev.Command,
		Node:       ev.Node,
		Target:     ev.Target,
		Result:     ev.Result,
		DurationMs: time.Since(ev.Start).Milliseconds(),
		ErrorCode:  ev.ErrorCode,
	}
	if writeErr := al.Write(entry); writeErr != nil {
		log().Warn("audit log write failed", "error", writeErr)
	}
}

func log() *output.Logger {
	return output.NewLogger(os.Stderr, false, false, false)
}
