package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	tronpb "github.com/tronprotocol/tron-deployment/internal/tronproto/pb"
)

const (
	// defaultGRPCPort is java-tron's FullNode gRPC port (node.rpc.port).
	// The SolidityNode service listens on 50061 and does not accept
	// broadcasts, so it is not a useful default here.
	defaultGRPCPort = "50051"

	// defaultCallsPerConnection matches java-tron's own
	// node.rpc.maxConcurrentCallsPerConnection default. Starting there
	// means the out-of-the-box run sits exactly at the server's ceiling,
	// which is the interesting place to stand.
	defaultCallsPerConnection = 100

	// grpcCallTimeout bounds one BroadcastTransaction call. It matches the
	// http transport's 5s default so the two are comparable.
	//
	// A call that is queued behind the server's max-concurrent-streams
	// limit is *not* rejected — it waits for a stream to free up. So when
	// in-flight calls exceed the server's cap, the symptom is this
	// deadline firing (GRPC_DeadlineExceeded), not a RESOURCE_EXHAUSTED.
	grpcCallTimeout = 5 * time.Second

	// grpcWarmupTimeout bounds how long we wait for every connection to
	// reach READY before the run starts.
	grpcWarmupTimeout = 10 * time.Second
)

// defaultGRPCEndpoint derives host:50051 from the configured HTTP node
// URL, so a config that only sets `node` still points somewhere sensible.
func defaultGRPCEndpoint(nodeURL string) string {
	host := ""
	if u, err := url.Parse(nodeURL); err == nil {
		host = u.Hostname()
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, defaultGRPCPort)
}

// grpcTransport broadcasts through the Wallet service.
//
// It holds N independent ClientConns on purpose. A single ClientConn
// multiplexes every call onto one HTTP/2 connection, so the server's
// per-connection call cap applies to all of them together; opening more
// connections is the only way a client raises that ceiling. Workers are
// partitioned across the conns in fixed blocks of callsPerConn, so
// connection k carries exactly callsPerConn in-flight calls and the
// mapping never shifts mid-run.
type grpcTransport struct {
	endpoint      string
	conns         []*grpc.ClientConn
	clients       []tronpb.WalletClient
	callsPerConn  int
	warmupTimeout time.Duration
}

func newGRPCTransport(cfg *Config) (txTransport, error) {
	return newGRPCTransportWith(cfg, grpcWarmupTimeout)
}

// newGRPCTransportWith takes the warmup budget explicitly so tests can
// exercise the unreachable-endpoint path without waiting out the real one.
func newGRPCTransportWith(cfg *Config, warmupTimeout time.Duration) (txTransport, error) {
	g := cfg.Broadcast.GRPC
	t := &grpcTransport{
		endpoint:      g.Endpoint,
		callsPerConn:  g.CallsPerConnection,
		warmupTimeout: warmupTimeout,
	}
	for i := 0; i < g.Connections; i++ {
		conn, err := grpc.NewClient(g.Endpoint,
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			_ = t.Close()
			return nil, fmt.Errorf("grpc dial %s: %w", g.Endpoint, err)
		}
		t.conns = append(t.conns, conn)
		t.clients = append(t.clients, tronpb.NewWalletClient(conn))
	}
	// grpc.NewClient is lazy. Connect up front so a wrong endpoint fails
	// here instead of arriving as N thousand broadcast failures, and so
	// the run does not measure connection setup as latency.
	if err := t.warmup(); err != nil {
		_ = t.Close()
		return nil, err
	}
	return t, nil
}

// warmup drives every connection to READY, or reports which one did not
// get there.
func (t *grpcTransport) warmup() error {
	ctx, cancel := context.WithTimeout(context.Background(), t.warmupTimeout)
	defer cancel()
	for i, conn := range t.conns {
		conn.Connect()
		for {
			s := conn.GetState()
			if s == connectivity.Ready {
				break
			}
			if !conn.WaitForStateChange(ctx, s) {
				return fmt.Errorf("grpc connection %d to %s not ready after %s (state %s)",
					i, t.endpoint, t.warmupTimeout, s)
			}
		}
	}
	return nil
}

// laneConn maps a worker slot to a connection in fixed blocks: lanes
// 0..callsPerConn-1 ride connection 0, the next block rides connection 1,
// and so on. The modulo only matters if a caller runs more lanes than the
// transport asked for; the mapping stays a total function either way.
func laneConn(lane, callsPerConn, nConns int) int {
	if lane < 0 {
		lane = -lane
	}
	return (lane / callsPerConn) % nConns
}

func (t *grpcTransport) Lanes() int { return len(t.conns) * t.callsPerConn }

func (t *grpcTransport) Describe() string {
	return fmt.Sprintf("grpc %s, %d connection(s) x %d calls each = %d in flight",
		t.endpoint, len(t.conns), t.callsPerConn, t.Lanes())
}

