package security

import (
	"fmt"
	"strings"
)

// allowedCommands is the whitelist of commands trond may execute on remote
// targets. The list is intentionally narrow: anything trond's lifecycle code
// genuinely needs, and nothing more. Past expansions (apt-get / yum / dnf /
// curl / wget / kill / pkill) were dropped because:
//   - bootstrap is the only legitimate consumer of package managers, and it
//     can be re-allowed scoped to that one command path if needed
//   - `trond exec` over SSH executes whichever name the caller passes, so a
//     wide list effectively grants the SSH user's full authority to anyone
//     who can run `trond exec` against the node
//   - curl/wget over SSH would let intent fields drive arbitrary network
//     egress on the target — out-of-band channel for exfil
//
// Values reachable in args (path / port / file content) are still
// shell-quoted by SSHTarget.Exec.
var allowedCommands = map[string]bool{
	// Container runtime
	"docker": true,
	// Systemd lifecycle (jar runtime)
	"systemctl":  true,
	"journalctl": true,
	// Read-only file probes used by diagnose / health / inspect
	"cat":     true,
	"ls":      true,
	"grep":    true,
	"tail":    true,
	"df":      true,
	"free":    true,
	"uname":   true,
	"id":      true,
	"which":   true,
	"ps":      true,
	"ss":      true,
	"netstat": true,
	// File mutation needed by writeRemoteFile / WriteFile path on jar nodes
	"mkdir":     true,
	"chmod":     true,
	"chown":     true,
	"tee":       true,
	"rm":        true, // jar runtime cleanup; constrained by quoted args
	"mv":        true, // Phase 4 SSH build pipeline: atomic .tmp → final rename
	"sha256sum": true, // jar download integrity check + Phase 4 transfer-skip probe
	// JVM probe (preflight) — does NOT execute arbitrary class paths
	"java": true,
}

// ValidateCommand checks if a command is in the SSH whitelist.
// Returns an error if the command is not allowed.
func ValidateCommand(cmd string) error {
	base := extractBaseCommand(cmd)
	if !allowedCommands[base] {
		return fmt.Errorf("command %q is not in the SSH whitelist; allowed commands: %s",
			base, allowedCommandList())
	}
	return nil
}

// provisioningCommands are allowed on top of allowedCommands, and only for a
// target that has been put in provisioning mode. `trond bootstrap` is the one
// caller that does so, which is the scoping the comment on allowedCommands
// asks for: these names install packages and run the vendor's Docker
// installer, so they must not be reachable from `trond exec` or from any
// lifecycle path driven by intent fields.
var provisioningCommands = map[string]bool{
	"apt-get": true,
	"yum":     true,
	"useradd": true,
	// The Docker convenience script is delivered by a pipeline, so bootstrap
	// hands it to a shell. Nothing outside bootstrap may do this.
	"sh": true,
}

// ValidateProvisioningCommand accepts the ordinary whitelist plus the
// package-manager and shell commands bootstrap needs.
func ValidateProvisioningCommand(cmd string) error {
	base := extractBaseCommand(cmd)
	if allowedCommands[base] || provisioningCommands[base] {
		return nil
	}
	return fmt.Errorf("command %q is allowed neither by the SSH whitelist nor in provisioning mode; allowed commands: %s",
		base, allowedCommandList())
}

// extractBaseCommand gets the first word (the command name) from a command string.
func extractBaseCommand(cmd string) string {
	// Handle sudo prefix
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return ""
	}
	base := parts[0]
	if base == "sudo" && len(parts) > 1 {
		base = parts[1]
	}
	return base
}

func allowedCommandList() string {
	cmds := make([]string, 0, len(allowedCommands))
	for cmd := range allowedCommands {
		cmds = append(cmds, cmd)
	}
	return strings.Join(cmds, ", ")
}
