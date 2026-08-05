// txgen — TRON Stress-Test Transaction Generator.
//
// A pure-Go alternative to tron-docker/tools/stress_test. Same general
// shape (generate → broadcast → statistic), JSON config, CSV
// intermediate format, and report layout that's plug-compatible with
// the upstream tool's downstream dashboards. Receiver address
// generation, which the upstream tool splits into a separate `collect`
// subcommand, is inlined into `generate` here.
//
// Why a Go rewrite:
//   - No java-tron source / JDK runtime needed; ~10 MB static binary
//     you can scp to any node.
//   - Shares HTTP infrastructure with tools/replay (broadcast client).
//   - Same lifecycle ergonomics as replay (no embedded fork of FullNode).
//
// Usage: see README.md in this directory.
//
// Dependencies: stdlib + golang.org/x/crypto (Keccak-256) +
// github.com/decred/dcrd/dcrec/secp256k1/v4 (signing) +
// tools/common/broadcast (shared HTTP broadcast) + google.golang.org/grpc
// and internal/tronproto/pb (the grpc broadcast transport).
// Go version: 1.21+
//
// File layout:
//   - main.go        entry point, subcommand dispatch
//   - config.go      JSON config struct + loader
//   - address.go     TRON address utils (Base58Check, hex, key derivation)
//   - sign.go        secp256k1 signing (low-S canonical)
//   - node.go        java-tron HTTP API client (create*, getblock*)
//   - csv.go         CSV read/write helpers
//   - generate.go    `generate` subcommand (builds receivers + signs txs)
//   - broadcast.go   `broadcast` subcommand
//   - transport.go   broadcast transport interface + http implementation
//   - grpc_node.go   grpc transport (Wallet.BroadcastTransaction)
//   - statistic.go   `statistic` subcommand + TPS math + report formatters
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

const usage = `txgen — TRON stress-test transaction generator.

Usage:
  txgen <subcommand> [flags]

Subcommands:
  generate     Build + sign synthetic txs → CSV files (receivers auto-generated)
  broadcast    Replay generated CSV at a target TPS → report
  statistic    Compute on-chain TPS for a block range
  keygen       Generate a post-quantum keypair (FN_DSA_512 requires build-txgen-falcon)
  help         Print this help

Flags:
  -c, --config <path>   JSON config file (default: ./txgen.json)

See tools/txgen/README.md for the full config schema and examples.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	case "keygen":
		// keygen does not use a config file; parse its own flags.
		if err := runKeygen(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	case "generate", "broadcast", "statistic":
		// known subcommand; validated here so the os.Exit below runs
		// before any defer is registered (gocritic exitAfterDefer).
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
		fmt.Print(usage)
		os.Exit(2)
	}

	sub := os.Args[1]
	fs := flag.NewFlagSet(sub, flag.ExitOnError)
	configPath := fs.String("c", "txgen.json", "JSON config file")
	fs.StringVar(configPath, "config", "txgen.json", "JSON config file (alias for -c)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		log.Fatal(err)
	}

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// dispatch owns the cancelable context so its deferred cancel() runs
	// before main's log.Fatalf below (gocritic exitAfterDefer).
	if err := dispatch(sub, cfg); err != nil {
		log.Fatalf("%s: %v", sub, err)
	}
}

// dispatch runs the chosen subcommand under a signal-cancelable context.
func dispatch(sub string, cfg *Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	switch sub {
	case "generate":
		return runGenerate(ctx, cfg)
	case "broadcast":
		return runBroadcast(ctx, cfg)
	case "statistic":
		return runStatistic(ctx, cfg)
	}
	return nil
}