func (t *grpcTransport) Close() error {
	var firstErr error
	for _, conn := range t.conns {
		if err := conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (t *grpcTransport) Broadcast(ctx context.Context, lane int, tx json.RawMessage) (bool, string) {
	pbTx, err := decodeSignedTx(tx)
	if err != nil {
		return false, err.Error()
	}
	cli := t.clients[laneConn(lane, t.callsPerConn, len(t.clients))]

	callCtx, cancel := context.WithTimeout(ctx, grpcCallTimeout)
	defer cancel()
	ret, err := cli.BroadcastTransaction(callCtx, pbTx)
	if err != nil {
		// Report the status code, not just the text: the codes are how the
		// server-side limits announce themselves (ResourceExhausted for a
		// rejected stream, DeadlineExceeded for one that queued too long).
		st, _ := status.FromError(err)
		return false, "GRPC_" + st.Code().String() + ": " + st.Message()
	}
	if ret.GetResult() {
		return true, "OK"
	}
	// Unlike the HTTP endpoint, which renders Return.message as hex through
	// JsonFormat, gRPC delivers the bytes as-is.
	return false, ret.GetCode().String() + ": " + string(ret.GetMessage())
}

// signedTxJSON is the part of a generate-tx*.csv row needed to rebuild the
// protobuf Transaction. The rows are whole node responses with a signature
// merged in, so they carry more than this.
type signedTxJSON struct {
	RawDataHex string          `json:"raw_data_hex"`
	Signature  []string        `json:"signature"`
	PQAuthSig  json.RawMessage `json:"pq_auth_sig"`
}

// decodeSignedTx converts one CSV row into a protobuf Transaction.
//
// raw_data_hex is used rather than raw_data: it is the exact byte string
// the node serialized and the signature covers, so nothing here has to
// reproduce java-tron's JSON-to-protobuf mapping.
func decodeSignedTx(row json.RawMessage) (*tronpb.Transaction, error) {
	var s signedTxJSON
	if err := json.Unmarshal(row, &s); err != nil {
		return nil, fmt.Errorf("DECODE_ERROR: parse signed tx json: %w", err)
	}
	if len(s.PQAuthSig) > 0 && !bytes.Equal(s.PQAuthSig, []byte("null")) {
		// Transaction.pq_auth_sig only exists on the PQ fork; the pinned
		// upstream protocol this package generates from has no such field,
		// so it would be dropped and every tx would fail signature
		// verification at the node. Refuse instead.
		return nil, errors.New("PQ_UNSUPPORTED: transaction carries pq_auth_sig, " +
			"which the pinned upstream protocol has no field for; use broadcast.transport = \"http\"")
	}
	if s.RawDataHex == "" {
		return nil, errors.New("DECODE_ERROR: raw_data_hex is missing")
	}
	rawBytes, err := hex.DecodeString(s.RawDataHex)
	if err != nil {
		return nil, fmt.Errorf("DECODE_ERROR: raw_data_hex: %w", err)
	}
	var raw tronpb.TransactionRaw
	if err := proto.Unmarshal(rawBytes, &raw); err != nil {
		return nil, fmt.Errorf("DECODE_ERROR: raw_data proto: %w", err)
	}

	// The signature and txID cover rawBytes exactly, but what goes on the
	// wire is what grpc re-marshals from `raw`. Those are equal for a
	// message whose every field this package knows about — and unequal if
	// the node speaks a fork with fields we do not have, because unmarshal
	// parks those in the unknown set and marshal appends them at the end
	// rather than in field-number order. That would change the txID and
	// invalidate the signature, so check rather than assume.
	//
	// If this ever fires for a real fork, the fix is to stop round-tripping
	// and put the original bytes on the wire verbatim (build the
	// Transaction wire encoding by hand and hang it off an empty message's
	// unknown fields) — not to delete the check.
	reencoded, err := proto.Marshal(&raw)
	if err != nil {
		return nil, fmt.Errorf("DECODE_ERROR: re-encode raw_data: %w", err)
	}
	if !bytes.Equal(reencoded, rawBytes) {
		return nil, errors.New("RAW_DATA_DRIFT: re-encoding raw_data did not reproduce " +
			"raw_data_hex, so broadcasting it would change the txID and void the signature; " +
			"the node is likely running a protocol fork this build does not carry")
	}

	sigs := make([][]byte, 0, len(s.Signature))
	for i, h := range s.Signature {
		b, err := hex.DecodeString(h)
		if err != nil {
			return nil, fmt.Errorf("DECODE_ERROR: signature[%d]: %w", i, err)
		}
		sigs = append(sigs, b)
	}
	return &tronpb.Transaction{RawData: &raw, Signature: sigs}, nil
}
