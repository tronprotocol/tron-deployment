package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStreamCSVContextStopsOnCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.csv")
	if err := os.WriteFile(path, []byte("txid,json\na,{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := streamCSVContext(ctx, path, make(chan [2]string))
	if err != context.Canceled {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestCSVProducerRunningCancellationConverges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.csv")
	var b strings.Builder
	b.WriteString("txid,json\n")
	for i := 0; i < 2000; i++ {
		b.WriteString("id,{}\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	work := make(chan [2]string, 1)
	work <- [2]string{"prefill", "{}"}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = streamCSVContext(ctx, path, work) }()
	// The producer has a full output channel and is blocked in its send.
	time.Sleep(10 * time.Millisecond)
	cancel()
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("producer did not converge after cancellation")
	}
}
