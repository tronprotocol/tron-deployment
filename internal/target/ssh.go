package target

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/tronprotocol/tron-deployment/internal/security"
)

// SSHTarget executes commands and file operations on a remote machine via SSH.
//
// Security hardening:
//   - Commands are validated against an allowlist before execution (see security.ValidateCommand)
//   - Host keys are verified against known_hosts files
//   - Paths passed to shell commands are single-quoted to neutralize metacharacters
//   - Long-running commands respect context cancellation
type SSHTarget struct {
	host           string
	port           int
	user           string
	identityFile   string
	knownHostsFile string // Path to known_hosts; empty uses ~/.ssh/known_hosts
	client         *ssh.Client
	// provisioning widens the command whitelist to the package managers and
	// shell that host preparation needs. Only SetProvisioning sets it, and
	// only `trond bootstrap` calls that.
	provisioning bool
}

var _ interface{ SetProvisioning(bool) } = (*SSHTarget)(nil)

var sshNetDialContext = (&net.Dialer{}).DialContext

func (t *SSHTarget) DialContext(_ context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("ssh direct dial requires host:port address: %w", err)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("ssh direct dial address %q is not loopback", addr)
		}
	}
	if t.client == nil {
		return nil, fmt.Errorf("ssh not connected")
	}
	return t.client.Dial(network, addr)
}

// NewSSHTarget creates a new SSHTarget. Call Connect() before use.
func NewSSHTarget(host string, port int, user, identityFile string) *SSHTarget {
	if port == 0 {
		port = 22
	}
	return &SSHTarget{
		host:         host,
		port:         port,
		user:         user,
		identityFile: identityFile,
	}
}

// WithKnownHosts overrides the known_hosts file path used for host key verification.
func (t *SSHTarget) WithKnownHosts(path string) *SSHTarget {
	t.knownHostsFile = path
	return t
}

// Connect establishes the SSH connection, verifying the server's host key,
// with a two-minute deadline.
// It bounds TCP connect and SSH handshake at two minutes.
func (t *SSHTarget) Connect() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return t.ConnectContext(ctx)
}

// ConnectContext establishes SSH while honoring cancellation during both TCP
// connect and SSH handshake. Callers with a bounded operation should use this
// method; Connect supplies a two-minute context.
func (t *SSHTarget) ConnectContext(ctx context.Context) error {
	identityPath, err := expandHome(t.identityFile)
	if err != nil {
		return fmt.Errorf("expand identity path: %w", err)
	}
	keyData, err := os.ReadFile(identityPath)
	if err != nil {
		return fmt.Errorf("read identity file %s: %w", identityPath, err)
	}

	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return fmt.Errorf("parse identity file: %w", err)
	}

	hostKeyCallback, err := t.hostKeyCallback()
	if err != nil {
		return fmt.Errorf("load known_hosts: %w", err)
	}

	config := &ssh.ClientConfig{
		User: t.user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: hostKeyCallback,
	}

	addr := net.JoinHostPort(t.host, strconv.Itoa(t.port))
	conn, err := sshNetDialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("ssh connect to %s: %w", addr, err)
	}
	type result struct {
		client *ssh.Client
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		cc, chans, reqs, handshakeErr := ssh.NewClientConn(conn, addr, config)
		if handshakeErr != nil {
			resultCh <- result{err: handshakeErr}
			return
		}
		client := ssh.NewClient(cc, chans, reqs)
		if ctx.Err() != nil {
			_ = client.Close()
			resultCh <- result{err: ctx.Err()}
			return
		}
		resultCh <- result{client: client}
	}()
	select {
	case result := <-resultCh:
		if result.err != nil {
			_ = conn.Close()
			return fmt.Errorf("ssh connect to %s: %w", addr, result.err)
		}
		t.client = result.client
	case <-ctx.Done():
		_ = conn.Close()
		// Handshake can finish concurrently with cancellation. Drain the result
		// and close a client created after the cancellation branch won.
		go func() {
			result := <-resultCh
			if result.client != nil {
				_ = result.client.Close()
			}
		}()
		return fmt.Errorf("ssh connect to %s: %w", addr, ctx.Err())
	}
	return nil
}

// IsRemote marks targets whose artifacts must be downloaded by the local
// process and uploaded over the target connection.
func (t *SSHTarget) IsRemote() bool { return true }

