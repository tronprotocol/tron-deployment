package snapshot

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/md5" //nolint:gosec // upstream publishes md5 .md5sum sidecars; this is integrity, not security
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// File names inside a TRON snapshot tarball. Both lite and full follow
// the same pattern; only the prefix differs.
const (
	tarballFullName = "FullNode_output-directory.tgz"
	tarballLiteName = "LiteFullNode_output-directory.tgz"
)

// snapshotRoot is the single top-level directory a TRON snapshot tarball
// is packed around: both FullNode_output-directory.tgz and
// LiteFullNode_output-directory.tgz expand to `output-directory/...`.
// That is the layout databasePath() probes, the layout knowledge/snapshots.md
// documents ("The upstream tarball expands as <dest>/output-directory/database/…")
// and the layout the dbfork CI job asserts after a real Nile download.
// extractTar refuses anything outside it — see the comment there.
const snapshotRoot = "output-directory"

// Tarball returns the .tgz filename for a given DBKind.
func Tarball(kind DBKind) string {
	switch kind {
	case DBKindFull:
		return tarballFullName
	case DBKindLite:
		return tarballLiteName
	default:
		return ""
	}
}

// TarballURL returns the full HTTP(S) URL of a snapshot tarball for the
// given source / backup.
func TarballURL(s Source, backup string, kind DBKind) string {
	tb := Tarball(kind)
	if tb == "" {
		return ""
	}
	return s.BaseURL + "/" + backup + "/" + tb
}

// MD5URL returns the URL of the .md5sum sidecar accompanying a tarball.
func MD5URL(s Source, backup string, kind DBKind) string {
	if u := TarballURL(s, backup, kind); u != "" {
		return u + ".md5sum"
	}
	return ""
}

// DownloadOptions configures one snapshot download.
type DownloadOptions struct {
	Source   Source
	Backup   string // e.g. "backup20250115"
	Kind     DBKind // lite | full
	DestDir  string // destination directory; the snapshot expands as <DestDir>/output-directory/...
	Force    bool   // overwrite a non-empty existing database
	NoVerify bool   // skip MD5 verification (for hosts that omit the sidecar)

	// ProgressFn is called periodically with bytes-downloaded out of total.
	// Caller is responsible for rendering. Both args are bytes; total is 0
	// when the server didn't supply a Content-Length.
	ProgressFn func(downloaded, total int64)

	// HTTPClient lets tests stub the network. Defaults to http.DefaultClient.
	HTTPClient *http.Client
}

// PreflightResult summarises what Download is about to do — surfaced via
// `trond snapshot download --dry-run`.
type PreflightResult struct {
	URL             string `json:"url"`
	MD5URL          string `json:"md5_url,omitempty"`
	ExpectedSize    int64  `json:"expected_size_bytes"`
	FreeBytes       uint64 `json:"free_bytes"`
	NeededBytes     uint64 `json:"needed_bytes"`
	UserdataPresent bool   `json:"userdata_present"`
	DatabasePresent bool   `json:"database_present"`
	WouldOverwrite  bool   `json:"would_overwrite"`
	HasMD5Sidecar   bool   `json:"has_md5_sidecar"`
}

// DownloadResult is what Download returns on success.
type DownloadResult struct {
	BytesDownloaded int64         `json:"bytes_downloaded"`
	Duration        time.Duration `json:"-"`
	DurationMs      int64         `json:"duration_ms"`
	MD5Verified     bool          `json:"md5_verified"`
	ExpectedMD5     string        `json:"expected_md5,omitempty"`
	ActualMD5       string        `json:"actual_md5"`
	ExtractedTo     string        `json:"extracted_to"`
	FilesExtracted  int           `json:"files_extracted"`
}

