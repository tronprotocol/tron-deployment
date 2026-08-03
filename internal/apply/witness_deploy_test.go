package apply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

// recordingTarget captures every file the runtime writes so a test can
// assert on the exact bytes that would land on the deployed host.
type recordingTarget struct {
	fakeTarget
	files map[string][]byte
}

func (r *recordingTarget) WriteFile(_ context.Context, path string, data []byte, _ os.FileMode) error {
	if r.files == nil {
		r.files = map[string][]byte{}
	}
	r.files[path] = append([]byte(nil), data...)
	return nil
}

// TestApply_DeploysRealWitnessKeyAndHashesIt is the other half of F3:
// redacting the preview surfaces must not touch what java-tron
// receives. The bytes handed to runtime.Deploy still carry the real
// key, and config_hash is still the SHA-256 of exactly those bytes —
// so idempotency (`apply` → `no_change`) is unaffected.
func TestApply_DeploysRealWitnessKeyAndHashesIt(t *testing.T) {
	const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	t.Setenv("PROBE_SR_KEY", key)

	parsed := &intent.Intent{
		Name:    "witness-node",
		Network: "mainnet",
		Target:  intent.Target{Type: "local", Runtime: "docker"},
		Nodes: []intent.NodeSpec{{
			Type:       "witness",
			Version:    "4.8.1",
			Resources:  intent.Resources{Memory: "8GB"},
			Ports:      intent.PortMapping{HTTP: 8090, GRPC: 50051},
			WitnessKey: &intent.WitnessKey{PrivateKeyEnv: "PROBE_SR_KEY"},
		}},
	}
	store, st := freshStore(t)
	tgt := &recordingTarget{}

	res, err := Apply(context.Background(), Options{
		Intent:         parsed,
		Target:         tgt,
		Store:          store,
		State:          st,
		IntentHash:     "deadbeef",
		DeploymentsDir: t.TempDir(),
		JDKVersion:     17,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var conf []byte
	for path, data := range tgt.files {
		if strings.HasSuffix(path, "witness-node.conf") {
			conf = data
		}
	}
	if conf == nil {
		t.Fatal("no .conf was written to the target")
	}

	if !strings.Contains(string(conf), `localwitness = ["`+key+`"]`) {
		t.Fatal("deployed config lost the real witness key — java-tron would refuse to sign")
	}
	if strings.Contains(string(conf), "<REDACTED") {
		t.Fatal("deployed config carries a redaction placeholder")
	}

	sum := sha256.Sum256(conf)
	if res.ConfigHash != hex.EncodeToString(sum[:]) {
		t.Errorf("config_hash must be the SHA-256 of the deployed bytes\n got: %s\nwant: %s",
			res.ConfigHash, hex.EncodeToString(sum[:]))
	}
}