// SetProvisioning puts the target in provisioning mode, allowing the
// package-manager and shell commands host preparation needs. It is
// deliberately not part of the target.Target interface: no lifecycle code
// path and no `trond exec` invocation can reach it.
func (t *SSHTarget) SetProvisioning(on bool) { t.provisioning = on }

// hostKeyCallback returns a verifier backed by known_hosts. Falls back to
// ~/.ssh/known_hosts when no explicit file is configured.
//
// If TROND_SSH_ACCEPT_NEW_HOSTS=1 is set, an unknown host is accepted and
// appended to known_hosts. This is opt-in — by default an unknown host is
// rejected to prevent MITM.
func (t *SSHTarget) hostKeyCallback() (ssh.HostKeyCallback, error) {
	path := t.knownHostsFile
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("find home dir: %w", err)
		}
		path = filepath.Join(home, ".ssh", "known_hosts")
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Create empty known_hosts so knownhosts.New doesn't fail
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		f.Close()
	}

	base, err := knownhosts.New(path)
	if err != nil {
		return nil, err
	}

	acceptNew := os.Getenv("TROND_SSH_ACCEPT_NEW_HOSTS") == "1"

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		cbErr := base(hostname, remote, key)
		if cbErr == nil {
			return nil
		}

		// Distinguish three cases the knownhosts package can surface:
		//   1) *KeyError with len(Want) > 0 — the offered key MISMATCHES a
		//      pinned key. This is a likely MITM and must NEVER be auto-trusted,
		//      regardless of TROND_SSH_ACCEPT_NEW_HOSTS.
		//   2) *KeyError with len(Want) == 0 — host has not been seen before.
		//      Eligible for opt-in TOFU.
		//   3) Any other error (parse error, I/O failure on known_hosts, …) —
		//      reject. We will not auto-trust through an opaque failure.
		var keyErr *knownhosts.KeyError
		if !errors.As(cbErr, &keyErr) {
			return fmt.Errorf("host key check for %s: %w", hostname, cbErr)
		}
		if len(keyErr.Want) > 0 {
			return fmt.Errorf("host key MISMATCH for %s — possible MITM, refusing to trust: %w", hostname, cbErr)
		}
		if !acceptNew {
			return fmt.Errorf("host key verification failed for %s: %w (set TROND_SSH_ACCEPT_NEW_HOSTS=1 to trust new hosts)", hostname, cbErr)
		}

		// Opt-in TOFU: append the new host's key to known_hosts.
		f, openErr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if openErr != nil {
			return fmt.Errorf("append known_hosts: %w", openErr)
		}
		defer f.Close()
		line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
		if _, err := fmt.Fprintln(f, line); err != nil {
			return fmt.Errorf("write known_hosts: %w", err)
		}
		return nil
	}, nil
}

// Close closes the SSH connection.
func (t *SSHTarget) Close() error {
	unregisterTransport(t)
	if t.client != nil {
		err := t.client.Close()
		t.client = nil
		return err
	}
	return nil
}

// Exec runs a command on the remote host. The command name is validated
// against the SSH allowlist (see internal/security). Context cancellation
// is honored by sending a signal and closing the session.
func (t *SSHTarget) Exec(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	if t.client == nil {
		return nil, fmt.Errorf("ssh not connected")
	}

	validate := security.ValidateCommand
	if t.provisioning {
		validate = security.ValidateProvisioningCommand
	}
	if err := validate(cmd); err != nil {
		return nil, err
	}

	session, err := t.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("new ssh session: %w", err)
	}
	defer session.Close()

	fullCmd := quoteArgs(cmd, args)

	var combined bytes.Buffer
	session.Stdout = &combined
	session.Stderr = &combined

	done := make(chan error, 1)
	go func() { done <- session.Run(fullCmd) }()

	select {
	case runErr := <-done:
		if runErr != nil {
			return combined.Bytes(), fmt.Errorf("ssh exec %q: %w: %s", fullCmd, runErr, combined.String())
		}
		return combined.Bytes(), nil
	case <-ctx.Done():
		// Best-effort signal; some sshd configs ignore SIGTERM over the protocol.
		_ = session.Signal(ssh.SIGTERM)
		_ = session.Close()
		return combined.Bytes(), ctx.Err()
	}
}

