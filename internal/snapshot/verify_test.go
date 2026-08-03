package snapshot

import (
	"context"
	"crypto/md5" //nolint:gosec // mirrors the upstream .md5sum sidecar format under test
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// verifyMirror serves a tiny lite-snapshot tarball plus a .md5sum sidecar
// whose behaviour the test picks: "ok" (correct digest), "404" (absent),
// or "transport" (handled by errOnSidecarTransport instead of the server).
func verifyMirror(t *testing.T, sidecar string) (*httptest.Server, []byte) {
	t.Helper()
	tgz := buildTGZ(t, map[string]string{"output-directory/database/CURRENT": "ok"})
	sum := md5.Sum(tgz) //nolint:gosec // integrity fixture, not crypto
	digest := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/backup20250101/"+tarballLiteName,
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				return
			}
			w.Write(tgz)
		})
	mux.HandleFunc("/backup20250101/"+tarballLiteName+".md5sum",
		func(w http.ResponseWriter, _ *http.Request) {
			if sidecar != "ok" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, "%s  %s\n", digest, tarballLiteName)
		})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, tgz
}

func verifySource(baseURL string) Source {
	return Source{
		Network:       NetworkMainnet,
		DBKind:        DBKindLite,
		DBEngine:      EngineLevelDB,
		Region:        RegionSingapore,
		Domain:        "test",
		BaseURL:       baseURL,
		IndexStrategy: "html",
	}
}

// errOnSidecarTransport fails every request for a .md5sum URL at the
// transport layer, simulating a DNS/TCP/TLS failure (or an on-path
// attacker dropping the connection) rather than an HTTP status.
type errOnSidecarTransport struct{ inner http.RoundTripper }

var errSidecarTransport = stubError("simulated transport failure")

func (t *errOnSidecarTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasSuffix(req.URL.Path, ".md5sum") {
		return nil, errSidecarTransport
	}
	return t.inner.RoundTrip(req)
}

// countFiles returns how many regular files exist anywhere under root.
func countFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return n
}

// A mirror that 404s the sidecar must not be able to switch verification
// off: the download fails and nothing is written to the destination.
func TestDownload_MissingSidecarFailsClosed(t *testing.T) {
	dest := t.TempDir()
	srv, _ := verifyMirror(t, "404")

	_, err := Download(context.Background(), DownloadOptions{
		Source:  verifySource(srv.URL),
		Backup:  "backup20250101",
		Kind:    DBKindLite,
		DestDir: dest,
	})
	if err == nil {
		t.Fatal("expected refusal when the .md5sum sidecar is absent")
	}
	var vu *VerificationUnavailableError
	if !errors.As(err, &vu) {
		t.Fatalf("expected *VerificationUnavailableError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "--no-verify") {
		t.Errorf("error should point at the deliberate opt-out, got: %v", err)
	}
	if n := countFiles(t, dest); n != 0 {
		t.Errorf("destination must be untouched, found %d files under %s", n, dest)
	}
	if _, err := os.Stat(databasePath(dest)); !os.IsNotExist(err) {
		t.Errorf("no chain database should have been extracted at %s", databasePath(dest))
	}
}

// A transport-level failure fetching the sidecar is equally fatal — an
// attacker who can drop the sidecar connection must not gain a downgrade.
func TestDownload_SidecarTransportErrorFailsClosed(t *testing.T) {
	dest := t.TempDir()
	srv, _ := verifyMirror(t, "ok")

	_, err := Download(context.Background(), DownloadOptions{
		Source:     verifySource(srv.URL),
		Backup:     "backup20250101",
		Kind:       DBKindLite,
		DestDir:    dest,
		HTTPClient: &http.Client{Transport: &errOnSidecarTransport{inner: http.DefaultTransport}},
	})
	if err == nil {
		t.Fatal("expected refusal when the sidecar fetch fails at the transport layer")
	}
	var vu *VerificationUnavailableError
	if !errors.As(err, &vu) {
		t.Fatalf("expected *VerificationUnavailableError, got %T: %v", err, err)
	}
	if !errors.Is(err, errSidecarTransport) {
		t.Errorf("underlying transport error should be wrapped, got: %v", err)
	}
	if n := countFiles(t, dest); n != 0 {
		t.Errorf("destination must be untouched, found %d files under %s", n, dest)
	}
}

// --no-verify remains the escape hatch: with it, a 404 sidecar still
// extracts, but the result says plainly that nothing was verified.
func TestDownload_NoVerifyExtractsPastMissingSidecar(t *testing.T) {
	dest := t.TempDir()
	srv, _ := verifyMirror(t, "404")

	res, err := Download(context.Background(), DownloadOptions{
		Source:   verifySource(srv.URL),
		Backup:   "backup20250101",
		Kind:     DBKindLite,
		DestDir:  dest,
		NoVerify: true,
	})
	if err != nil {
		t.Fatalf("--no-verify should still extract: %v", err)
	}
	if res.MD5Verified {
		t.Error("MD5Verified must be false when verification was skipped")
	}
	if !res.VerificationSkipped {
		t.Error("VerificationSkipped must be true so output cannot imply a verified download")
	}
	if res.ExpectedMD5 != "" {
		t.Errorf("no expected digest should be recorded, got %q", res.ExpectedMD5)
	}
	if res.FilesExtracted != 1 {
		t.Errorf("expected 1 file extracted, got %d", res.FilesExtracted)
	}
	if _, err := os.Stat(filepath.Join(dest, "output-directory", "database", "CURRENT")); err != nil {
		t.Errorf("extracted file missing: %v", err)
	}
}

// The happy path is unchanged: a well-formed sidecar is fetched, compared,
// and the snapshot extracts exactly as before.
func TestDownload_WellFormedSidecarVerifiesAndExtracts(t *testing.T) {
	dest := t.TempDir()
	srv, tgz := verifyMirror(t, "ok")

	res, err := Download(context.Background(), DownloadOptions{
		Source:  verifySource(srv.URL),
		Backup:  "backup20250101",
		Kind:    DBKindLite,
		DestDir: dest,
	})
	if err != nil {
		t.Fatalf("download with a valid sidecar: %v", err)
	}
	if !res.MD5Verified {
		t.Error("expected MD5Verified = true")
	}
	if res.VerificationSkipped {
		t.Error("expected VerificationSkipped = false")
	}
	want := md5.Sum(tgz) //nolint:gosec // integrity fixture, not crypto
	if res.ActualMD5 != hex.EncodeToString(want[:]) {
		t.Errorf("actual md5 %q != %q", res.ActualMD5, hex.EncodeToString(want[:]))
	}
	if !strings.EqualFold(res.ExpectedMD5, res.ActualMD5) {
		t.Errorf("expected md5 %q != actual %q", res.ExpectedMD5, res.ActualMD5)
	}
	if res.FilesExtracted != 1 {
		t.Errorf("expected 1 file extracted, got %d", res.FilesExtracted)
	}
	body, err := os.ReadFile(filepath.Join(dest, "output-directory", "database", "CURRENT"))
	if err != nil {
		t.Fatalf("extracted file missing: %v", err)
	}
	if string(body) != "ok" {
		t.Errorf("extracted content %q != %q", string(body), "ok")
	}
}
