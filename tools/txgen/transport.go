package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tronprotocol/tron-deployment/tools/common/broadcast"
)

// Transport names accepted in broadcast.transport.
const (
	TransportHTTP = "http"
	TransportGRPC = "grpc"
)

// txTransport hands one signed transaction to a node and reports whether
// the node accepted it into its pending pool.
//
// The `lane` argument is the caller's worker slot, in [0, Lanes()). The
// http transport ignores it; the grpc transport uses it to decide which
// connection the call rides, which is the whole reason the parameter
// exists — see grpc_node.go.
type txTransport interface {
	Broadcast(ctx context.Context, lane int, tx json.RawMessage) (bool, string)

	// Lanes is how many workers the caller should run. It is the
	// transport's concurrency shape, not a suggestion: the grpc transport
	// derives its per-connection in-flight count from the worker pool
	// size, so running a different number of workers would change what
	// the run measures.
	Lanes() int

	// Describe is a one-line summary for the run log.
	Describe() string

	Close() error
}

// httpTransport posts to /wallet/broadcasttransaction. It is the original
// behaviour, unchanged: every worker shares one http.Client, which pools
// connections per host on its own.
type httpTransport struct {
	cli   *broadcast.Client
	lanes int
}

func (t *httpTransport) Broadcast(ctx context.Context, _ int, tx json.RawMessage) (bool, string) {
	return t.cli.Broadcast(ctx, tx)
}

func (t *httpTransport) Lanes() int { return t.lanes }

func (t *httpTransport) Describe() string {
	return fmt.Sprintf("http %s/wallet/broadcasttransaction, %d workers", t.cli.BaseURL(), t.lanes)
}

func (t *httpTransport) Close() error { return nil }

// newTransport builds the transport named by cfg.Broadcast.Transport.
// The returned transport is always Close()-able, including on error paths
// inside the grpc constructor.
func newTransport(cfg *Config) (txTransport, error) {
	switch cfg.Broadcast.Transport {
	case TransportGRPC:
		return newGRPCTransport(cfg)
	case TransportHTTP:
		return &httpTransport{
			cli:   broadcast.New(cfg.Node),
			lanes: cfg.Broadcast.Workers,
		}, nil
	default:
		// Unreachable: validateBroadcast rejects anything else at load time.
		return nil, fmt.Errorf("unsupported broadcast.transport %q", cfg.Broadcast.Transport)
	}
}