// StreamExec starts an allow-listed remote command without buffering output.
func (t *SSHTarget) StreamExec(ctx context.Context, cmd string, args ...string) (io.ReadCloser, error) {
	if t.client == nil {
		return nil, fmt.Errorf("ssh not connected")
	}
	validate := security.ValidateCommand
	if t.provisioning {
		validate = security.ValidateProvisioningCommand
	}
	if err := validate(cmd); err != nil {
		return nil, err
	}
	session, err := t.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("new ssh session: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		session.Close()
		return nil, err
	}
	if err := session.Start(quoteArgs(cmd, args)); err != nil {
		session.Close()
		return nil, err
	}
	pr, pw := io.Pipe()
	var wg sync.WaitGroup
	wg.Add(2)
	copyStream := func(src io.Reader) { defer wg.Done(); _, _ = io.Copy(pw, src) }
	go copyStream(stdout)
	go copyStream(stderr)
	streamDone := make(chan struct{})
	go func() {
		wg.Wait()
		// Close streamDone before the pipe: runLogs may return from io.Copy
		// and call Close() the moment pw closes; streamDone must already be
		// closed so Close() classifies this as a natural EOF (no terminate).
		close(streamDone)
		_ = pw.Close()
	}()
	var terminateOnce sync.Once
	terminate := func() { terminateOnce.Do(func() { _ = session.Signal(ssh.SIGTERM); _ = session.Close() }) }
	processDone := make(chan error, 1)
	go func() { processDone <- session.Wait() }()
	watchStop, watchDone := watchSSHStreamContext(ctx, terminate)
	return &sshStreamReader{ReadCloser: pr, watchStop: watchStop, watchDone: watchDone, terminate: terminate, processDone: processDone, streamDone: streamDone, wait: func() error {
		waitErr := <-processDone
		wg.Wait()
		_ = session.Close()
		return waitErr
	}}, nil
}

func watchSSHStreamContext(ctx context.Context, stopRemote func()) (chan struct{}, chan struct{}) {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			stopRemote()
		case <-stop:
		}
	}()
	return stop, done
}

type sshStreamReader struct {
	io.ReadCloser
	wait        func() error
	terminate   func()
	watchStop   chan struct{}
	watchDone   chan struct{}
	processDone chan error
	streamDone  chan struct{}
	closeOnce   sync.Once
	closeErr    error
}

func (r *sshStreamReader) Close() error {
	r.closeOnce.Do(func() {
		close(r.watchStop)
		<-r.watchDone
		var waitErr error
		switch {
		case r.processDone != nil && r.streamDone != nil:
			select {
			case <-r.streamDone:
				waitErr = <-r.processDone
			default:
				r.terminate()
				waitErr = <-r.processDone
			}
		case r.processDone != nil:
			select {
			case waitErr = <-r.processDone:
			default:
				r.terminate()
				waitErr = <-r.processDone
			}
		default:
			r.terminate()
			waitErr = r.wait()
		}
		err := r.ReadCloser.Close()
		if err != nil {
			r.closeErr = err
		} else {
			r.closeErr = waitErr
		}
	})
	return r.closeErr
}

func (t *SSHTarget) Upload(ctx context.Context, localPath, remotePath string) error {
	if t.client == nil {
		return fmt.Errorf("ssh not connected")
	}

	localData, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("read local file: %w", err)
	}

	return t.writeRemoteFile(ctx, remotePath, localData, 0644)
}

// PutFile streams a large local file to a remote path WITHOUT loading
// the whole file into trond's heap (vs Upload which slurps everything).
// Designed for the build pipeline's SSH-target path where the JAR is
// ~200 MB and the deploy runs on a fairly constrained dev workstation.
//
// Atomicity: data lands at `remotePath.tmp` first, then `mv` renames
// to the final path. A cancelled run via ctx (e.g. SIGINT during the
// scp window) leaves only the .tmp (best-effort cleanup on the host
// side handles that). The remote never observes a half-written JAR
// being executed by systemd.
//
// FR-009 / FR-016 (Phase 4): SSH target build → scp → atomic install.
func (t *SSHTarget) PutFile(ctx context.Context, localPath, remotePath string) error {
	if t.client == nil {
		return fmt.Errorf("ssh not connected")
	}
	if err := security.ValidateCommand("tee"); err != nil {
		return err
	}
	if err := security.ValidateCommand("mv"); err != nil {
		return err
	}

	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open local %s: %w", localPath, err)
	}
	defer f.Close()

	session, err := t.client.NewSession()
	if err != nil {
		return fmt.Errorf("new ssh session: %w", err)
	}
	defer session.Close()

	session.Stdin = f

	tmpPath := remotePath + ".tmp"
	quotedTmp := shellQuote(tmpPath)
	quotedFinal := shellQuote(remotePath)
	quotedDir := shellQuote(filepath.Dir(remotePath))
	// mkdir parent → tee bytes into .tmp → chmod 0644 → atomic mv.
	// `set -e` inside the bash invocation so a failed step aborts
	// the chain before mv promotes a partial file.
	cmd := fmt.Sprintf(
		"set -e; mkdir -p %s; tee %s > /dev/null; chmod 0644 %s; mv %s %s",
		quotedDir, quotedTmp, quotedTmp, quotedTmp, quotedFinal,
	)

	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()

	select {
	case runErr := <-done:
		if runErr != nil {
			t.cleanupTmp(tmpPath)
			return fmt.Errorf("put remote file %s: %w", remotePath, runErr)
		}
		return nil
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		_ = session.Close()
		t.cleanupTmp(tmpPath)
		return ctx.Err()
	}
}

