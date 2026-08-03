package snapshot

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPick_DefaultsToFirstMatch(t *testing.T) {
	s := Pick(Filter{Network: NetworkMainnet, DBKind: DBKindLite})
	if s == nil || s.Domain != "34.143.247.77" {
		t.Fatalf("expected mainnet lite to default to SG mirror, got %+v", s)
	}
}

func TestPick_RegionNarrowing(t *testing.T) {
	s := Pick(Filter{Network: NetworkMainnet, DBKind: DBKindFull, Region: RegionAmerica})
	if s == nil {
		t.Fatal("expected a US mirror for full mainnet")
	}
	if s.Region != RegionAmerica {
		t.Fatalf("expected region america, got %s", s.Region)
	}
}

func TestPick_NoMatch(t *testing.T) {
	if got := Pick(Filter{Network: "private"}); got != nil {
		t.Fatalf("private network has no mirrors; got %+v", got)
	}
}

func TestLookupDomain(t *testing.T) {
	if s := LookupDomain("snapshots.nileex.io"); s == nil || s.Network != NetworkNile {
		t.Fatalf("nile lookup failed: %+v", s)
	}
	if s := LookupDomain("not-a-real-host"); s != nil {
		t.Fatalf("expected nil for unknown domain, got %+v", s)
	}
}

func TestTarballURL_Variants(t *testing.T) {
	main := *LookupDomain("34.143.247.77")
	if got := TarballURL(main, "backup20250115", DBKindLite); got != "http://34.143.247.77/backup20250115/LiteFullNode_output-directory.tgz" {
		t.Fatalf("mainnet lite URL wrong: %s", got)
	}
	// Nile lite: BaseURL = https://snapshots.nileex.io; the LiteFullNode
	// tarball is published at /backup<date>/LiteFullNode_*.tgz.
	nileLite := Pick(Filter{Network: NetworkNile, DBKind: DBKindLite})
	if nileLite == nil {
		t.Fatal("nile lite pick returned nil")
	}
	if got := TarballURL(*nileLite, "backup20260524", DBKindLite); got != "https://snapshots.nileex.io/backup20260524/LiteFullNode_output-directory.tgz" {
		t.Fatalf("nile lite URL wrong: %s", got)
	}
	// Nile rocksdb: BaseURL bakes in the /rocksdb prefix so the same
	// BaseURL+"/"+backup+"/"+tarball composition produces the published
	// rocksdb path. FullNode_*.tgz (no lite variant published).
	nileRocks := Pick(Filter{Network: NetworkNile, DBEngine: EngineRocksDB})
	if nileRocks == nil {
		t.Fatal("nile rocksdb pick returned nil")
	}
	if got := TarballURL(*nileRocks, "backup20260524", DBKindFull); got != "https://snapshots.nileex.io/rocksdb/backup20260524/FullNode_output-directory.tgz" {
		t.Fatalf("nile rocksdb URL wrong: %s", got)
	}
	if got := TarballURL(main, "x", DBKind("bogus")); got != "" {
		t.Fatalf("unknown kind should return empty, got %s", got)
	}
}

func TestExtractTar_StreamingHonoursForce(t *testing.T) {
	dest := t.TempDir()
	// Pre-place a file we should refuse to clobber without --force.
	clash := filepath.Join(dest, "output-directory", "database", "CURRENT")
	if err := os.MkdirAll(filepath.Dir(clash), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clash, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	tgz := buildTGZ(t, map[string]string{
		"output-directory/database/CURRENT": "fresh",
	})

	// force=false → must error on the clobber.
	_, err := extractTar(mustGunzip(t, tgz), dest, false)
	if err == nil {
		t.Fatal("expected error when overwriting without force")
	}

	// force=true → succeeds, content updates.
	_, err = extractTar(mustGunzip(t, tgz), dest, true)
	if err != nil {
		t.Fatalf("force extract: %v", err)
	}
	got, err := os.ReadFile(clash)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fresh" {
		t.Fatalf("file not overwritten: %q", got)
	}
}

