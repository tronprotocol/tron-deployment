package shadowfork

import (
	"fmt"
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

var kgOutPath string

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
  trond shadow-fork keygen --output json          # for agents / jq

With --out the file is written as shell exports, which is what
scripts/poc-shadow-fork.sh sources:

  export SHADOW_FORK_WITNESS_KEY="..."
  export SHADOW_FORK_WITNESS_ADDRESS="T..."`,
	RunE: runKeygen,
}

func init() {
	keygenCmd.Flags().StringVar(&kgOutPath, "out", "",
		"write shell exports to this file (0600) instead of stdout")
}

func runKeygen(cmd *cobra.Command, _ []string) error {
	priv, hexAddr, addr, err := tronaddr.NewRandomAddress()
	if err != nil {
		return output.NewError("KEYGEN_ERROR", output.ExitGeneralError, err.Error())
	}

	outputFmt, _ := cmd.Flags().GetString("output")

	if kgOutPath != "" {
		// 0600 from the start: the key must never exist world-readable,
		// not even for the length of the write.
		f, err := os.OpenFile(kgOutPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return output.NewError("KEYGEN_ERROR", output.ExitGeneralError, err.Error())
		}
		_, werr := fmt.Fprintf(f,
			"export SHADOW_FORK_WITNESS_KEY=%q\nexport SHADOW_FORK_WITNESS_ADDRESS=%q\n",
			priv, addr)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return output.NewError("KEYGEN_ERROR", output.ExitGeneralError, werr.Error())
		}
		if outputFmt == "json" {
			// The private key is deliberately absent here: this path
			// wrote it to a 0600 file, and the JSON goes to stdout,
			// which lands in logs and agent transcripts.
			return output.WriteJSON(os.Stdout, map[string]any{
				"out":         kgOutPath,
				"address":     addr,
				"hex_address": hexAddr,
			})
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Witness keypair written to %s (mode 0600)\n  address: %s\n",
			kgOutPath, addr)
		return nil
	}

	if outputFmt == "json" {
		return output.WriteJSON(os.Stdout, map[string]any{
			"private_key": priv,
			"address":     addr,
			"hex_address": hexAddr,
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"export SHADOW_FORK_WITNESS_KEY=%q\nexport SHADOW_FORK_WITNESS_ADDRESS=%q\n", priv, addr)
	return nil
}