// cleanupTmp removes a leftover `.tmp` from a failed PutFile. The
// removal is best-effort — if SSH is already wedged (the typical
// reason PutFile failed in the first place), `rm` will also fail
// and we surface a one-line stderr warning so an operator at least
// knows there's stale state to investigate. Without the warning the
// .tmp file could persist across many failed deploys silently.
//
// Uses a bounded context derived from Background (not the parent ctx,
// which is typically already cancelled when cleanup runs after SIGINT)
// so the cleanup itself can't hang indefinitely on a wedged socket.
func (t *SSHTarget) cleanupTmp(tmpPath string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := t.Exec(ctx, "rm", "-f", tmpPath); err != nil {
		fmt.Fprintf(os.Stderr,
			"warning: failed to clean up partial transfer %s on %s: %v\n",
			tmpPath, t.String(), err)
	}
}

// Sha256IfExists returns the hex sha256 of a remote file, or empty
// string when the remote ran sha256sum and it exited non-zero (file
// missing, sha256sum binary absent, perms blocking the read — all
// reported by the remote shell as an exit code). The caller treats
// empty + nil err as "transfer needed", which is correct in those
// cases: PutFile will either succeed or surface a more specific error.
//
// Transport-level failures (SSH session can't open, connection
// dropped mid-command) DO bubble up as a non-nil err. The caller
// would otherwise waste bandwidth on a doomed PutFile attempt that
// hits the same broken link; surfacing the error lets it bail
// quickly with the actual root cause.
//
// We deliberately skip a `test -f` preflight: a single sha256sum
// call subsumes both "exists" and "compute hash". One round-trip,
// one allowlisted command.
func (t *SSHTarget) Sha256IfExists(ctx context.Context, remotePath string) (string, error) {
	if t.client == nil {
		return "", fmt.Errorf("ssh not connected")
	}
	out, err := t.Exec(ctx, "sha256sum", remotePath)
	if err != nil {
		// Propagate cancellation directly instead of fooling the
		// caller into thinking "no file, scp it" → another SSH
		// round-trip → eventually surfaces cancellation. Faster
		// bail-out when the user hit Ctrl+C.
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", ctx.Err()
		}
		// The remote shell ran the command and it exited non-zero
		// (file missing, sha256sum absent, permission denied, etc.).
		// Treat as "no usable hash" — caller falls through to PutFile.
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			return "", nil
		}
		// Anything else (NewSession failed, EOF on the SSH channel,
		// dial error) is a transport-level problem. Bubbling lets
		// the caller avoid wasting a large transfer on a broken link.
		return "", err
	}
	// `sha256sum <path>` output: `<hex>  <path>\n`.
	parts := strings.Fields(string(out))
	if len(parts) < 1 {
		return "", fmt.Errorf("unexpected sha256sum output: %q", string(out))
	}
	return parts[0], nil
}

// CommandExists reports whether a command is on the remote's PATH.
// Used by preflight (FR-017) to fail-fast before apply when a
// dependency like scp / sha256sum is missing on the target.
//
// Implementation: `which <name>` rather than `command -v`. `command`
// is a shell builtin and isn't on trond's SSH allowlist (each
// allowlist entry adds attack surface — keep narrow); `which` IS
// allowlisted and behaves identically for the existence check we
// care about.
func (t *SSHTarget) CommandExists(ctx context.Context, name string) bool {
	if t.client == nil {
		return false
	}
	_, err := t.Exec(ctx, "which", name)
	return err == nil
}