// Preflight inspects the destination filesystem and remote tarball
// without actually downloading. Returns enough context for the caller
// to refuse or warn before kicking off a multi-hour transfer.
func Preflight(ctx context.Context, opts DownloadOptions) (*PreflightResult, error) {
	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	url := TarballURL(opts.Source, opts.Backup, opts.Kind)
	if url == "" {
		return nil, fmt.Errorf("invalid kind %q (need lite or full)", opts.Kind)
	}

	r := &PreflightResult{URL: url}

	// HEAD for size. We tolerate a missing Content-Length (some mirrors
	// don't send it) but report 0 so callers can warn.
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HEAD %s: %w", url, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HEAD %s: HTTP %d", url, resp.StatusCode)
	}
	r.ExpectedSize = resp.ContentLength

	// Probe the .md5sum sidecar with a HEAD; if it's absent we'll fall
	// back to a download-only flow with the verification flag flipped.
	md5URL := MD5URL(opts.Source, opts.Backup, opts.Kind)
	r.MD5URL = md5URL
	if md5Req, err := http.NewRequestWithContext(ctx, http.MethodHead, md5URL, http.NoBody); err == nil {
		if md5Resp, err := client.Do(md5Req); err == nil {
			md5Resp.Body.Close()
			r.HasMD5Sidecar = md5Resp.StatusCode == http.StatusOK
		}
	}

	// Local checks.
	if err := os.MkdirAll(opts.DestDir, 0o755); err != nil {
		return nil, fmt.Errorf("ensure dest dir: %w", err)
	}
	free, err := freeBytes(opts.DestDir)
	if err != nil {
		return nil, fmt.Errorf("statfs %s: %w", opts.DestDir, err)
	}
	r.FreeBytes = free

	// java-tron leveldb compresses surprisingly poorly — uncompressed
	// state typically ends up roughly the same size as the .tgz. We
	// require headroom = ExpectedSize × 2 so the download buffer plus
	// extracted state fit. This is conservative; a 50 GB lite snapshot
	// asks for 100 GB free, which is realistic for a node host anyway.
	if r.ExpectedSize > 0 {
		r.NeededBytes = uint64(r.ExpectedSize) * 2
	}

	// Inspect the destination for existing chain data.
	dbPath, userPath := databasePath(opts.DestDir), userdataPath(opts.DestDir)
	r.DatabasePresent = isNonEmptyDir(dbPath)
	r.UserdataPresent = isNonEmptyDir(userPath)
	r.WouldOverwrite = r.DatabasePresent

	return r, nil
}

// Download streams the snapshot tarball, hashes it on the fly, and
// extracts it directly to disk — no full .tgz is ever written. This
// halves the disk-space requirement compared to download-then-extract
// and shaves the extraction wall-time off the user-perceived runtime.
//
// Pipeline:
//
//	HTTP body → TeeReader(md5) → progress wrapper → gzip → tar → fs
//
// On any error during streaming we abort cleanly: partially extracted
// files stay where they are, the next run with --force will overwrite
// them. We don't try to roll back, because rolling back a 50 GB
// extraction would cost an extra 50 GB of seeks for what is fundamentally
// a discardable cache.
func Download(ctx context.Context, opts DownloadOptions) (*DownloadResult, error) {
	if opts.DestDir == "" {
		return nil, errors.New("dest dir is required")
	}
	if opts.Backup == "" {
		return nil, errors.New("backup is required")
	}

	pre, err := Preflight(ctx, opts)
	if err != nil {
		return nil, err
	}
	// pre.UserdataPresent is intentionally not acted on here — the
	// snapshot tarball's top-level layout never overlaps with userdata/,
	// so extraction leaves witness keys / operator state untouched. The
	// flag is surfaced in PreflightResult so the cmd layer can show a
	// reassuring note in the human-readable output.
	if pre.DatabasePresent && !opts.Force {
		return nil, &OverwriteError{
			Path:    databasePath(opts.DestDir),
			Message: fmt.Sprintf("existing database at %s; pass --force to overwrite", databasePath(opts.DestDir)),
		}
	}
	if pre.NeededBytes > 0 && pre.FreeBytes < pre.NeededBytes {
		return nil, fmt.Errorf("not enough disk space at %s: need ~%s, have %s",
			opts.DestDir, humanBytes(int64(pre.NeededBytes)), humanBytes(int64(pre.FreeBytes)))
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{
			// No overall timeout — a 50 GB transfer can legitimately run
			// for hours. Caller controls timeout via ctx.
			Timeout: 0,
		}
	}

	// Pull the .md5sum first (tiny). If the user passed --no-verify we
	// skip this and clear ExpectedMD5.
	expectedMD5 := ""
	if !opts.NoVerify && pre.HasMD5Sidecar {
		md5URL := MD5URL(opts.Source, opts.Backup, opts.Kind)
		md5Body, err := fetchSmall(ctx, client, md5URL)
		if err != nil {
			return nil, fmt.Errorf("fetch md5 sidecar: %w", err)
		}
		expectedMD5, err = parseMD5Sidecar(md5Body, md5URL)
		if err != nil {
			return nil, err
		}
	}

	start := time.Now()
	url := pre.URL
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}

	hasher := md5.New() //nolint:gosec // matches upstream .md5sum sidecar format; integrity, not crypto
	progress := &progressReader{
		r:     resp.Body,
		total: resp.ContentLength,
		cb:    opts.ProgressFn,
	}
	teed := io.TeeReader(progress, hasher)

	gz, err := gzip.NewReader(teed)
	if err != nil {
		return nil, fmt.Errorf("gzip header: %w", err)
	}
	defer gz.Close()

	extracted, err := extractTar(gz, opts.DestDir, opts.Force)
	if err != nil {
		return nil, err
	}

	// Drain anything left in the body (rare, but a tar that ended with
	// padding can leave bytes the tar reader doesn't pull). Without this
	// the md5 hash would be computed over a strict subset of the wire
	// bytes and verification would falsely fail.
	if _, err := io.Copy(io.Discard, teed); err != nil {
		return nil, fmt.Errorf("drain body: %w", err)
	}

	actualMD5 := hex.EncodeToString(hasher.Sum(nil))
	verified := false
	if expectedMD5 != "" {
		if !strings.EqualFold(actualMD5, expectedMD5) {
			return nil, fmt.Errorf("md5 mismatch: expected %s, got %s", expectedMD5, actualMD5)
		}
		verified = true
	}

	dur := time.Since(start)
	return &DownloadResult{
		BytesDownloaded: progress.read,
		Duration:        dur,
		DurationMs:      dur.Milliseconds(),
		MD5Verified:     verified,
		ExpectedMD5:     expectedMD5,
		ActualMD5:       actualMD5,
		ExtractedTo:     opts.DestDir,
		FilesExtracted:  extracted,
	}, nil
}

