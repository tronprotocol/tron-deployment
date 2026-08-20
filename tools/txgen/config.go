package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/tronprotocol/tron-deployment/internal/tronaddr"
)

// Config is txgen's runtime configuration, loaded from a JSON file.
//
// All paths in the file are resolved relative to the working
// directory of the running binary, not the config file's location.
type Config struct {
	// Node — the TRON HTTP endpoint used for createtransaction calls
	// during generate, and for broadcast / block lookups otherwise.
	Node string `json:"node"`

	// Generate: build signed txs and write them to CSV.
	Generate struct {
		TotalTxCount         int    `json:"totalTxCount"`
		ReceiverAddressCount int    `json:"receiverAddressCount"`
		Concurrency          int    `json:"concurrency"`
		PrivateKey           string `json:"privateKey"`
		OutputDir            string `json:"outputDir"`
		ExpirationMillis     int64  `json:"expirationMillis"`
		TxType               struct {
			Transfer      int `json:"transfer"`
			TransferTRC10 int `json:"transferTrc10"`
			TransferTRC20 int `json:"transferTrc20"`
		} `json:"txType"`
		TRC10ID        string `json:"trc10Id"`
		TRC20Address   string `json:"trc20Address"`
		TransferAmount int64  `json:"transferAmount"`
		TRC20FeeLimit  int64  `json:"trc20FeeLimit"`

		// PQ: when enabled, a `ratio` percent of transactions are signed
		// with a post-quantum scheme and carry a pq_auth_sig instead of the
		// ECDSA signature; the remainder are ECDSA-signed.
		//
		// ML_DSA_44: set scheme="ML_DSA_44" and seed (32-byte hex, 64 chars).
		// The keypair is derived deterministically from the seed.
		//
		// FN_DSA_512 (Falcon-512): set scheme="FN_DSA_512" and provide the
		// privateKey (1281-byte liboqs SK, 2562 hex chars) and publicKey
		// (896-byte h polynomial, 1792 hex chars). Generate a keypair with
		// `txgen keygen` (requires build-txgen-falcon).
		PQ struct {
			Enabled    bool   `json:"enabled"`
			Scheme     string `json:"scheme"`     // "ML_DSA_44" or "FN_DSA_512"
			Seed       string `json:"seed"`       // ML_DSA_44: 32-byte hex (64 hex chars)
			PrivateKey string `json:"privateKey"` // FN_DSA_512: liboqs SK hex (2562 chars)
			PublicKey  string `json:"publicKey"`  // FN_DSA_512: h polynomial hex (1792 chars)
			Ratio      int    `json:"ratio"`      // percent of txs PQ-signed, 1-100 (default 100)
		} `json:"pq"`
	} `json:"generate"`

	// Broadcast: read CSV, fire to node at tpsLimit, write txIDs.
	Broadcast struct {
		InputDir string `json:"inputDir"`
		TpsLimit int    `json:"tpsLimit"`
		Workers  int    `json:"workers"` // concurrent HTTP workers; 0 = auto (tpsLimit/50, min 4, max 256)

		// Transport selects the wire protocol: "http" (default) posts to
		// /wallet/broadcasttransaction on :8090, "grpc" calls the Wallet
		// service's BroadcastTransaction on :50051.
		Transport string `json:"transport"`

		// GRPC configures the grpc transport. Ignored when transport=http.
		//
		// Concurrency is expressed as connections x callsPerConnection
		// rather than as `workers`, because the two axes are not
		// interchangeable over gRPC: one ClientConn multiplexes every call
		// onto a single HTTP/2 connection, and java-tron caps in-flight
		// calls per connection (node.rpc.maxConcurrentCallsPerConnection).
		// Raising `workers` alone therefore stops adding load once the cap
		// is reached; the extra calls queue in the client transport instead.
		GRPC struct {
			// Endpoint is host:port. Default: the host of `node` with port 50051.
			Endpoint string `json:"endpoint"`
			// Connections is the number of independent ClientConns, i.e. the
			// number of HTTP/2 connections the server sees. Default 1.
			Connections int `json:"connections"`
			// CallsPerConnection is how many calls each connection keeps in
			// flight. Default 100, matching java-tron's own cap so the
			// default run sits exactly at the server's limit.
			CallsPerConnection int `json:"callsPerConnection"`
		} `json:"grpc"`

		SaveTxID   bool   `json:"saveTxId"`
		TxIDFile   string `json:"txIdFile"`
		ReportFile string `json:"reportFile"`

		// workersExplicit records whether the config file set `workers`
		// before applyDefaults filled it in. The grpc transport derives
		// `workers` from its own two axes, so a hand-set value there is a
		// contradiction we reject rather than silently override.
		workersExplicit bool
	} `json:"broadcast"`

	// Statistic: post-broadcast TPS calculation across a block range.
	Statistic struct {
		StartBlock int64  `json:"startBlock"`
		EndBlock   int64  `json:"endBlock"`
		OutputFile string `json:"outputFile"`
	} `json:"statistic"`
}

