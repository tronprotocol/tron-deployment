package shadowfork

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/tronaddr"
)

// A shadow fork has to be signed by a witness whose key you hold, so
// every run starts by making one. scripts/poc-shadow-fork.sh used to
// shell out to Python's tronpy for this — its comment said trond had no
// subcommand for it — which put a pip install between an operator and a
// fork. The derivation is already in this repository for txgen, so the
// subcommand is a few lines over internal/tronaddr.

var (
	kgOutPath string
	kgCount   int
)

var keygenCmd = &cobra.Command{
	Use:   "keygen",
	Short: "Generate a witness keypair for a shadow-fork chain",
	Long: `Generate a fresh secp256k1 keypair and derive its TRON address.

The forked chain's witness set is rewritten to an address you can sign
for; this produces that pair. The key only ever signs blocks on your own
fork, but it is a real private key — the file is written 0600 and should
stay out of version control.

  trond shadow-fork keygen                        # print to stdout
  trond shadow-fork keygen --out witness.env      # shell-sourceable
  trond shadow-fork keygen --count 27             # a full slate
  trond shadow-fork keygen --output json          # for agents / jq

A one-witness fork produces blocks but never solidifies them: the
confirmation count sits at 1, far below the 19-of-27 a block needs
before java-tron treats it as irreversible. Generate a slate with
--count when the thing under test reads solidified state, or is about
solidification stalling.

With --out the file is written as shell exports, which is what
scripts/poc-shadow-fork.sh sources:

  export SHADOW_FORK_WITNESS_KEY="..."
  export SHADOW_FORK_WITNESS_ADDRESS="T..."

Past the first, each witness is numbered so a script can source the
file and loop:

  export SHADOW_FORK_WITNESS_KEY_2="..."
  export SHADOW_FORK_WITNESS_ADDRESS_2="T..."
  export SHADOW_FORK_WITNESS_COUNT=27`,
	RunE: runKeygen,
}

func init() {
	keygenCmd.Flags().StringVar(&kgOutPath, "out", "",
		"write shell exports to this file (0600) instead of stdout")
	keygenCmd.Flags().IntVar(&kgCount, "count", 1,
		"number of witnesses to generate — a chain needs more than 2/3 of them producing before it solidifies")
}

// witnessPair is one generated slate entry.
type witnessPair struct {
	PrivateKey string `json:"private_key,omitempty"`
	Address    string `json:"address"`
	HexAddress string `json:"hex_address"`
}

func runKeygen(cmd *cobra.Command, _ []string) error {
	if kgCount < 1 {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			"--count must be at least 1")
	}

	pairs := make([]witnessPair, 0, kgCount)
	for range kgCount {
		priv, hexAddr, addr, err := tronaddr.NewRandomAddress()
		if err != nil {
			return output.NewError("KEYGEN_ERROR", output.ExitGeneralError, err.Error())
		}
		pairs = append(pairs, witnessPair{PrivateKey: priv, Address: addr, HexAddress: hexAddr})
	}

	outputFmt, _ := cmd.Flags().GetString("output")

	if kgOutPath != "" {
		// 0600 from the start: the keys must never exist world-readable,
		// not even for the length of the write.
		f, err := os.OpenFile(kgOutPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return output.NewError("KEYGEN_ERROR", output.ExitGeneralError, err.Error())
		}
		werr := writeExports(f, pairs)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return output.NewError("KEYGEN_ERROR", output.ExitGeneralError, werr.Error())
		}
		if outputFmt == "json" {
			// The private keys are deliberately absent: the caller asked
			// for them on disk, and stdout ends up in logs and agent
			// transcripts.
			redacted := make([]witnessPair, len(pairs))
			for i, p := range pairs {
				redacted[i] = witnessPair{Address: p.Address, HexAddress: p.HexAddress}
			}
			return output.WriteJSON(os.Stdout, map[string]any{
				"out":       kgOutPath,
				"count":     len(pairs),
				"witnesses": redacted,
			})
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%d witness keypair(s) written to %s (mode 0600)\n",
			len(pairs), kgOutPath)
		for i, p := range pairs {
			fmt.Fprintf(cmd.OutOrStdout(), "  %2d. %s\n", i+1, p.Address)
		}
		return nil
	}

	if outputFmt == "json" {
		return output.WriteJSON(os.Stdout, map[string]any{
			"count":     len(pairs),
			"witnesses": pairs,
		})
	}
	return writeExports(cmd.OutOrStdout(), pairs)
}

// writeExports renders the slate as shell exports. The first witness is
// unsuffixed so an existing single-witness script keeps working; the
// rest are numbered from 2, and COUNT lets a caller loop.
func writeExports(w io.Writer, pairs []witnessPair) error {
	for i, p := range pairs {
		suffix := ""
		if i > 0 {
			suffix = fmt.Sprintf("_%d", i+1)
		}
		if _, err := fmt.Fprintf(w,
			"export SHADOW_FORK_WITNESS_KEY%s=%q\nexport SHADOW_FORK_WITNESS_ADDRESS%s=%q\n",
			suffix, p.PrivateKey, suffix, p.Address); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "export SHADOW_FORK_WITNESS_COUNT=%d\n", len(pairs))
	return err
}