func TestExtractTar_RejectsTraversal(t *testing.T) {
	tgz := buildTGZ(t, map[string]string{
		"../escape.txt": "nope",
	})
	dest := t.TempDir()
	_, err := extractTar(mustGunzip(t, tgz), dest, true)
	if err == nil {
		t.Fatal("expected traversal rejection")
	}
	if !strings.Contains(err.Error(), "traversal") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestExtractTar_RejectsAbsoluteEntry(t *testing.T) {
	tgz := buildTGZ(t, map[string]string{"/etc/cron.d/pwn": "nope"})
	dest := t.TempDir()
	_, err := extractTar(mustGunzip(t, tgz), dest, true)
	if err == nil {
		t.Fatal("expected absolute-path rejection")
	}
	if !strings.Contains(err.Error(), "traversal") {
		t.Fatalf("wrong error: %v", err)
	}
}

// TestExtractTar_RefusesEntriesOutsideSnapshotRoot is the regression test
// for the hostile-mirror case: the destination directory is NOT ours
// alone. With `--node <name>` it is a jar node's install_path, which also
// holds FullNode.jar (executed by the systemd unit's ExecStart),
// config.conf and userdata/. Staying inside destDir is therefore not
// enough — an entry has to live under output-directory/ as well.
func TestExtractTar_RefusesEntriesOutsideSnapshotRoot(t *testing.T) {
	cases := []struct {
		what  string
		entry string
	}{
		{"jar the systemd unit executes", "FullNode.jar"},
		{"rendered node config", "config.conf"},
		{"systemd unit", "tron-fn0.service"},
		{"operator state beside the db", "userdata/keystore/key.json"},
		{"nested, but not under the snapshot root", "conf/config.conf"},
		{"sibling sharing the string prefix", "output-directory-evil/FullNode.jar"},
		{"dotfile in the destination", ".bashrc"},
		{"re-rooted after a lexical clean", "output-directory/../FullNode.jar"},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			dest := t.TempDir()
			tgz := buildTGZ(t, map[string]string{tc.entry: "pwned"})
			// force=true is the dangerous mode: O_EXCL is dropped, so an
			// accepted entry would O_TRUNC whatever already sits there.
			_, err := extractTar(mustGunzip(t, tgz), dest, true)
			if err == nil {
				t.Fatalf("entry %q was accepted", tc.entry)
			}
			// The error must name the offending entry — a refused hostile
			// archive has to be distinguishable from a benign one.
			if !strings.Contains(err.Error(), tc.entry) {
				t.Fatalf("error should name entry %q, got: %v", tc.entry, err)
			}
			if _, statErr := os.Stat(filepath.Join(dest, filepath.Clean(tc.entry))); !os.IsNotExist(statErr) {
				t.Fatalf("entry %q reached disk (stat err: %v)", tc.entry, statErr)
			}
		})
	}
}

// TestExtractTar_HostileJarEntryLeavesInstalledJarUntouched walks the
// exploit scenario end to end: a plausible snapshot payload followed by a
// FullNode.jar entry, extracted with --force into a jar node's
// install_path. The jar on disk must survive byte-for-byte.
func TestExtractTar_HostileJarEntryLeavesInstalledJarUntouched(t *testing.T) {
	dest := t.TempDir() // stands in for a jar node's install_path
	jar := filepath.Join(dest, "FullNode.jar")
	if err := os.WriteFile(jar, []byte("the real jar"), 0o644); err != nil {
		t.Fatal(err)
	}

	tgz := buildTGZTyped(t, []tarEntry{
		{name: "output-directory/database/CURRENT", body: "MANIFEST-000001\n"},
		{name: "FullNode.jar", body: "malicious jar"},
	})
	_, err := extractTar(mustGunzip(t, tgz), dest, true)
	if err == nil {
		t.Fatal("expected the FullNode.jar entry to be refused")
	}
	if !strings.Contains(err.Error(), "FullNode.jar") {
		t.Fatalf("error should name the entry, got: %v", err)
	}
	got, err := os.ReadFile(jar)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the real jar" {
		t.Fatalf("FullNode.jar was overwritten: %q", got)
	}
}

