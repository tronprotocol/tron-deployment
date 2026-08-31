package target

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// LocalTargetNoDial satisfies Target WITHOUT the optional Dialer interface,
// to prove the fallback paths (plain host transport / net.Dialer) still
// work for targets that cannot tunnel. It must NOT embed LocalTarget: the
// embedded *LocalTarget method set would promote DialContext onto it and
// silently re-implement Dialer. Methods are never exercised by the tests
// below (they only dial), so they fail loudly if that ever changes.
type LocalTargetNoDial struct{}

type blockingDialer struct{}

func (blockingDialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type blockingTarget struct{ LocalTargetNoDial }

func (blockingTarget) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return blockingDialer{}.DialContext(ctx, network, addr)
}

var _ Target = (*LocalTargetNoDial)(nil)

func (l *LocalTargetNoDial) Exec(context.Context, string, ...string) ([]byte, error) {
	return nil, fmt.Errorf("LocalTargetNoDial: Exec not implemented")
}
func (l *LocalTargetNoDial) Upload(context.Context, string, string) error {
	return fmt.Errorf("LocalTargetNoDial: Upload not implemented")
}
func (l *LocalTargetNoDial) Download(context.Context, string, string) error {
	return fmt.Errorf("LocalTargetNoDial: Download not implemented")
}
func (l *LocalTargetNoDial) ReadFile(context.Context, string) ([]byte, error) {
	return nil, fmt.Errorf("LocalTargetNoDial: ReadFile not implemented")
}
func (l *LocalTargetNoDial) WriteFile(context.Context, string, []byte, os.FileMode) error {
	return fmt.Errorf("LocalTargetNoDial: WriteFile not implemented")
}
func (l *LocalTargetNoDial) DiskFree(context.Context, string) (uint64, error) {
	return 0, fmt.Errorf("LocalTargetNoDial: DiskFree not implemented")
}
func (l *LocalTargetNoDial) MemTotal(context.Context) (uint64, error) {
	return 0, fmt.Errorf("LocalTargetNoDial: MemTotal not implemented")
}
func (l *LocalTargetNoDial) PutFile(context.Context, string, string) error {
	return fmt.Errorf("LocalTargetNoDial: PutFile not implemented")
}
func (l *LocalTargetNoDial) Sha256IfExists(context.Context, string) (string, error) {
	return "", fmt.Errorf("LocalTargetNoDial: Sha256IfExists not implemented")
}
func (l *LocalTargetNoDial) CommandExists(context.Context, string) bool { return false }
func (l *LocalTargetNoDial) String() string                             { return "local-no-dial" }

// TestEndpointHost pins the ssh-vs-local reporting rule: ssh targets with a
// recorded host report that host; everything else reports loopback.
func TestEndpointHost(t *testing.T) {
	cases := []struct {
		targetType, host, want string
	}{
		{"ssh", "10.0.0.5", "10.0.0.5"},
		{"ssh", "", "127.0.0.1"}, // recorded host missing → fail-safe loopback
		{"local", "", "127.0.0.1"},
		{"local", "ignored", "127.0.0.1"},
		{"", "", "127.0.0.1"},
	}
	for _, tc := range cases {
		if got := EndpointHost(tc.targetType, tc.host); got != tc.want {
			t.Errorf("EndpointHost(%q, %q) = %q, want %q", tc.targetType, tc.host, got, tc.want)
		}
	}
}

// TestLocalTargetDialContext proves the local DialContext reaches a real
// listener — this is what makes target.Get/Post work unchanged for local
// targets after the ssh-tunnel migration.
func TestLocalTargetDialContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	conn, err := NewLocalTarget().DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	conn.Close()
}

