// Package shadowfork wires the `trond shadow-fork` subcommand tree.
//
// The mutation logic lives in internal/dbfork (re-used by unit tests +
// the equivalence harness without dragging cobra in). This package is
// the thin presentation layer that maps CLI flags to dbfork's Config /
// Options and renders the Result for humans + MCP agents.
package shadowfork

import "github.com/spf13/cobra"

// Cmd is the parent command attached to root by cmd/root.go.
//
// The umbrella surface is narrow: `keygen` makes the witness pair the
// forked chain signs with, `mutate` rewrites the snapshot. Future
// subcommands would slot in here: an `init` to scaffold an example
// fork.conf, a `validate` to dry-run the loader without touching the
// DB, a `run` to chain mutate + private-network launch.
var Cmd = &cobra.Command{
	Use:   "shadow-fork",
	Short: "Shadow-fork testing utilities (Go port of java-tron DbFork)",
	Long: `Shadow-fork lets you take a real chain DB snapshot, replace
witnesses + fund accounts + tweak protocol parameters, and launch
a private network from the modified state — useful for testing
hard-fork upgrades, witness-set transitions, and contract-state
migrations without affecting mainnet.

Subcommands:

  trond shadow-fork keygen --out witness.env
  trond shadow-fork mutate --data-dir ./output-directory --config fork.conf

Mutation is byte-equivalent with java-tron's DbFork toolkit
(equivalence test runs in CI against a real Nile snapshot). Both
HOCON and YAML fork.conf formats are accepted; HOCON drops in
directly from existing java tooling.`,
}

func init() {
	Cmd.AddCommand(keygenCmd)
	Cmd.AddCommand(mutateCmd)
}
