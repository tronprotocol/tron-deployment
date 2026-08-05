package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes body to a temp file and loads it, returning whatever
// LoadConfig returned.
func writeConfig(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "txgen.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return LoadConfig(path)
}

func TestConfig_TransportDefaultsToHTTP(t *testing.T) {
	cfg, err := writeConfig(t, `{"node": "http://10.0.0.7:8090"}`)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Broadcast.Transport != TransportHTTP {
		t.Errorf("transport = %q, want %q", cfg.Broadcast.Transport, TransportHTTP)
	}
	// The http path must keep its own worker sizing untouched: tpsLimit/50,
	// floored at 4.
	if cfg.Broadcast.Workers != 20 {
		t.Errorf("workers = %d, want 20 (tpsLimit 1000 / 50)", cfg.Broadcast.Workers)
	}
	// And no grpc defaults leak in when the transport is not grpc.
	if g := cfg.Broadcast.GRPC; g.Endpoint != "" || g.Connections != 0 || g.CallsPerConnection != 0 {
		t.Errorf("grpc settings populated under the http transport: %+v", g)
	}
}

func TestConfig_GRPCDefaults(t *testing.T) {
	cfg, err := writeConfig(t, `{
		"node": "http://10.0.0.7:8090",
		"broadcast": {"transport": "grpc"}
	}`)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	g := cfg.Broadcast.GRPC
	if g.Endpoint != "10.0.0.7:50051" {
		t.Errorf("endpoint = %q, want 10.0.0.7:50051", g.Endpoint)
	}
	if g.Connections != 1 {
		t.Errorf("connections = %d, want 1", g.Connections)
	}
	if g.CallsPerConnection != defaultCallsPerConnection {
		t.Errorf("callsPerConnection = %d, want %d", g.CallsPerConnection, defaultCallsPerConnection)
	}
	// Workers is derived, never the tpsLimit/50 http heuristic.
	if want := g.Connections * g.CallsPerConnection; cfg.Broadcast.Workers != want {
		t.Errorf("workers = %d, want %d (connections x callsPerConnection)", cfg.Broadcast.Workers, want)
	}
}

func TestConfig_GRPCDerivesWorkers(t *testing.T) {
	cfg, err := writeConfig(t, `{
		"broadcast": {
			"transport": "grpc",
			"grpc": {"connections": 4, "callsPerConnection": 25}
		}
	}`)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Broadcast.Workers != 100 {
		t.Errorf("workers = %d, want 100", cfg.Broadcast.Workers)
	}
}

func TestConfig_TransportRejections(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			// `workers` counts HTTP requests in flight; over gRPC the two
			// axes are connections and calls-per-connection. Silently
			// honouring one of them would make the run measure something
			// other than what the config says.
			name: "workers under grpc",
			body: `{"broadcast": {"transport": "grpc", "workers": 64}}`,
			want: "does not apply to the grpc transport",
		},
		{
			name: "grpc settings under http",
			body: `{"broadcast": {"grpc": {"connections": 4}}}`,
			want: `broadcast.grpc.* requires broadcast.transport = "grpc"`,
		},
		{
			name: "grpc endpoint under http",
			body: `{"broadcast": {"grpc": {"endpoint": "1.2.3.4:50051"}}}`,
			want: `broadcast.grpc.* requires broadcast.transport = "grpc"`,
		},
		{
			name: "unknown transport",
			body: `{"broadcast": {"transport": "quic"}}`,
			want: `broadcast.transport "quic" unsupported`,
		},
		{
			name: "negative connections",
			body: `{"broadcast": {"transport": "grpc", "grpc": {"connections": -1}}}`,
			want: "broadcast.grpc.connections must be > 0",
		},
		{
			name: "negative callsPerConnection",
			body: `{"broadcast": {"transport": "grpc", "grpc": {"callsPerConnection": -5}}}`,
			want: "broadcast.grpc.callsPerConnection must be > 0",
		},
		{
			name: "lane count beyond the ceiling",
			body: `{"broadcast": {"transport": "grpc", "grpc": {"connections": 100, "callsPerConnection": 10000}}}`,
			want: "must be <= 65536",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := writeConfig(t, tc.body)
			if err == nil {
				t.Fatalf("want an error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestConfig_ExpirationCeiling pins the boundary against java-tron's
// MAXIMUM_TIME_UNTIL_EXPIRATION. Because txgen measures expiration from
// raw_data.timestamp and the node checks against the head block's
// timestamp, a value *at* the ceiling is rejected by the node for most of
// every block interval — so it has to fail at config load, not silently
// at broadcast.
func TestConfig_ExpirationCeiling(t *testing.T) {
	const gen = `"totalTxCount": 1, "privateKey": "%s", "txType": {"transfer": 100},`
	key := strings.Repeat("a", PrivateKeyHexLen)

	t.Run("default is java-tron's own", func(t *testing.T) {
		cfg, err := writeConfig(t, `{"generate": {`+fmt.Sprintf(gen, key)+` "expirationMillis": 0}}`)
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.Generate.ExpirationMillis != defaultExpirationMillis {
			t.Errorf("expirationMillis = %d, want %d",
				cfg.Generate.ExpirationMillis, defaultExpirationMillis)
		}
	})

	t.Run("at the ceiling is rejected", func(t *testing.T) {
		body := fmt.Sprintf(`{"generate": {%s "expirationMillis": %d}}`,
			fmt.Sprintf(gen, key), maxExpirationMillis)
		_, err := writeConfig(t, body)
		if err == nil {
			t.Fatal("want expirationMillis at the ceiling rejected, got nil")
		}
		if !strings.Contains(err.Error(), "MAXIMUM_TIME_UNTIL_EXPIRATION") {
			t.Errorf("error = %v, want it to name the java-tron limit", err)
		}
	})

	t.Run("just under the ceiling is allowed", func(t *testing.T) {
		body := fmt.Sprintf(`{"generate": {%s "expirationMillis": %d}}`,
			fmt.Sprintf(gen, key), maxExpirationMillis-1)
		if _, err := writeConfig(t, body); err != nil {
			t.Errorf("LoadConfig: %v", err)
		}
	})
}

// TestConfig_BroadcastValidatedForEverySubcommand pins that the transport
// checks are not skipped by validate()'s early return for configs with no
// generate section — `broadcast` is exactly the subcommand that has none.
func TestConfig_BroadcastValidatedForEverySubcommand(t *testing.T) {
	_, err := writeConfig(t, `{"broadcast": {"transport": "nonsense"}}`)
	if err == nil {
		t.Fatal("want the transport rejected even with no generate section, got nil")
	}
}