// extractTar walks an already-gunzipped tar stream and writes entries
// under destDir. Returns the count of regular files written. We mimic
// tron-docker's safety check (no path traversal, no writing through
// existing symlinks) but skip the symlink-resolution dance because we
// own the destination directory and never extract an absolute path.
//
// Staying inside destDir is not on its own enough: we do NOT own destDir
// exclusively. With `--node <name>` it is a jar node's install_path,
// which also holds FullNode.jar (what the systemd unit's ExecStart runs),
// config.conf and userdata/. Entry names come straight off the wire from
// a mirror that is plain HTTP for mainnet, so every entry is additionally
// confined to snapshotRoot/.
func extractTar(r io.Reader, destDir string, force bool) (int, error) {
	tr := tar.NewReader(r)
	count := 0
	cleanedDest, err := filepath.Abs(destDir)
	if err != nil {
		return 0, fmt.Errorf("abs dest: %w", err)
	}
	prefix := cleanedDest + string(os.PathSeparator)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return count, fmt.Errorf("tar header: %w", err)
		}

		// Reject absolute paths and traversal (`..`) before resolving.
		clean := filepath.Clean(hdr.Name)
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || clean == ".." {
			return count, fmt.Errorf("refusing path with traversal: %q", hdr.Name)
		}

		// Confine the entry to the archive's one legitimate top-level
		// directory. Without this, an entry named `FullNode.jar` or
		// `config.conf` is "inside destDir" and therefore accepted, and
		// with --force the O_EXCL guard is gone so O_TRUNC lands on the
		// jar the node's systemd unit executes. Refuse loudly, naming the
		// entry, and abort the extraction: a hostile archive must not be
		// quietly sanitised into a benign-looking one.
		if !underSnapshotRoot(clean) {
			return count, fmt.Errorf("refusing entry outside %s/: %q", snapshotRoot, hdr.Name)
		}

		target := filepath.Join(cleanedDest, clean)
		// Defence in depth: confirm target stays within destDir even
		// after Join's lexical clean (it should, given the check above,
		// but a future change to clean must not break this guarantee).
		if target != cleanedDest && !strings.HasPrefix(target, prefix) {
			return count, fmt.Errorf("entry %q would escape dest", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, fs(hdr.Mode)); err != nil {
				return count, fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return count, fmt.Errorf("mkdir parent %s: %w", target, err)
			}
			// Refuse to write through a pre-existing symlink — this
			// stops a hostile/borked archive from poisoning files
			// outside the chosen destination.
			if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return count, fmt.Errorf("refusing to write through existing symlink: %s", target)
			}
			flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
			if !force {
				// Without --force, abort on collision rather than overwrite.
				flags |= os.O_EXCL
			}
			out, err := os.OpenFile(target, flags, fs(hdr.Mode))
			if err != nil {
				return count, fmt.Errorf("create %s: %w", target, err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return count, fmt.Errorf("write %s: %w", target, err)
			}
			if err := out.Close(); err != nil {
				return count, fmt.Errorf("close %s: %w", target, err)
			}
			count++
		case tar.TypeSymlink, tar.TypeLink:
			// We deliberately drop symlinks — they're rare in TRON
			// snapshots and accepting them complicates the traversal
			// proof. If a real archive needs them, revisit with a
			// link-resolution check that mirrors the file path check.
			continue
		default:
			// Skip device files, fifos, etc — never present in TRON
			// chain data tarballs.
			continue
		}
	}
	return count, nil
}