// TestHTTPClientFallsBackForNonDialer: a Target that does not implement
// Dialer must get a working default-transport client (plain host dial).
func TestHTTPClientFallsBackForNonDialer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	client := HTTPClient(&LocalTargetNoDial{}, 2*time.Second)
	if _, ok := client.Transport.(*http.Transport); ok {
		t.Fatalf("non-Dialer target must keep the default transport, got a custom one")
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET via fallback client: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestHTTPClientReusesTransportPerTarget(t *testing.T) {
	target := NewLocalTarget()
	first := HTTPClient(target, time.Second)
	second := HTTPClient(target, time.Second)
	if first.Transport != second.Transport {
		t.Fatalf("HTTPClient transports differ: %p vs %p", first.Transport, second.Transport)
	}
	if _, ok := first.Transport.(*http.Transport); !ok {
		t.Fatalf("transport type = %T, want *http.Transport", first.Transport)
	}
}

func TestHTTPClientDoesNotShareTransportAcrossTargetInstances(t *testing.T) {
	firstTarget := NewSSHTarget("same", 22, "user", "")
	secondTarget := NewSSHTarget("same", 22, "user", "")
	first := HTTPClient(firstTarget, time.Second)
	second := HTTPClient(secondTarget, time.Second)
	if first.Transport == second.Transport {
		t.Fatal("distinct target instances must not share a transport")
	}
}

func TestHTTPClientLocalTargetsShareTransport(t *testing.T) {
	if HTTPClient(NewLocalTarget(), time.Second).Transport != HTTPClient(NewLocalTarget(), time.Second).Transport {
		t.Fatal("LocalTarget instances should use shared transport")
	}
}

func TestSSHTargetCloseUnregistersTransport(t *testing.T) {
	target := NewSSHTarget("same", 22, "user", "")
	first := HTTPClient(target, time.Second).Transport
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	second := HTTPClient(target, time.Second).Transport
	if first == second {
		t.Fatal("transport reused after target Close")
	}
}

func TestCloseIdleConnectionsClosesCachedTransports(t *testing.T) {
	CloseIdleConnections()
	if sharedLocalTransport == nil {
		t.Fatal("expected shared local transport")
	}
	CloseIdleConnections()
}

func TestLocalTransportConcurrentCloseIsRaceFree(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = HTTPClient(NewLocalTarget(), time.Second).Transport
		}()
		go func() {
			defer wg.Done()
			CloseIdleConnections()
		}()
	}
	wg.Wait()
}

func TestSSHTargetConnectContextUnreachableReturnsPromptly(t *testing.T) {
	originalDial := sshNetDialContext
	sshNetDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Cleanup(func() { sshNetDialContext = originalDial })
	identity, knownHosts := sshTestFiles(t)
	target := NewSSHTarget("127.0.0.1", 1, "user", identity).WithKnownHosts(knownHosts)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := target.ConnectContext(ctx)
	if err == nil {
		t.Fatal("ConnectContext unexpectedly succeeded")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("ConnectContext took %s, want prompt failure", elapsed)
	}
}

func TestSSHTargetConnectContextCancelDuringHandshake(t *testing.T) {
	identity, knownHosts := sshTestFiles(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			close(accepted)
			defer conn.Close()
			_, _ = io.Copy(io.Discard, conn)
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	target := NewSSHTarget("127.0.0.1", port, "user", identity).WithKnownHosts(knownHosts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- target.ConnectContext(ctx) }()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("test server did not accept TCP connection")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ConnectContext did not return after cancellation")
	}
}

func sshTestFiles(t *testing.T) (string, string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(identity, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHosts, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = public
	return identity, knownHosts
}

// TestDialContextFallback: the free DialContext helper falls back to a plain
// net.Dialer when the target does not implement Dialer.
func TestDialContextFallback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	conn, err := DialContext(context.Background(), &LocalTargetNoDial{}, "tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("DialContext fallback: %v", err)
	}
	conn.Close()
}

func TestSSHTargetDialContextNilClient(t *testing.T) {
	_, err := NewSSHTarget("host", 22, "user", "").DialContext(context.Background(), "tcp", "127.0.0.1:8090")
	if err == nil || !strings.Contains(err.Error(), "ssh not connected") {
		t.Fatalf("DialContext nil client error = %v", err)
	}
}

func TestSSHTargetDialContextRejectsNonLoopback(t *testing.T) {
	_, err := NewSSHTarget("host", 22, "user", "").DialContext(context.Background(), "tcp", "10.0.0.5:8090")
	if err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("DialContext non-loopback error = %v", err)
	}
}

func TestDialContextDialerHonorsTimeout(t *testing.T) {
	start := time.Now()
	_, err := DialContext(context.Background(), &blockingTarget{}, "tcp", "127.0.0.1:8090", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("DialContext timeout error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("DialContext took %s, want near 50ms", elapsed)
	}
}

// TestGetNon2xxIsError pins the status-code contract: a non-2xx response is
// an error carrying a body snippet (curl -fsS semantics the probes replaced).
func TestGetNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "boom")
	}))
	defer srv.Close()

	_, err := Get(context.Background(), NewLocalTarget(), srv.URL, 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "http 500") {
		t.Fatalf("Get on 500 = %v, want http 500 error", err)
	}
}

// TestPostSendsJSONBody verifies Post sets the JSON content type and ships
// the body through (getblockbynum-style probes depend on it).
func TestPostSendsJSONBody(t *testing.T) {
	var gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	if _, err := Post(context.Background(), NewLocalTarget(), srv.URL, []byte(`{"num":0}`), 2*time.Second); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
	if gotBody != `{"num":0}` {
		t.Errorf("body = %q, want %q", gotBody, `{"num":0}`)
	}
}