// LoadConfig reads + validates a JSON config file. Defaults are filled
// in for unset numeric fields so a minimal config is still usable.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Node == "" {
		c.Node = "http://127.0.0.1:8090"
	}
	if c.Generate.Concurrency == 0 {
		c.Generate.Concurrency = 8
	}
	if c.Generate.OutputDir == "" {
		c.Generate.OutputDir = "txgen-output"
	}
	if c.Generate.ReceiverAddressCount == 0 {
		c.Generate.ReceiverAddressCount = 1000
	}
	if c.Generate.TransferAmount == 0 {
		c.Generate.TransferAmount = 1
	}
	if c.Generate.ExpirationMillis == 0 {
		c.Generate.ExpirationMillis = defaultExpirationMillis
	}
	if c.Generate.TRC20FeeLimit == 0 {
		c.Generate.TRC20FeeLimit = 100_000_000 // 100 TRX
	}
	if c.Generate.PQ.Enabled {
		if c.Generate.PQ.Scheme == "" {
			c.Generate.PQ.Scheme = SchemeMLDSA44
		}
		// An omitted ratio defaults to 100% PQ; to disable PQ entirely set
		// pq.enabled = false rather than ratio = 0.
		if c.Generate.PQ.Ratio == 0 {
			c.Generate.PQ.Ratio = 100
		}
	}
	if c.Broadcast.InputDir == "" {
		c.Broadcast.InputDir = c.Generate.OutputDir
	}
	if c.Broadcast.TpsLimit == 0 {
		c.Broadcast.TpsLimit = 1000
	}
	if c.Broadcast.Transport == "" {
		c.Broadcast.Transport = TransportHTTP
	}
	// Record this before any default can overwrite it — validate needs to
	// tell "the user asked for N workers" from "we picked N".
	c.Broadcast.workersExplicit = c.Broadcast.Workers > 0
	if c.Broadcast.Transport == TransportGRPC {
		g := &c.Broadcast.GRPC
		if g.Endpoint == "" {
			g.Endpoint = defaultGRPCEndpoint(c.Node)
		}
		if g.Connections == 0 {
			g.Connections = 1
		}
		if g.CallsPerConnection == 0 {
			g.CallsPerConnection = defaultCallsPerConnection
		}
		// One worker per in-flight call slot: the worker pool *is* the
		// concurrency limiter, so "N calls in flight on connection k" is
		// structural rather than something a semaphore has to maintain.
		c.Broadcast.Workers = g.Connections * g.CallsPerConnection
	}
	if c.Broadcast.Workers == 0 {
		c.Broadcast.Workers = c.Broadcast.TpsLimit / 50
		if c.Broadcast.Workers < 4 {
			c.Broadcast.Workers = 4
		}
		if c.Broadcast.Workers > 256 {
			c.Broadcast.Workers = 256
		}
	}
	if c.Broadcast.TxIDFile == "" {
		c.Broadcast.TxIDFile = "broadcast-txid.csv"
	}
	if c.Broadcast.ReportFile == "" {
		c.Broadcast.ReportFile = "broadcast-report.txt"
	}
	if c.Statistic.OutputFile == "" {
		c.Statistic.OutputFile = "tps-statistic.txt"
	}
}

// Transaction expiration bounds, both taken from java-tron.
//
// A node rejects a transaction unless
//
//	headBlockTime < expiration <= headBlockTime + MAXIMUM_TIME_UNTIL_EXPIRATION
//
// (framework/src/main/java/org/tron/core/db/Manager.java, validateCommon;
// Constant.MAXIMUM_TIME_UNTIL_EXPIRATION = 24h).
//
// txgen measures expiration from raw_data.timestamp — the node's clock
// when it built the transaction — while the node checks against the head
// block's timestamp, which trails it by up to one block interval. So an
// expirationMillis *at* the ceiling produces
//
//	rawTimestamp + 24h > headBlockTime + 24h  <=>  rawTimestamp > headBlockTime
//
// which holds for most of every block interval: the transaction is
// rejected as expired the moment it is created, and only starts being
// accepted once the chain produces a block past its creation time. That
// was the shipped default, and it made "generate then broadcast" fail
// while "generate, wait, broadcast" worked.
const (
	// defaultExpirationMillis matches java-tron's own default for
	// transactions it builds (Constant.TRANSACTION_DEFAULT_EXPIRATION_TIME).
	defaultExpirationMillis = 60_000

	// maxExpirationMillis is Constant.MAXIMUM_TIME_UNTIL_EXPIRATION. Values
	// at or above it can never be accepted; values just below it are
	// accepted only once the head block catches up to the creation time.
	maxExpirationMillis = 24 * 60 * 60 * 1_000
)

// maxBroadcastLanes bounds connections x callsPerConnection. Each lane is
// a goroutine blocked on an RPC, so the ceiling is about keeping a typo
// (a stray zero) from turning into a million goroutines.
const maxBroadcastLanes = 65536

