package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

var bootstrapIntentPath string

var bootstrapResolveTarget = resolveTarget

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Install prerequisites on the target",
	Long:  "Install Docker or JDK on the target machine based on the intent's runtime requirement.",
	RunE:  runBootstrap,
}

func init() {
	bootstrapCmd.Flags().StringVar(&bootstrapIntentPath, "intent", "", "Path to intent.yaml (required)")
	mustMarkRequired(bootstrapCmd, "intent")
	rootCmd.AddCommand(bootstrapCmd)
}

func runBootstrap(cmd *cobra.Command, args []string) error {

	parsed, err := intent.Load(bootstrapIntentPath)
	if err != nil {
		return exitWithError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
	}
	if err := guard.Enforce(parsed.Network); err != nil {
		return err
	}

	tgt, err := bootstrapResolveTarget(parsed)
	if err != nil {
		return exitWithError("TARGET_UNREACHABLE", output.ExitTargetUnreachable, err.Error())
	}
	if closer, ok := tgt.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	// Host preparation installs packages, which the ordinary SSH whitelist
	// does not allow — deliberately, so that no lifecycle path or `trond
	// exec` can. bootstrap is the one command that may, and only for the
	// lifetime of this target.
	if p, ok := tgt.(interface{ SetProvisioning(bool) }); ok {
		p.SetProvisioning(true)
	}

	runtimeType := parsed.Target.Runtime
	if runtimeType == "" {
		runtimeType = "docker"
	}

	ctx := cmd.Context()
	var installed []string

	switch runtimeType {
	case "docker":
		if err := installDocker(ctx, tgt); err != nil {
			return exitWithError("BOOTSTRAP_ERROR", output.ExitGeneralError,
				fmt.Sprintf("Failed to install Docker: %v", err))
		}
		installed = append(installed, "docker")

	case "jar":
		if err := installJDK(ctx, tgt); err != nil {
			return exitWithError("BOOTSTRAP_ERROR", output.ExitGeneralError,
				fmt.Sprintf("Failed to install JDK: %v", err))
		}
		installed = append(installed, "jdk")

		// Create system user for jar runtime
		user := "tron"
		if len(parsed.Nodes) > 0 && parsed.Nodes[0].SystemUser != "" {
			user = parsed.Nodes[0].SystemUser
		}
		// Until provisioning mode existed this call was refused by the
		// SSH whitelist and failed on every remote target, so discarding
		// the error was invisible. Now that it runs, a real failure —
		// no permission, a conflicting uid, no /usr/sbin/nologin — would
		// otherwise be reported as a created user.
		if out, err := tgt.Exec(ctx, "useradd", "--system", "--no-create-home",
			"--shell", "/usr/sbin/nologin", user); err != nil {
			if !userAlreadyExists(out) {
				return exitWithError("BOOTSTRAP_ERROR", output.ExitGeneralError,
					fmt.Sprintf("Failed to create system user %q: %v: %s", user, err, strings.TrimSpace(string(out))))
			}
		}
		installed = append(installed, "user:"+user)
	}

	result := map[string]any{
		"installed": installed,
		"target":    tgt.String(),
	}
	writeResult(result)
	return nil
}

func installDocker(ctx context.Context, tgt target.Target) error {
	// Try apt-get first (Debian/Ubuntu)
	if out, _ := tgt.Exec(ctx, "which", "apt-get"); strings.TrimSpace(string(out)) != "" {
		cmds := [][]string{
			{"apt-get", "update", "-y"},
			{"apt-get", "install", "-y", "ca-certificates", "curl", "gnupg"},
			{"sh", "-c", "curl -fsSL https://get.docker.com | sh"},
		}
		for _, c := range cmds {
			if _, err := tgt.Exec(ctx, c[0], c[1:]...); err != nil {
				return fmt.Errorf("run %s: %w", c[0], err)
			}
		}
		return nil
	}

	// Try yum (RHEL/CentOS)
	if out, _ := tgt.Exec(ctx, "which", "yum"); strings.TrimSpace(string(out)) != "" {
		if _, err := tgt.Exec(ctx, "sh", "-c", "curl -fsSL https://get.docker.com | sh"); err != nil {
			return fmt.Errorf("install docker: %w", err)
		}
		return nil
	}

	return fmt.Errorf("unsupported package manager; install Docker manually")
}

func installJDK(ctx context.Context, tgt target.Target) error {
	// Try apt-get (Debian/Ubuntu)
	if out, _ := tgt.Exec(ctx, "which", "apt-get"); strings.TrimSpace(string(out)) != "" {
		if _, err := tgt.Exec(ctx, "apt-get", "update", "-y"); err != nil {
			return err
		}
		if _, err := tgt.Exec(ctx, "apt-get", "install", "-y", "openjdk-17-jre-headless"); err != nil {
			return err
		}
		return nil
	}

	// Try yum (RHEL/CentOS)
	if out, _ := tgt.Exec(ctx, "which", "yum"); strings.TrimSpace(string(out)) != "" {
		if _, err := tgt.Exec(ctx, "yum", "install", "-y", "java-17-openjdk-headless"); err != nil {
			return err
		}
		return nil
	}

	return fmt.Errorf("unsupported package manager; install JDK 17 manually")
}

// userAlreadyExists reports whether a useradd failure was only the user
// being there already, which bootstrap has to tolerate: it is expected
// to be re-runnable, and the second run finds the user from the first.
//
// useradd exits 9 for "name already in use", but the exit status does
// not survive target.Exec's error, so the message is what is left to
// match on. Both util-linux and busybox wording are covered.
func userAlreadyExists(out []byte) bool {
	msg := strings.ToLower(string(out))
	return strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "already in use")
}
