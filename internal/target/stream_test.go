package target

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLocalTargetStreamExecStreamsBeforeCommandExit(t *testing.T) {
	r, err := NewLocalTarget().StreamExec(context.Background(), "sh", "-c", "printf first; sleep 1; printf second")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	buf := make([]byte, 5)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "first" {
		t.Fatalf("first=%q", buf)
	}
}

func TestLocalTargetStreamExecMergesStderr(t *testing.T) {
	r, err := NewLocalTarget().StreamExec(context.Background(), "sh", "-c", "printf out; printf err >&2")
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(r)
	closeErr := r.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read=%v close=%v", readErr, closeErr)
	}
	if got := string(data); !strings.Contains(got, "out") || !strings.Contains(got, "err") {
		t.Fatalf("merged=%q", got)
	}
}

func TestLocalTargetStreamExecNaturalEOFDoesNotKill(t *testing.T) {
	r, err := NewLocalTarget().StreamExec(context.Background(), "sh", "-c", "trap 'printf killed' TERM; printf done")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if string(data) != "done" {
		t.Fatalf("natural EOF output = %q, want done without kill signal", data)
	}
}

func TestLocalTargetStreamExecReturnsExitError(t *testing.T) {
	r, err := NewLocalTarget().StreamExec(context.Background(), "sh", "-c", "printf bad >&2; exit 7")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(r)
	if err := r.Close(); err == nil {
		t.Fatal("expected non-zero exit error")
	}
}

func TestLocalTargetStreamExecContextCancellationConverges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r, err := NewLocalTarget().StreamExec(ctx, "sh", "-c", "while true; do printf x; done")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("stream close did not converge after cancellation")
	}
}

func TestLocalTargetStreamExecCloseKillsRunningCommand(t *testing.T) {
	r, err := NewLocalTarget().StreamExec(context.Background(), "sh", "-c", "sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not kill running command")
	}
}

func TestSSHStreamContextCancellationClosesRemoteSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	stop, done := watchSSHStreamContext(ctx, func() { close(stopped) })
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("remote stop was not requested")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher goroutine did not converge")
	}
	close(stop)
}

func TestSSHStreamReaderCloseStopsRunningRemoteSession(t *testing.T) {
	reader, writer := io.Pipe()
	stopped := make(chan struct{})
	var stopOnce sync.Once
	watchStop, watchDone := watchSSHStreamContext(context.Background(), func() {})
	fakeWait := func() error { stopOnce.Do(func() { close(stopped) }); return nil }
	r := &sshStreamReader{
		ReadCloser: reader,
		watchStop:  watchStop,
		watchDone:  watchDone,
		terminate:  func() { _ = writer.Close(); stopOnce.Do(func() { close(stopped) }) },
		wait:       fakeWait,
	}
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not stop remote session")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("remote stop was not requested")
	}
}

func TestSSHStreamReaderCloseSkipsTerminateAfterNaturalExit(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	watchStop, watchDone := watchSSHStreamContext(context.Background(), func() {})
	processDone := make(chan error, 1)
	streamDone := make(chan struct{})
	close(streamDone)
	terminateCalls := 0
	r := &sshStreamReader{
		ReadCloser:  reader,
		watchStop:   watchStop,
		watchDone:   watchDone,
		processDone: processDone,
		streamDone:  streamDone,
		terminate:   func() { terminateCalls++ },
		wait:        func() error { return nil },
	}
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case <-closed:
		t.Fatal("Close returned before process completion")
	case <-time.After(20 * time.Millisecond):
	}
	if terminateCalls != 0 {
		t.Fatalf("terminate calls=%d, want 0", terminateCalls)
	}
	processDone <- nil
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not converge after process completion")
	}
}
