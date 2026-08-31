package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tronprotocol/tron-deployment/tools/common/broadcast"
)

func TestRunCancellationDoesNotAdvanceCursor(t *testing.T) {
	var (
		mu     sync.Mutex
		cancel context.CancelFunc
		calls  int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/wallet/getblockbynum" {
			_, _ = w.Write([]byte(`{"block_header":{"raw_data":{"number":100}},"transactions":[{"txID":"tx-1","raw_data":{"contract":[{"type":"TransferContract"}]}}]}`))
			return
		}
		if req.URL.Path == "/wallet/broadcasttransaction" {
			mu.Lock()
			calls++
			if cancel != nil {
				cancel()
				cancel = nil
			}
			mu.Unlock()
			_, _ = w.Write([]byte(`{"result":true}`))
			return
		}
		http.NotFound(w, req)
	}))
	defer server.Close()

	ctx, stop := context.WithCancel(context.Background())
	cancel = stop
	defer stop()
	stateDir := t.TempDir()
	statePath := filepath.Join(stateDir, "state.json")
	failLog := filepath.Join(stateDir, "failures.jsonl")
	skipLog := filepath.Join(stateDir, "skips.jsonl")
	fail, err := openJsonlLogger(failLog)
	if err != nil {
		t.Fatal(err)
	}
	defer fail.Close()
	skip, err := openJsonlLogger(skipLog)
	if err != nil {
		t.Fatal(err)
	}
	defer skip.Close()

	r := &Replayer{
		cfg: Config{
			TrongridURL:   server.URL,
			TrongridQPS:   1000,
			PrivateNode:   server.URL,
			Start:         100,
			End:           100,
			StateFile:     statePath,
			FailLog:       failLog,
			SkipLog:       skipLog,
			TpsMultiplier: 100,
			IncludeAll:    true,
		},
		trongrid:  newTronGridClient(server.URL, "", 1000),
		private:   newBroadcastClientForTest(server.URL),
		state:     ReplayState{LastMainnetBlock: 99},
		skipTypes: map[string]struct{}{},
		failLog:   fail,
		skipLog:   skip,
	}

	err = r.Run(ctx)
	if err == nil {
		t.Fatal("Run succeeded after cancellation")
	}
	got := loadState(statePath)
	if got.LastMainnetBlock != 99 {
		t.Fatalf("last_mainnet_block = %d, want 99", got.LastMainnetBlock)
	}
	if calls != 1 {
		t.Fatalf("broadcast calls = %d, want 1", calls)
	}
}

func TestRunBroadcastFailureDoesNotAdvanceCursor(t *testing.T) {
	var broadcasts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/wallet/getblockbynum":
			_, _ = w.Write([]byte(`{"block_header":{"raw_data":{"number":100}},"transactions":[{"txID":"tx-1","raw_data":{"contract":[{"type":"TransferContract"}]}},{"txID":"tx-2","raw_data":{"contract":[{"type":"TransferContract"}]}}]}`))
		case "/wallet/broadcasttransaction":
			broadcasts++
			if broadcasts == 1 {
				_, _ = w.Write([]byte(`{"result":true}`))
			} else if broadcasts == 2 {
				_, _ = w.Write([]byte(`{"result":false,"code":"FAIL","message":"failed"}`))
			} else {
				_, _ = w.Write([]byte(`{"result":true}`))
			}
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	stateDir := t.TempDir()
	statePath := filepath.Join(stateDir, "state.json")
	failLog := filepath.Join(stateDir, "failures.jsonl")
	skipLog := filepath.Join(stateDir, "skips.jsonl")
	fail, err := openJsonlLogger(failLog)
	if err != nil {
		t.Fatal(err)
	}
	defer fail.Close()
	skip, err := openJsonlLogger(skipLog)
	if err != nil {
		t.Fatal(err)
	}
	defer skip.Close()
	r := &Replayer{
		cfg:      Config{TrongridURL: server.URL, TrongridQPS: 1000, PrivateNode: server.URL, Start: 100, End: 100, StateFile: statePath, FailLog: failLog, SkipLog: skipLog, TpsMultiplier: 100, IncludeAll: true},
		trongrid: newTronGridClient(server.URL, "", 1000), private: newBroadcastClientForTest(server.URL),
		state: ReplayState{LastMainnetBlock: 99}, skipTypes: map[string]struct{}{}, failLog: fail, skipLog: skip,
	}
	if err := r.Run(context.Background()); err == nil {
		t.Fatal("Run succeeded after a broadcast failure")
	}
	if got := loadState(statePath).LastMainnetBlock; got != 99 {
		t.Fatalf("last_mainnet_block = %d, want 99", got)
	}
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("retry Run: %v", err)
	}
	got := loadState(statePath)
	if got.LastMainnetBlock != 100 || got.InProgressTxIndex != 0 {
		t.Fatalf("retry state = %+v, want block 100 and reset tx index", got)
	}
	// Re-broadcasting already-landed txs on retry is expected; see TODOS.md AUD-009.
	if broadcasts != 4 {
		t.Fatalf("broadcasts after retry = %d, want 4 (successful tx replayed)", broadcasts)
	}
}

func TestRunFetchFailureDoesNotAdvanceCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/wallet/getblockbynum" {
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
			return
		}
		http.NotFound(w, req)
	}))
	defer server.Close()

	stateDir := t.TempDir()
	statePath := filepath.Join(stateDir, "state.json")
	failLog := filepath.Join(stateDir, "failures.jsonl")
	skipLog := filepath.Join(stateDir, "skips.jsonl")
	fail, err := openJsonlLogger(failLog)
	if err != nil {
		t.Fatal(err)
	}
	defer fail.Close()
	skip, err := openJsonlLogger(skipLog)
	if err != nil {
		t.Fatal(err)
	}
	defer skip.Close()
	r := &Replayer{
		cfg:      Config{TrongridURL: server.URL, TrongridQPS: 1000, PrivateNode: server.URL, Start: 100, End: 100, StateFile: statePath, FailLog: failLog, SkipLog: skipLog, TpsMultiplier: 100, IncludeAll: true},
		trongrid: newTronGridClient(server.URL, "", 1000), private: newBroadcastClientForTest(server.URL),
		state: ReplayState{LastMainnetBlock: 99}, skipTypes: map[string]struct{}{}, failLog: fail, skipLog: skip,
	}
	err = r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "fetch failed") {
		t.Fatalf("error = %v, want fetch failure", err)
	}
	if got := loadState(statePath).LastMainnetBlock; got != 99 {
		t.Fatalf("last_mainnet_block = %d, want 99", got)
	}
}

// The production broadcast client has no test constructor; keep this helper
// local so the test can use the same HTTP implementation without changing the
// shared package API.
func newBroadcastClientForTest(url string) *broadcast.Client {
	return broadcast.NewWithTimeout(url, time.Second)
}