func (t *SSHTarget) Download(ctx context.Context, remotePath, localPath string) error {
	if t.client == nil {
		return fmt.Errorf("ssh not connected")
	}

	data, err := t.readRemoteFile(ctx, remotePath)
	if err != nil {
		return err
	}

	return writeFile(localPath, data, 0o600)
}

func (t *SSHTarget) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return t.readRemoteFile(ctx, path)
}

func (t *SSHTarget) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	return t.writeRemoteFile(ctx, path, data, perm)
}

func (t *SSHTarget) Chmod(ctx context.Context, path string, perm os.FileMode) error {
	if err := security.ValidateCommand("chmod"); err != nil {
		return err
	}
	_, err := t.Exec(ctx, "chmod", fmt.Sprintf("%o", perm), path)
	return err
}

func (t *SSHTarget) DiskFree(ctx context.Context, path string) (uint64, error) {
	out, err := t.Exec(ctx, "df", "--output=avail", "-k", path)
	if err != nil {
		return 0, err
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("unexpected df output")
	}

	kb, err := strconv.ParseUint(strings.TrimSpace(lines[len(lines)-1]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse disk free: %w", err)
	}

	return kb * 1024, nil
}

func (t *SSHTarget) MemTotal(ctx context.Context) (uint64, error) {
	out, err := t.Exec(ctx, "grep", "MemTotal", "/proc/meminfo")
	if err != nil {
		return 0, err
	}

	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return 0, fmt.Errorf("unexpected meminfo output")
	}

	kb, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse memtotal: %w", err)
	}

	return kb * 1024, nil
}

func (t *SSHTarget) String() string {
	return fmt.Sprintf("ssh://%s@%s:%d", t.user, t.host, t.port)
}

// readRemoteFile reads a file from the remote host using cat over SSH.
// The path is single-quoted to prevent shell interpretation.
func (t *SSHTarget) readRemoteFile(ctx context.Context, path string) ([]byte, error) {
	out, err := t.Exec(ctx, "cat", path)
	if err != nil {
		return nil, fmt.Errorf("read remote file %s: %w", path, err)
	}
	return out, nil
}

// writeRemoteFile writes data to a remote file. The path is single-quoted to
// neutralize shell metacharacters; the data is streamed via stdin so it never
// touches the command line.
func (t *SSHTarget) writeRemoteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	if err := security.ValidateCommand("tee"); err != nil {
		return err
	}

	session, err := t.client.NewSession()
	if err != nil {
		return fmt.Errorf("new ssh session: %w", err)
	}
	defer session.Close()

	session.Stdin = bytes.NewReader(data)

	quotedPath := shellQuote(path)
	quotedDir := shellQuote(filepath.Dir(path))
	cmd := fmt.Sprintf("umask 077 && mkdir -p %s && if test -e %s; then chmod %o %s; fi && tee %s > /dev/null && chmod %o %s",
		quotedDir, quotedPath, perm, quotedPath, quotedPath, perm, quotedPath)

	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()

	select {
	case runErr := <-done:
		if runErr != nil {
			return fmt.Errorf("write remote file %s: %w", path, runErr)
		}
		return nil
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		_ = session.Close()
		return ctx.Err()
	}
}

// quoteArgs joins cmd and args with EVERY token single-quoted so shell
// metacharacters anywhere in the call cannot break out. We quote `cmd`
// too — even though every internal call site today passes a single
// whitelist-validated word, the contract should hold defensively against
// future callers passing `cmd = "docker --tlscert /tmp/x"` or similar.
func quoteArgs(cmd string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(cmd))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// expandHome resolves a leading "~" or "~/" to the user's home directory.
// SSH identity_file is commonly given as "~/.ssh/id_rsa" in intent files —
// os.ReadFile does not interpret ~ so we expand it ourselves.
func expandHome(path string) (string, error) {
	if path == "" || path[0] != '~' {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:]), nil
	}
	// "~user/foo" form is not supported — refuse rather than guess.
	return "", fmt.Errorf("unsupported home expansion: %s", path)
}

// shellQuote returns s wrapped in single quotes with any embedded single
// quotes escaped. Output is safe to interpolate into a POSIX shell command.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	// Replace embedded ' with '\''
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Ensure SSHTarget implements Target.
var _ Target = (*SSHTarget)(nil)

// Ensure io.Closer is implemented.
var _ io.Closer = (*SSHTarget)(nil)
