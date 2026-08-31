package target

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// LocalTarget executes commands and file operations on the local machine.
type LocalTarget struct{}

func (t *LocalTarget) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

// NewLocalTarget creates a new LocalTarget.
func NewLocalTarget() *LocalTarget {
	return &LocalTarget{}
}

func (t *LocalTarget) Exec(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, cmd, args...)
	out, err := c.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("exec %s: %w: %s", cmd, err, string(out))
	}
	return out, nil
}

func (t *LocalTarget) StreamExec(ctx context.Context, cmd string, args ...string) (io.ReadCloser, error) {
	c := exec.CommandContext(ctx, cmd, args...)
	stdout, err := c.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := c.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := c.Start(); err != nil {
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
		close(streamDone)
		_ = pw.Close()
	}()
	return &streamReader{ReadCloser: pr, terminate: func() {
		if c.Process != nil {
			_ = c.Process.Kill()
		}
	}, streamDone: streamDone, wait: func() error {
		waitErr := c.Wait()
		wg.Wait()
		return waitErr
	}}, nil
}

func (t *LocalTarget) Upload(_ context.Context, localPath, remotePath string) error {
	// Local target: just copy the file
	return copyFile(localPath, remotePath)
}

// PutFile is the local-target sibling of SSHTarget.PutFile: same-fs
// copy with atomic install (write `<remotePath>.tmp`, rename).
// localPath == remotePath is a no-op — the Phase 4 build flow uses
// this when the artifact already lives at the cache path and apply
// just needs to declare "this IS where the artifact is on the
// target".
func (t *LocalTarget) PutFile(_ context.Context, localPath, remotePath string) error {
	if localPath == remotePath {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(remotePath), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", remotePath, err)
	}
	src, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", localPath, err)
	}
	defer src.Close()

	tmp := remotePath + ".tmp"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("copy: %w", err)
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, remotePath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s → %s: %w", tmp, remotePath, err)
	}
	return nil
}

// Sha256IfExists hashes a local file; missing file = empty string.
// Same signature as SSHTarget.Sha256IfExists so apply can branch on
// "same hash, skip transfer" uniformly regardless of target type.
func (t *LocalTarget) Sha256IfExists(_ context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CommandExists checks the local PATH. Mirrors SSHTarget so apply's
// preflight code can ask "is sha256sum present" uniformly across
// target types.
func (t *LocalTarget) CommandExists(_ context.Context, name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// Compile-time assertion that LocalTarget implements Target. Future
// additions to the Target interface fail loudly here instead of at
// the first use site somewhere else in the codebase. Mirrors the
// equivalent assertion at the bottom of ssh.go.
var _ Target = (*LocalTarget)(nil)

func (t *LocalTarget) Download(_ context.Context, remotePath, localPath string) error {
	data, err := os.ReadFile(remotePath)
	if err != nil {
		return err
	}
	return writeFile(localPath, data, 0o600)
}

func (t *LocalTarget) ReadFile(_ context.Context, path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (t *LocalTarget) WriteFile(_ context.Context, path string, data []byte, perm os.FileMode) error {
	return writeFile(path, data, perm)
}

func (t *LocalTarget) Chmod(_ context.Context, path string, perm os.FileMode) error {
	return os.Chmod(path, perm)
}

func (t *LocalTarget) DiskFree(ctx context.Context, path string) (uint64, error) {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		cmd = "df"
		args = []string{"-k", path}
	default: // linux
		cmd = "df"
		args = []string{"--output=avail", "-k", path}
	}

	out, err := t.Exec(ctx, cmd, args...)
	if err != nil {
		return 0, err
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("unexpected df output")
	}

	// Parse the available KB from the last line
	fields := strings.Fields(lines[len(lines)-1])
	var availField string
	if runtime.GOOS == "darwin" {
		if len(fields) < 4 {
			return 0, fmt.Errorf("unexpected df output format")
		}
		availField = fields[3] // Available column on macOS
	} else {
		availField = fields[0]
	}

	kb, err := strconv.ParseUint(strings.TrimSpace(availField), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse disk free: %w", err)
	}

	return kb * 1024, nil // Convert KB to bytes
}

func (t *LocalTarget) MemTotal(ctx context.Context) (uint64, error) {
	switch runtime.GOOS {
	case "darwin":
		out, err := t.Exec(ctx, "sysctl", "-n", "hw.memsize")
		if err != nil {
			return 0, err
		}
		return strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	default: // linux
		data, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0, fmt.Errorf("read meminfo: %w", err)
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) < 2 {
					continue
				}
				kb, err := strconv.ParseUint(fields[1], 10, 64)
				if err != nil {
					return 0, fmt.Errorf("parse memtotal: %w", err)
				}
				return kb * 1024, nil
			}
		}
		return 0, fmt.Errorf("MemTotal not found in /proc/meminfo")
	}
}

func (t *LocalTarget) String() string {
	return "local"
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// writeFile enforces permissions before truncating an existing file, so
// sensitive contents are never written while the old mode remains active.
func writeFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// WriteLocalFile securely writes a local destination, enforcing permissions
// before truncating existing contents. Used for downloaded files too.
func WriteLocalFile(path string, data []byte, perm os.FileMode) error {
	return writeFile(path, data, perm)
}