// validateBroadcast checks the transport selection and the settings that
// belong to it. It runs for every subcommand.
func (c *Config) validateBroadcast() error {
	g := c.Broadcast.GRPC
	switch c.Broadcast.Transport {
	case TransportHTTP:
		// Catch grpc settings written under the http transport: they would
		// otherwise be silently inert, and the run would look configured.
		if g.Endpoint != "" || g.Connections != 0 || g.CallsPerConnection != 0 {
			return errors.New(`broadcast.grpc.* requires broadcast.transport = "grpc"`)
		}
	case TransportGRPC:
		if c.Broadcast.workersExplicit {
			return errors.New("broadcast.workers does not apply to the grpc transport; " +
				"set broadcast.grpc.connections x broadcast.grpc.callsPerConnection instead")
		}
		if g.Connections < 1 {
			return fmt.Errorf("broadcast.grpc.connections must be > 0, got %d", g.Connections)
		}
		if g.CallsPerConnection < 1 {
			return fmt.Errorf("broadcast.grpc.callsPerConnection must be > 0, got %d", g.CallsPerConnection)
		}
		if g.Connections > maxBroadcastLanes/g.CallsPerConnection {
			return fmt.Errorf("broadcast.grpc.connections x callsPerConnection must be <= %d, got %d x %d",
				maxBroadcastLanes, g.Connections, g.CallsPerConnection)
		}
	default:
		return fmt.Errorf("broadcast.transport %q unsupported (supported: %s, %s)",
			c.Broadcast.Transport, TransportHTTP, TransportGRPC)
	}
	return nil
}

// validate is only meaningful for the `generate` subcommand. Other
// subcommands ignore the `generate` section entirely, so we don't fail
// here for missing fields — runGenerate will surface them with a clear
// error if it actually needs them.
func (c *Config) validate() error {
	// Broadcast settings are validated unconditionally — unlike the
	// generate section below, they are not skipped for other subcommands.
	if err := c.validateBroadcast(); err != nil {
		return err
	}
	tt := c.Generate.TxType
	sum := tt.Transfer + tt.TransferTRC10 + tt.TransferTRC20
	// Skip generate-section validation if all three weights are zero —
	// the user is running a different subcommand and didn't fill it in.
	if sum == 0 {
		return nil
	}
	if sum != 100 {
		return fmt.Errorf("generate.txType weights must sum to 100, got %d", sum)
	}
	if c.Generate.PQ.Enabled {
		switch c.Generate.PQ.Scheme {
		case SchemeMLDSA44:
			if len(c.Generate.PQ.Seed) != pqSeedHexLen {
				return fmt.Errorf("generate.pq.seed must be %d hex chars (32 bytes) for ML_DSA_44", pqSeedHexLen)
			}
		case SchemeFNDSA512:
			if len(c.Generate.PQ.PrivateKey) != falconSKHexLen {
				return fmt.Errorf("generate.pq.privateKey must be %d hex chars (%d bytes) for FN_DSA_512", falconSKHexLen, falconSKLen)
			}
			if len(c.Generate.PQ.PublicKey) != falconHHexLen {
				return fmt.Errorf("generate.pq.publicKey must be %d hex chars (%d bytes h polynomial) for FN_DSA_512", falconHHexLen, falconHLen)
			}
		default:
			return fmt.Errorf("generate.pq.scheme %q unsupported (supported: %s, %s)", c.Generate.PQ.Scheme, SchemeMLDSA44, SchemeFNDSA512)
		}
		if c.Generate.PQ.Ratio < 1 || c.Generate.PQ.Ratio > 100 {
			return fmt.Errorf("generate.pq.ratio must be 1-100, got %d", c.Generate.PQ.Ratio)
		}
	}
	// The ECDSA privateKey is needed whenever some txs are ECDSA-signed:
	// PQ off entirely, or PQ on but signing less than 100% of txs.
	if !c.Generate.PQ.Enabled || c.Generate.PQ.Ratio < 100 {
		if c.Generate.PrivateKey == "" {
			return errors.New("generate.privateKey is required")
		}
		if len(c.Generate.PrivateKey) != tronaddr.PrivateKeyHexLen {
			return fmt.Errorf("generate.privateKey must be %d hex chars", tronaddr.PrivateKeyHexLen)
		}
	}
	if tt.TransferTRC10 > 0 && c.Generate.TRC10ID == "" {
		return errors.New("generate.trc10Id is required when transferTrc10 > 0")
	}
	if tt.TransferTRC20 > 0 && c.Generate.TRC20Address == "" {
		return errors.New("generate.trc20Address is required when transferTrc20 > 0")
	}
	if c.Generate.TotalTxCount <= 0 {
		return errors.New("generate.totalTxCount must be > 0")
	}
	if c.Generate.ReceiverAddressCount <= 0 {
		return errors.New("generate.receiverAddressCount must be > 0")
	}
	if c.Generate.ExpirationMillis < 0 {
		return errors.New("generate.expirationMillis must be >= 0")
	}
	if c.Generate.ExpirationMillis >= maxExpirationMillis {
		return fmt.Errorf("generate.expirationMillis must be < %d (java-tron's "+
			"MAXIMUM_TIME_UNTIL_EXPIRATION); at or above it every transaction is "+
			"rejected with TRANSACTION_EXPIRATION_ERROR, got %d",
			maxExpirationMillis, c.Generate.ExpirationMillis)
	}
	return nil
}