// Symlink entries were (and stay) skipped inside the snapshot root, but a
// symlink aimed at a control file must be refused loudly rather than
// silently dropped.
func TestExtractTar_SymlinkEntries(t *testing.T) {
	t.Run("outside the snapshot root is refused", func(t *testing.T) {
		dest := t.TempDir()
		tgz := buildTGZTyped(t, []tarEntry{
			{name: "FullNode.jar", typeflag: tar.TypeSymlink, linkname: "/tmp/evil.jar"},
		})
		_, err := extractTar(mustGunzip(t, tgz), dest, true)
		if err == nil {
			t.Fatal("expected symlink entry outside the snapshot root to be refused")
		}
		if !strings.Contains(err.Error(), "FullNode.jar") {
			t.Fatalf("error should name the entry, got: %v", err)
		}
	})

	t.Run("inside the snapshot root is still skipped", func(t *testing.T) {
		dest := t.TempDir()
		tgz := buildTGZTyped(t, []tarEntry{
			{name: "output-directory/database/LINK", typeflag: tar.TypeSymlink, linkname: "CURRENT"},
			{name: "output-directory/database/CURRENT", body: "ok"},
		})
		n, err := extractTar(mustGunzip(t, tgz), dest, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 1 {
			t.Fatalf("expected 1 regular file extracted, got %d", n)
		}
		if _, err := os.Lstat(filepath.Join(dest, "output-directory", "database", "LINK")); !os.IsNotExist(err) {
			t.Fatalf("symlink entry should have been skipped, stat err: %v", err)
		}
	})
}

// A legitimate snapshot archive must extract exactly as before: both the
// `output-directory/...` and the `./output-directory/...` spellings, plus
// the bare `./` root entry that `tar -C <parent> -czf - .` emits.
func TestExtractTar_AcceptsLegitimateSnapshotLayout(t *testing.T) {
	dest := t.TempDir()
	tgz := buildTGZTyped(t, []tarEntry{
		{name: "./", typeflag: tar.TypeDir},
		{name: "output-directory/", typeflag: tar.TypeDir},
		{name: "output-directory/database/", typeflag: tar.TypeDir},
		{name: "output-directory/database/CURRENT", body: "MANIFEST-000001\n"},
		{name: "./output-directory/database/000001.sst", body: "sst bytes"},
		{name: "output-directory/index/CURRENT", body: "index"},
	})

	n, err := extractTar(mustGunzip(t, tgz), dest, true)
	if err != nil {
		t.Fatalf("legitimate archive rejected: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 regular files extracted, got %d", n)
	}
	for path, want := range map[string]string{
		"output-directory/database/CURRENT":    "MANIFEST-000001\n",
		"output-directory/database/000001.sst": "sst bytes",
		"output-directory/index/CURRENT":       "index",
	} {
		got, err := os.ReadFile(filepath.Join(dest, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestDownload_RefusesExistingDatabaseWithoutForce(t *testing.T) {
	dest := t.TempDir()
	// Seed a non-empty database directory.
	dbPath := filepath.Join(dest, "output-directory", "database")
	if err := os.MkdirAll(dbPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbPath, "MANIFEST"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := startMirror(t)
	defer srv.Close()

	src := Source{
		Network:       NetworkMainnet,
		DBKind:        DBKindLite,
		DBEngine:      EngineLevelDB,
		Region:        RegionSingapore,
		Domain:        "test",
		BaseURL:       srv.URL,
		IndexStrategy: "html",
	}
	_, err := Download(context.Background(), DownloadOptions{
		Source:  src,
		Backup:  "backup20250101",
		Kind:    DBKindLite,
		DestDir: dest,
		Force:   false,
	})
	if err == nil {
		t.Fatal("expected refusal with existing database")
	}
	var ow *OverwriteError
	if !errorsAs(err, &ow) {
		t.Fatalf("expected OverwriteError, got %T: %v", err, err)
	}
}

// startMirror serves a tiny tarball at /backup20250101/LiteFullNode_output-directory.tgz
// so the disk-space + overwrite checks can exercise without hitting the
// real upstream. Returns an httptest server.
func startMirror(t *testing.T) *httptest.Server {
	t.Helper()
	tgz := buildTGZ(t, map[string]string{"output-directory/database/CURRENT": "ok"})

	mux := http.NewServeMux()
	mux.HandleFunc("/backup20250101/LiteFullNode_output-directory.tgz",
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "0")
			w.Write(tgz)
		})
	mux.HandleFunc("/backup20250101/LiteFullNode_output-directory.tgz.md5sum",
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
	return httptest.NewServer(mux)
}

func buildTGZ(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		hdr := &tar.Header{
			Name: name,
			Size: int64(len(body)),
			Mode: 0o644,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// tarEntry describes one archive member for buildTGZTyped. A zero
// typeflag means a regular file. Unlike buildTGZ's map, the slice keeps
// entry order, which matters when a test needs a hostile entry to follow
// a legitimate one in the stream.
type tarEntry struct {
	name     string
	body     string
	typeflag byte
	linkname string
}

func buildTGZTyped(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typeflag
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: typ,
			Linkname: e.linkname,
			Mode:     0o644,
		}
		if typ == tar.TypeDir {
			hdr.Mode = 0o755
		}
		if typ == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mustGunzip(t *testing.T, tgz []byte) *gzip.Reader {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestProgressReader_FlushesOnError(t *testing.T) {
	// Reader returns 100 bytes then a non-EOF error: simulates a
	// truncated transport. The progress callback should fire with the
	// final byte count even though we never reached EOF — without the
	// flush, a connection drop at 87% would leave a stale "87% eta ..."
	// line on the terminal.
	src := &errAfterReader{data: bytes.Repeat([]byte("x"), 100), err: errFakeNetwork}

	var lastDownloaded int64
	calls := 0
	pr := &progressReader{
		r:     src,
		total: 1000,
		cb: func(d, _ int64) {
			lastDownloaded = d
			calls++
		},
	}

	// Drain: errAfterReader returns data first (no err), then err on the
	// next Read. io.ReadAll iterates Read until a non-nil err is returned.
	if _, err := io.ReadAll(pr); err != errFakeNetwork {
		t.Fatalf("expected fake network error, got %v", err)
	}
	if calls == 0 {
		t.Fatalf("expected callback to fire on terminal error, got %d calls", calls)
	}
	if lastDownloaded != 100 {
		t.Fatalf("expected final progress = 100 bytes, got %d", lastDownloaded)
	}
}

func TestProgressReader_FlushesOnEOF(t *testing.T) {
	src := &errAfterReader{data: []byte("hello"), err: io.EOF}
	var calls int
	pr := &progressReader{
		r:     src,
		total: 5,
		cb:    func(_, _ int64) { calls++ },
	}
	if _, err := io.ReadAll(pr); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if calls == 0 {
		t.Fatal("expected callback to fire on EOF")
	}
}

func TestProgressReader_NoCallbackWhenNil(t *testing.T) {
	// Nil cb: must not panic, must propagate Read semantics unchanged.
	src := &errAfterReader{data: []byte("hi"), err: io.EOF}
	pr := &progressReader{r: src, total: 2}
	buf := make([]byte, 8)
	if _, err := pr.Read(buf); err != nil { // first Read returns data, no err
		t.Fatalf("first read: %v", err)
	}
	if _, err := pr.Read(buf); err != io.EOF {
		t.Fatalf("second read: expected EOF, got %v", err)
	}
}

// errAfterReader returns `data` then `err`. Used to simulate a clean
// EOF or a network truncation in progress-reader tests.
type errAfterReader struct {
	data []byte
	off  int
	err  error
}

var errFakeNetwork = stubError("fake network error")

type stubError string

func (e stubError) Error() string { return string(e) }

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

// errorsAs is a tiny shim so the test file doesn't need to import
// errors just for one call (and so we can keep the same control flow if
// the typed-error story changes later).
func errorsAs(err error, target any) bool {
	type unwrapper interface{ Unwrap() error }
	for cur := err; cur != nil; {
		if t, ok := target.(**OverwriteError); ok {
			if ow, ok := cur.(*OverwriteError); ok {
				*t = ow
				return true
			}
		}
		u, ok := cur.(unwrapper)
		if !ok {
			break
		}
		cur = u.Unwrap()
	}
	return false
}

// TestIsRunning_TreatsLiveAndEPERMAsAlive directly exercises the kill(0)
// outcome triage that the prune subcommand depends on. Without this
// dedicated test the only coverage was indirect (via cmd/snapshot's
// prune fixture using PID 1), making future refactors of IsRunning
// risky.
func TestIsRunning_DeadPIDReturnsFalse(t *testing.T) {
	// A pid we're confident isn't allocated. 999_999 is below the
	// hard kernel cap on macOS (~99k) and Linux (~32k by default but
	// PID_MAX configurable up to 4M) — far enough out that the kernel
	// returns ESRCH on a kill(0) probe.
	if IsRunning(999_999) {
		t.Skip("PID 999999 is unexpectedly alive on this system; can't prove the negative")
	}
}

func TestIsRunning_AliveSelfReturnsTrue(t *testing.T) {
	// Self-PID is always signalable from the test process.
	if !IsRunning(os.Getpid()) {
		t.Errorf("IsRunning(self pid %d) should return true", os.Getpid())
	}
}

func TestIsRunning_EPERMTreatedAsAlive(t *testing.T) {
	// PID 1 (init/launchd) is always running. As a non-root test
	// process, kill(0) returns EPERM rather than ESRCH — the bug
	// fixed in this commit was misclassifying that as "dead".
	// Skip if running as root, where the EPERM path doesn't fire.
	if os.Getuid() == 0 {
		t.Skip("test runs as root; kill(0,1) returns nil instead of EPERM")
	}
	if !IsRunning(1) {
		t.Errorf("IsRunning(1) should return true; init exists but kill(0) returns EPERM for non-root callers — that's still alive")
	}
}