// underSnapshotRoot reports whether a cleaned (relative, traversal-free)
// tar entry name lives at or under snapshotRoot. Three shapes are legal:
//
//	"."                        the archive root itself, emitted as a dir
//	                           entry by `tar -C <parent> -czf - .`; it maps
//	                           to destDir, which already exists
//	"output-directory"         the top-level dir entry
//	"output-directory/<...>"   anything beneath it
//
// A bare separator suffix is required so a sibling like
// "output-directory-evil/x" — which shares the string prefix but not the
// directory — is refused.
func underSnapshotRoot(clean string) bool {
	if clean == "." || clean == snapshotRoot {
		return true
	}
	return strings.HasPrefix(clean, snapshotRoot+string(os.PathSeparator))
}

// progressReader counts bytes flowing through it and emits a callback at
// throttled intervals. Used so the downloader can render a progress bar
// without coupling extract logic to UI.
type progressReader struct {
	r        io.Reader
	read     int64
	total    int64
	cb       func(downloaded, total int64)
	lastEmit time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.read += int64(n)
	}
	// Throttle UI to ~10 Hz; skip if the caller didn't supply a cb.
	// On any terminal condition (EOF or transport error) flush a final
	// frame so the user's last-seen progress matches the actual byte
	// count — without this, a connection drop at 87% would leave the
	// stale "87% eta ..." line on the terminal.
	if p.cb != nil && (err != nil || time.Since(p.lastEmit) > 100*time.Millisecond) {
		p.cb(p.read, p.total)
		p.lastEmit = time.Now()
	}
	return n, err
}

// OverwriteError is returned when the destination already has a chain
// database and the caller didn't pass --force. The cmd layer matches on
// it to surface a HUMAN_REQUIRED-like exit.
type OverwriteError struct {
	Path    string
	Message string
}

func (e *OverwriteError) Error() string { return e.Message }

// Helpers ----------------------------------------------------------------

func fetchSmall(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	// 4 KB is a generous ceiling for an .md5sum sidecar.
	return io.ReadAll(io.LimitReader(resp.Body, 4096))
}

// md5HexLen is the length of a hex-encoded MD5 digest.
const md5HexLen = 32

// parseMD5Sidecar pulls the hex digest out of a coreutils-style sidecar
// body ("<digest>  <filename>", the form the live mirrors publish; a
// bare digest is also accepted).
//
// The body comes off the network — mainnet mirrors are plain http:// —
// so every shape is possible: an empty file, a whitespace-only file, an
// HTML error page from an interception proxy, a truncated digest. Each
// is reported as an error naming the sidecar URL rather than being
// indexed blindly (an empty body used to panic here) and rather than
// yielding an empty digest: Download reads an empty expectedMD5 as
// "verification disabled", so degrading to one would silently turn a
// corrupt sidecar into a skipped integrity check.
func parseMD5Sidecar(body []byte, url string) (string, error) {
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return "", fmt.Errorf("malformed md5 sidecar at %s: empty body", url)
	}
	digest := fields[0]
	if len(digest) != md5HexLen {
		return "", fmt.Errorf("malformed md5 sidecar at %s: expected a %d-character hex digest, got %q (%d chars)",
			url, md5HexLen, elide(digest), len(digest))
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", fmt.Errorf("malformed md5 sidecar at %s: digest %q is not hexadecimal", url, digest)
	}
	return digest, nil
}

// elide caps an untrusted value quoted into an error message; the
// sidecar body can be up to the fetchSmall limit on a single "field".
func elide(s string) string {
	const max = 48
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

func databasePath(destDir string) string {
	// java-tron writes its leveldb / rocksdb at output-directory/database.
	// The tarballs we extract preserve that layout so this is where the
	// existing-data check lives.
	return filepath.Join(destDir, "output-directory", "database")
}

func userdataPath(destDir string) string {
	// userdata isn't part of the snapshot but lives next to the database
	// for jar-runtime nodes. We surface its presence so the user knows
	// witness keys / mined-block-cache / etc are preserved across the
	// extraction.
	return filepath.Join(destDir, "userdata")
}

func isNonEmptyDir(p string) bool {
	entries, err := os.ReadDir(p)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

func freeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	// Bavail (rather than Bfree) honours per-user quotas / reserved
	// blocks, matching what `df` shows.
	return st.Bavail * uint64(st.Bsize), nil
}

func fs(mode int64) os.FileMode {
	if mode <= 0 {
		return 0o644
	}
	return os.FileMode(mode) & 0o777
}

// humanBytes formats a byte count for messages — same look as `df -h`.
func humanBytes(n int64) string {
	const (
		KB = 1 << 10
		MB = 1 << 20
		GB = 1 << 30
		TB = 1 << 40
	)
	switch {
	case n >= TB:
		return fmt.Sprintf("%.2f TB", float64(n)/float64(TB))
	case n >= GB:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(GB))
	case n >= MB:
		return fmt.Sprintf("%.2f MB", float64(n)/float64(MB))
	case n >= KB:
		return fmt.Sprintf("%.2f KB", float64(n)/float64(KB))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
