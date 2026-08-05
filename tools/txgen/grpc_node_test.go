package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	tronpb "github.com/tronprotocol/tron-deployment/internal/tronproto/pb"
)

// sampleRaw builds a TransactionRaw shaped like a real TRX transfer: a
// contract carrying an Any-packed TransferContract, so the round-trip
// guard is exercised through a nested message and not just scalars.
func sampleRaw(t *testing.T) *tronpb.TransactionRaw {
	t.Helper()
	param, err := anypb.New(&tronpb.TransferContract{
		OwnerAddress: mustHex(t, "41e552f6487585c2b58bc2c9bb4492bc1f17132cd0"),
		ToAddress:    mustHex(t, "41d1e7a6bc354106cb410e65ff8b181c600ff14292"),
		Amount:       1,
	})
	if err != nil {
		t.Fatalf("pack contract: %v", err)
	}
	return &tronpb.TransactionRaw{
		RefBlockBytes: mustHex(t, "1234"),
		RefBlockHash:  mustHex(t, "0102030405060708"),
		Expiration:    1_700_000_060_000,
		Timestamp:     1_700_000_000_000,
		Contract: []*tronpb.Transaction_Contract{{
			Type:      tronpb.Transaction_Contract_TransferContract,
			Parameter: param,
		}},
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

// signedRow renders a CSV row the way generate.go does: the node's
// response fields plus a merged-in signature.
func signedRow(t *testing.T, rawHex string, sigs []string, extra map[string]any) json.RawMessage {
	t.Helper()
	obj := map[string]any{
		"txID":         strings.Repeat("ab", 32),
		"raw_data_hex": rawHex,
		"raw_data":     map[string]any{"timestamp": 1_700_000_000_000},
		"signature":    sigs,
		"visible":      false,
	}
	for k, v := range extra {
		obj[k] = v
	}
	row, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	return row
}

func TestDecodeSignedTx_RebuildsTransaction(t *testing.T) {
	raw := sampleRaw(t)
	rawBytes, err := proto.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	sigHex := strings.Repeat("cd", 65)
	row := signedRow(t, hex.EncodeToString(rawBytes), []string{sigHex}, nil)

	tx, err := decodeSignedTx(row)
	if err != nil {
		t.Fatalf("decodeSignedTx: %v", err)
	}

	// The property that matters is not "the struct looks right" but "what
	// goes on the wire is the byte string the signature covers".
	got, err := proto.Marshal(tx.GetRawData())
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if hex.EncodeToString(got) != hex.EncodeToString(rawBytes) {
		t.Errorf("raw_data on the wire differs from raw_data_hex\n got %x\nwant %x", got, rawBytes)
	}
	if len(tx.GetSignature()) != 1 || hex.EncodeToString(tx.GetSignature()[0]) != sigHex {
		t.Errorf("signature = %x, want %s", tx.GetSignature(), sigHex)
	}
}

func TestDecodeSignedTx_RejectsPQAuthSig(t *testing.T) {
	raw := sampleRaw(t)
	rawBytes, _ := proto.Marshal(raw)
	row := signedRow(t, hex.EncodeToString(rawBytes), nil, map[string]any{
		"pq_auth_sig": []map[string]string{{
			"scheme":     "ML_DSA_44",
			"public_key": "00",
			"signature":  "00",
		}},
	})

	// Silently dropping pq_auth_sig would produce a transaction the node
	// rejects for a reason that points nowhere near the transport.
	if _, err := decodeSignedTx(row); err == nil {
		t.Fatal("want an error for a pq-signed transaction, got nil")
	} else if !strings.Contains(err.Error(), "PQ_UNSUPPORTED") {
		t.Errorf("error = %v, want PQ_UNSUPPORTED", err)
	}
}

func TestDecodeSignedTx_AcceptsNullPQAuthSig(t *testing.T) {
	raw := sampleRaw(t)
	rawBytes, _ := proto.Marshal(raw)
	// An explicit JSON null is absence, not a PQ signature.
	row := signedRow(t, hex.EncodeToString(rawBytes), nil, map[string]any{"pq_auth_sig": nil})
	if _, err := decodeSignedTx(row); err != nil {
		t.Fatalf("decodeSignedTx with pq_auth_sig=null: %v", err)
	}
}

// TestDecodeSignedTx_RejectsFieldDrift models the case the round-trip
// guard exists for: a node running a protocol fork whose extra field this
// build has no definition for. Unmarshal parks it in the unknown set and
// Marshal appends it last, so the bytes come back reordered — which would
// change the txID and void the signature.
func TestDecodeSignedTx_RejectsFieldDrift(t *testing.T) {
	var forked []byte
	// A field number this build does not know, written *before* the known
	// fields — the order a fork's own serializer would produce.
	forked = protowire.AppendTag(forked, 200, protowire.VarintType)
	forked = protowire.AppendVarint(forked, 7)
	forked = protowire.AppendTag(forked, 14, protowire.VarintType) // timestamp
	forked = protowire.AppendVarint(forked, 1_700_000_000_000)

	row := signedRow(t, hex.EncodeToString(forked), nil, nil)
	_, err := decodeSignedTx(row)
	if err == nil {
		t.Fatal("want an error when re-encoding reorders raw_data, got nil")
	}
	if !strings.Contains(err.Error(), "RAW_DATA_DRIFT") {
		t.Errorf("error = %v, want RAW_DATA_DRIFT", err)
	}
}

func TestDecodeSignedTx_Errors(t *testing.T) {
	rawBytes, _ := proto.Marshal(sampleRaw(t))
	rawHex := hex.EncodeToString(rawBytes)

	tests := []struct {
		name string
		row  json.RawMessage
		want string
	}{
		{"not json", json.RawMessage(`{`), "DECODE_ERROR"},
		{"no raw_data_hex", signedRow(t, "", nil, nil), "raw_data_hex is missing"},
		{"raw_data_hex not hex", signedRow(t, "zz", nil, nil), "DECODE_ERROR"},
		{"raw_data_hex not a TransactionRaw", signedRow(t, "ffffffff", nil, nil), "DECODE_ERROR"},
		{"signature not hex", signedRow(t, rawHex, []string{"nothex"}, nil), "signature[0]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeSignedTx(tc.row)
			if err == nil {
				t.Fatalf("want an error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestDefaultGRPCEndpoint(t *testing.T) {
	tests := []struct{ node, want string }{
		{"http://127.0.0.1:8090", "127.0.0.1:50051"},
		{"http://10.0.0.7:8090", "10.0.0.7:50051"},
		{"https://node.example.com", "node.example.com:50051"},
		{"http://[::1]:8090", "[::1]:50051"},
		{"", "127.0.0.1:50051"},
		{"not a url", "127.0.0.1:50051"},
	}
	for _, tc := range tests {
		if got := defaultGRPCEndpoint(tc.node); got != tc.want {
			t.Errorf("defaultGRPCEndpoint(%q) = %q, want %q", tc.node, got, tc.want)
		}
	}
}

func TestLaneConn(t *testing.T) {
	// 3 connections x 4 calls each: contiguous blocks of 4 lanes per conn.
	const callsPerConn, nConns = 4, 3
	counts := make([]int, nConns)
	for lane := 0; lane < callsPerConn*nConns; lane++ {
		got := laneConn(lane, callsPerConn, nConns)
		if want := lane / callsPerConn; got != want {
			t.Errorf("laneConn(%d) = %d, want %d", lane, got, want)
		}
		counts[got]++
	}
	for i, c := range counts {
		if c != callsPerConn {
			t.Errorf("connection %d got %d lanes, want %d", i, c, callsPerConn)
		}
	}
	// Out-of-range lanes must still land on a real connection.
	if got := laneConn(callsPerConn*nConns, callsPerConn, nConns); got != 0 {
		t.Errorf("laneConn past the end = %d, want it to wrap to 0", got)
	}
}

// stubWallet implements just BroadcastTransaction; the rest of the ~190
// method Wallet service comes from the generated Unimplemented struct.
type stubWallet struct {
	tronpb.UnimplementedWalletServer

	mu    sync.Mutex
	peers map[string]int
	txs   []*tronpb.Transaction
}

func (s *stubWallet) BroadcastTransaction(ctx context.Context, tx *tronpb.Transaction) (*tronpb.Return, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := peer.FromContext(ctx); ok {
		s.peers[p.Addr.String()]++
	}
	s.txs = append(s.txs, tx)
	return &tronpb.Return{Result: true, Code: tronpb.Return_SUCCESS}, nil
}

func (s *stubWallet) snapshot() (map[string]int, []*tronpb.Transaction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	peers := make(map[string]int, len(s.peers))
	for k, v := range s.peers {
		peers[k] = v
	}
	return peers, append([]*tronpb.Transaction(nil), s.txs...)
}

// startStubWallet serves the stub on a loopback port and returns its
// host:port. A real listener rather than bufconn, because the point of
// the test is how many TCP connections the server sees.
func startStubWallet(t *testing.T) (*stubWallet, string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	stub := &stubWallet{peers: map[string]int{}}
	srv := grpc.NewServer()
	tronpb.RegisterWalletServer(srv, stub)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return stub, lis.Addr().String()
}

func grpcTestConfig(endpoint string, conns, calls int) *Config {
	cfg := &Config{Node: "http://127.0.0.1:8090"}
	cfg.Broadcast.Transport = TransportGRPC
	cfg.Broadcast.GRPC.Endpoint = endpoint
	cfg.Broadcast.GRPC.Connections = conns
	cfg.Broadcast.GRPC.CallsPerConnection = calls
	return cfg
}

// TestGRPCTransport_OneConnectionPerConfiguredConnection is the claim the
// whole feature rests on: `connections: N` must produce N HTTP/2
// connections at the server, because a server-side per-connection call
// cap is only observable if the client controls how many connections
// exist. Counted at the server by remote address.
func TestGRPCTransport_OneConnectionPerConfiguredConnection(t *testing.T) {
	stub, endpoint := startStubWallet(t)
	const conns, calls = 3, 2

	tr, err := newTransport(grpcTestConfig(endpoint, conns, calls))
	if err != nil {
		t.Fatalf("newTransport: %v", err)
	}
	defer tr.Close()

	if got := tr.Lanes(); got != conns*calls {
		t.Errorf("Lanes() = %d, want %d", got, conns*calls)
	}

	rawBytes, _ := proto.Marshal(sampleRaw(t))
	row := signedRow(t, hex.EncodeToString(rawBytes), []string{strings.Repeat("cd", 65)}, nil)
	for lane := 0; lane < tr.Lanes(); lane++ {
		ok, msg := tr.Broadcast(context.Background(), lane, row)
		if !ok {
			t.Fatalf("lane %d: broadcast failed: %s", lane, msg)
		}
	}

	peers, txs := stub.snapshot()
	if len(peers) != conns {
		t.Errorf("server saw %d connections %v, want %d", len(peers), peers, conns)
	}
	for addr, n := range peers {
		if n != calls {
			t.Errorf("connection %s carried %d calls, want %d", addr, n, calls)
		}
	}
	if len(txs) != conns*calls {
		t.Fatalf("server received %d transactions, want %d", len(txs), conns*calls)
	}
	// And the bytes survived the trip intact.
	got, err := proto.Marshal(txs[0].GetRawData())
	if err != nil {
		t.Fatalf("marshal received raw: %v", err)
	}
	if hex.EncodeToString(got) != hex.EncodeToString(rawBytes) {
		t.Errorf("raw_data received = %x, want %x", got, rawBytes)
	}
}

func TestGRPCTransport_DescribeAndWarmupFailure(t *testing.T) {
	stub, endpoint := startStubWallet(t)
	_ = stub
	tr, err := newTransport(grpcTestConfig(endpoint, 2, 50))
	if err != nil {
		t.Fatalf("newTransport: %v", err)
	}
	defer tr.Close()
	want := fmt.Sprintf("grpc %s, 2 connection(s) x 50 calls each = 100 in flight", endpoint)
	if got := tr.Describe(); got != want {
		t.Errorf("Describe() = %q, want %q", got, want)
	}

	// A dead endpoint must fail while the operator is still watching,
	// not as N thousand broadcast failures once the run is under way.
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadAddr := dead.Addr().String()
	if err := dead.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	const budget = 500 * time.Millisecond
	start := time.Now()
	_, err = newGRPCTransportWith(grpcTestConfig(deadAddr, 1, 1), budget)
	if err == nil {
		t.Error("want the constructor to fail against a closed port, got nil")
	} else if !strings.Contains(err.Error(), "not ready") {
		t.Errorf("error = %v, want it to name the unready connection", err)
	}
	// Bounded by the budget, not by the first RPC in the run.
	if elapsed := time.Since(start); elapsed > budget+5*time.Second {
		t.Errorf("warmup took %s, want it bounded by %s", elapsed, budget)
	}
}
