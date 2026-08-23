package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	configCmd "github.com/tronprotocol/tron-deployment/cmd/config"
	networkCmd "github.com/tronprotocol/tron-deployment/cmd/network"
	shadowforkCmd "github.com/tronprotocol/tron-deployment/cmd/shadowfork"
	snapshotCmd "github.com/tronprotocol/tron-deployment/cmd/snapshot"
	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
)

var (
	// Global flags
	outputFormat string
	logFormat    string
	quiet        bool
	verbose      bool
	noColor      bool
	stateDirFlag string
)

// version, commit and buildTime are populated at link time via -ldflags by
// the Makefile and goreleaser. Defaults to "dev" so unstamped local builds
// still report something coherent.
var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "trond",
	Short: "TRON node deployment and lifecycle management",
	Long: `trond is a CLI tool for deploying and managing java-tron nodes.

It uses declarative intent files to describe desired node state,
then renders configuration and deploys via Docker or native jar+systemd.

Supports local and remote (SSH) targets with structured JSON output
for CI pipelines and AI agents.`,
	Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, buildTime),
	SilenceUsage:  true,
	SilenceErrors: true,
	// Apply --state-dir before any subcommand runs so subpackages
	// (cmd/network, cmd/config) see the same base.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Reject an unknown --output rather than falling through to text.
		// The writers switch on "json" and default to text, so `-o yaml`
		// (which an older contract doc advertised) or a plain typo used to
		// return a human table to a caller that asked for machine output —
		// silently, with exit 0. An agent then parses a table as JSON.
		if err := validateFormat("--output", outputFormat); err != nil {
			return err
		}
		if err := validateFormat("--log-format", logFormat); err != nil {
			return err
		}
		if stateDirFlag != "" {
			paths.SetBaseDir(stateDirFlag)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(configCmd.Cmd)
	rootCmd.AddCommand(networkCmd.Cmd)
	rootCmd.AddCommand(shadowforkCmd.Cmd)
	rootCmd.AddCommand(snapshotCmd.Cmd)

	rootCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "text", "Output format: text, json")
	rootCmd.PersistentFlags().StringVar(&logFormat, "log-format", "text", "Log format: text, json")
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false, "Suppress non-essential output")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Increase log verbosity")
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "Disable ANSI colors")
	// No --config flag. One was registered here and advertised in --help as
	// "Config file (default ~/.trond/config.yaml)", but nothing ever read the
	// variable: passing --config /nonexistent succeeded silently. There is no
	// config-file feature, so the flag is gone rather than left lying about a
	// setting it does not have. --state-dir below is the real knob.
	rootCmd.PersistentFlags().StringVar(&stateDirFlag, "state-dir", "", "Directory for state.json, audit.log, deployments (default ~/.trond, env: TROND_STATE_DIR)")
	// Persistent safety gate: refuse any mutating verb unless the node's
	// network is private. A one-way floor — this flag OR a truthy
	// TROND_REQUIRE_PRIVATE turns it on; once on it cannot be disabled for
	// the invocation. See internal/guard.
	rootCmd.PersistentFlags().BoolVar(&guard.FlagValue, "require-private", false,
		"Refuse to mutate a node unless its network is private (also: "+guard.EnvVar+"; a one-way safety floor for unattended agents)")
}

// Root returns the configured root cobra command, used by the doc/manpage
// generator under cmd/gendoc and by tests that want to walk the tree.
func Root() *cobra.Command { return rootCmd }

// Version returns the build-stamped version string set via -ldflags.
// Exposed for callers (main.go) that need it before cobra parses
// arguments — e.g. the OpenTelemetry resource tag.
func Version() string { return version }

// Execute runs the root command and returns the exit code.
// StructuredError values are rendered in the requested format and their
// ExitCode is returned. This runs after cobra has unwound all RunE deferred
// cleanup (state locks, SSH sessions, etc.), so exiting here is safe.
func Execute() int {
	if err := rootCmd.Execute(); err != nil {
		var sErr *output.StructuredError
		if errors.As(err, &sErr) {
			output.WriteError(os.Stderr, sErr, outputFormat)
			return sErr.ExitCode
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// OutputFormat returns the current output format flag value.
func OutputFormat() string {
	return outputFormat
}

// IsQuiet returns whether quiet mode is enabled.
func IsQuiet() bool {
	return quiet
}

// IsVerbose returns whether verbose mode is enabled.
func IsVerbose() bool {
	return verbose
}

// NoColor returns whether color output is disabled.
func NoColor() bool {
	return noColor
}

// Log returns a structured logger configured from global flags.
func Log() *output.Logger {
	return output.NewLogger(os.Stderr, logFormat == "json", verbose, quiet)
}

// mustMarkRequired marks a flag as required and panics if the flag does not
// exist. Failure is only possible at program start due to a programming
// error, so panicking is the right behavior — it fails loudly at init().
func mustMarkRequired(cmd *cobra.Command, name string) {
	if err := cmd.MarkFlagRequired(name); err != nil {
		panic(fmt.Sprintf("mark flag %q required on %s: %v", name, cmd.Name(), err))
	}
}

// validateFormat rejects a format value the writers cannot honour. Both
// output.Write* helpers switch on "json" and fall through to text for
// anything else, so without this an unrecognised value degrades silently:
// `-o yaml` (advertised by an old contract doc) or a typo returned a human
// table, with exit 0, to a caller that asked for machine-readable output.
func validateFormat(flag, value string) error {
	switch value {
	case "text", "json":
		return nil
	default:
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("unknown %s %q: expected text or json", flag, value))
	}
}
