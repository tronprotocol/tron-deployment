package apply

import (
	"context"
	"testing"
	"time"
)

func TestWaitForReadyJarUsesTargetHTTP(t *testing.T) {
	tgt, port := newHTTPStatusTarget(t, func(path string, _ []byte) (string, bool) {
		if path != "/wallet/getnowblock" {
			t.Errorf("path = %q, want getnowblock", path)
		}
		return `{"block_header":{"raw_data":{"number":1}}}`, false
	})

	if err := WaitForReady(context.Background(), tgt, "jar-node", "jar", port, time.Second); err != nil {
		t.Fatalf("WaitForReady jar: %v", err)
	}
}
