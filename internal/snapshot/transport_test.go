package snapshot

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The mainnet mirrors in SourceTable are bare IPs with no HTTPS endpoint
// (probed 2026-07-31: :80 answers 200, :443 times out). trond therefore
// cannot upgrade those transfers — but it must never let the cleartext hop
// pass silently, because the .md5sum sidecar rides the same connection and
// so proves nothing about provenance. These tests pin that behaviour.

func TestPlaintextTransport_ClassifiesScheme(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"http://34.143.247.77/backup20250115/LiteFullNode_output-directory.tgz", true},
		{"https://snapshots.nileex.io/backup20260524/LiteFullNode_output-directory.tgz", false},
		{"HTTPS://snapshots.nileex.io/x.tgz", false}, // scheme compare is case-insensitive
		{"HTTP://34.143.247.77/x.tgz", true},
		{"ftp://example.invalid/x.tgz", true}, // anything not https is unauthenticated
		{"34.143.247.77/x.tgz", true},         // no scheme at all → fail safe to "warn"
		{"://nonsense", true},                 // unparseable → fail safe to "warn"
	}
	for _, c := range cases {
		if got := PlaintextTransport(c.url); got != c.want {
			t.Errorf("PlaintextTransport(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

// TestSourceTable_NileUsesHTTPS guards the one part of the table that does
// have a TLS endpoint. If someone downgrades these rows to http:// the
// warning would start firing for nile and we'd have lost real protection.
func TestSourceTable_NileUsesHTTPS(t *testing.T) {
	for _, s := range SourceTable {
		if s.Network != NetworkNile {
			continue
		}
		if !strings.HasPrefix(s.BaseURL, "https://") {
			t.Errorf("nile source %q has BaseURL %q; nile publishes HTTPS and must use it",
				s.Domain, s.BaseURL)
		}
	}
}

func TestDownload_WarnsOnPlaintextMirror(t *testing.T) {
	dest := t.TempDir()
	srv := startTinyMirror(t, false)
	defer srv.Close()

	var warnings []string
	res, err := Download(context.Background(), DownloadOptions{
		Source:  sourceFor(srv.URL),
		Backup:  "backup20250101",
		Kind:    DBKindLite,
		DestDir: dest,
		WarnFn:  func(msg string) { warnings = append(warnings, msg) },
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one warning for an http:// mirror, got %d: %v", len(warnings), warnings)
	}
	w := warnings[0]
	for _, want := range []string{"cleartext HTTP", "same unauthenticated", "--sha256"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning missing %q; got:\n%s", want, w)
		}
	}
	if !res.PlaintextTransport {
		t.Error("DownloadResult.PlaintextTransport should be true for an http:// mirror")
	}
}

func TestDownload_NoWarningOnHTTPSMirror(t *testing.T) {
	dest := t.TempDir()
	srv := startTinyMirror(t, true)
	defer srv.Close()

	var warnings []string
	res, err := Download(context.Background(), DownloadOptions{
		Source:  sourceFor(srv.URL),
		Backup:  "backup20250101",
		Kind:    DBKindLite,
		DestDir: dest,
		WarnFn:  func(msg string) { warnings = append(warnings, msg) },
		// httptest's TLS server uses a self-signed cert; trusting it here
		// keeps the test about scheme classification, not PKI.
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warning for an https:// mirror, got: %v", warnings)
	}
	if res.PlaintextTransport {
		t.Error("DownloadResult.PlaintextTransport should be false for an https:// mirror")
	}
}

func TestPreflight_ReportsPlaintextTransport(t *testing.T) {
	srv := startTinyMirror(t, false)
	defer srv.Close()

	pre, err := Preflight(context.Background(), DownloadOptions{
		Source:  sourceFor(srv.URL),
		Backup:  "backup20250101",
		Kind:    DBKindLite,
		DestDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !pre.PlaintextTransport {
		t.Error("PreflightResult.PlaintextTransport should be true for an http:// mirror")
	}
}

// TestDownload_NilWarnFnIsSafe — the warning is optional plumbing; a caller
// that doesn't supply WarnFn must still download, and must still get the
// fact on the result struct.
func TestDownload_NilWarnFnIsSafe(t *testing.T) {
	srv := startTinyMirror(t, false)
	defer srv.Close()

	res, err := Download(context.Background(), DownloadOptions{
		Source:  sourceFor(srv.URL),
		Backup:  "backup20250101",
		Kind:    DBKindLite,
		DestDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("download with nil WarnFn: %v", err)
	}
	if !res.PlaintextTransport {
		t.Error("PlaintextTransport should be reported even with no WarnFn")
	}
}

func TestDownload_SHA256PinMatches(t *testing.T) {
	srv := startTinyMirror(t, false)
	defer srv.Close()

	sum := sha256.Sum256(tinyTGZ(t))
	want := hex.EncodeToString(sum[:])

	res, err := Download(context.Background(), DownloadOptions{
		Source:         sourceFor(srv.URL),
		Backup:         "backup20250101",
		Kind:           DBKindLite,
		DestDir:        t.TempDir(),
		ExpectedSHA256: strings.ToUpper(want), // case-insensitive on input
	})
	if err != nil {
		t.Fatalf("download with matching pin: %v", err)
	}
	if !res.SHA256Verified {
		t.Error("SHA256Verified should be true when the pin matches")
	}
	if res.SHA256 != want {
		t.Errorf("SHA256 = %s, want %s", res.SHA256, want)
	}
	if res.ExpectedSHA256 != want {
		t.Errorf("ExpectedSHA256 should echo the normalised pin, got %s", res.ExpectedSHA256)
	}
}

func TestDownload_SHA256PinMismatchFails(t *testing.T) {
	srv := startTinyMirror(t, false)
	defer srv.Close()

	_, err := Download(context.Background(), DownloadOptions{
		Source:         sourceFor(srv.URL),
		Backup:         "backup20250101",
		Kind:           DBKindLite,
		DestDir:        t.TempDir(),
		ExpectedSHA256: strings.Repeat("ab", 32), // valid hex, wrong value
	})
	if err == nil {
		t.Fatal("expected a mismatching --sha256 pin to fail the download")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("wrong error for a bad pin: %v", err)
	}
}

// TestDownload_SHA256PinRejectedBeforeTransfer — a malformed pin must fail
// immediately, not after an operator has waited out a multi-hour transfer.
// The mirror is never started, so any network use would fail differently.
func TestDownload_MalformedSHA256PinFailsEarly(t *testing.T) {
	for _, bad := range []string{"deadbeef", strings.Repeat("z", 64)} {
		_, err := Download(context.Background(), DownloadOptions{
			Source:         sourceFor("http://127.0.0.1:1"), // nothing listening
			Backup:         "backup20250101",
			Kind:           DBKindLite,
			DestDir:        t.TempDir(),
			ExpectedSHA256: bad,
		})
		if err == nil {
			t.Fatalf("expected malformed pin %q to be rejected", bad)
		}
		if !strings.Contains(err.Error(), "invalid --sha256") {
			t.Fatalf("pin %q should be rejected before any network use, got: %v", bad, err)
		}
	}
}

// TestDownload_NoPinLeavesSHA256Unverified — computing the digest is not
// the same as verifying it. Without an out-of-band pin there is nothing to
// verify against, and the result must not claim otherwise.
func TestDownload_NoPinLeavesSHA256Unverified(t *testing.T) {
	srv := startTinyMirror(t, false)
	defer srv.Close()

	res, err := Download(context.Background(), DownloadOptions{
		Source:  sourceFor(srv.URL),
		Backup:  "backup20250101",
		Kind:    DBKindLite,
		DestDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if res.SHA256Verified {
		t.Error("SHA256Verified must be false when no pin was supplied")
	}
	if res.SHA256 == "" {
		t.Error("SHA256 should still be computed so the operator can pin it next time")
	}
	if res.ExpectedSHA256 != "" {
		t.Errorf("ExpectedSHA256 should be empty with no pin, got %q", res.ExpectedSHA256)
	}
}

// Helpers ----------------------------------------------------------------

func sourceFor(baseURL string) Source {
	return Source{
		Network:       NetworkMainnet,
		DBKind:        DBKindLite,
		DBEngine:      EngineLevelDB,
		Domain:        "test",
		BaseURL:       baseURL,
		IndexStrategy: "html",
	}
}

// tinyTGZ is the exact byte stream the test mirrors serve, so a test can
// compute the digest the downloader is expected to report.
func tinyTGZ(t *testing.T) []byte {
	t.Helper()
	return buildTGZ(t, map[string]string{"output-directory/database/CURRENT": "ok"})
}

// startTinyMirror serves the tiny tarball and a matching .md5sum sidecar
// over plain HTTP or TLS depending on tlsMode, so the scheme-dependent
// behaviour can be exercised with the same fixture.
//
// The sidecar is served for real because a missing one is now a hard
// error (VERIFICATION_UNAVAILABLE) rather than a silent skip — these
// tests are about transport disclosure and the --sha256 pin, so they
// need the download to reach completion.
func startTinyMirror(t *testing.T, tlsMode bool) *httptest.Server {
	t.Helper()
	tgz := tinyTGZ(t)
	sum := fmt.Sprintf("%x  LiteFullNode_output-directory.tgz\n", md5.Sum(tgz))

	mux := http.NewServeMux()
	mux.HandleFunc("/backup20250101/LiteFullNode_output-directory.tgz",
		func(w http.ResponseWriter, _ *http.Request) {
			// Let net/http set Content-Length from the body. (The older
			// startMirror fixture pins it to 0, which is fine there because
			// that test never reaches the GET.)
			_, _ = w.Write(tgz)
		})
	mux.HandleFunc("/backup20250101/LiteFullNode_output-directory.tgz.md5sum",
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(sum))
		})

	if !tlsMode {
		return httptest.NewServer(mux)
	}
	srv := httptest.NewTLSServer(mux)
	// httptest.Server.Client() already trusts the server's cert; assert it
	// so a future change to the helper can't silently downgrade the test.
	if tr, ok := srv.Client().Transport.(*http.Transport); ok && tr.TLSClientConfig == nil {
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return srv
}
