package snapshot

import (
	"context"
	"crypto/md5" //nolint:gosec // fixture digest for the .md5sum sidecar; integrity, not crypto
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The .md5sum body arrives over the network (mainnet mirrors are plain
// http://), so every degenerate shape has to produce a named error
// rather than a panic — and rather than an empty digest, which Download
// reads as "verification disabled".
func TestParseMD5Sidecar_MalformedBodiesError(t *testing.T) {
	const url = "http://34.143.247.77/backup20250101/LiteFullNode_output-directory.tgz.md5sum"

	cases := []struct {
		name string
		body string
		want string // substring the error must carry
	}{
		{"empty body", "", "empty body"},
		{"whitespace only", " \t\r\n  \n", "empty body"},
		{"newline only", "\n", "empty body"},
		{"html error page", "<html>\r\n<head><title>502 Bad Gateway</title></head>\r\n</html>\r\n", "hex digest"},
		{"short digest", "d41d8cd98f00b204e980\n", "hex digest"},
		{"long digest", strings.Repeat("a", 64) + "  LiteFullNode_output-directory.tgz\n", "hex digest"},
		{"non-hex digest of the right length", strings.Repeat("z", md5HexLen) + "  LiteFullNode_output-directory.tgz\n", "not hexadecimal"},
		{"filename first", "LiteFullNode_output-directory.tgz  d41d8cd98f00b204e9800998ecf8427e\n", "hex digest"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A panic here fails the test, which is the point: this is
			// the index-out-of-range sink.
			got, err := parseMD5Sidecar([]byte(tc.body), url)
			if err == nil {
				t.Fatalf("expected an error for %q, got digest %q", tc.body, got)
			}
			if got != "" {
				t.Fatalf("a rejected sidecar must yield no digest (an empty expectedMD5 silently disables verification), got %q", got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q should explain the problem (%q)", err, tc.want)
			}
			if !strings.Contains(err.Error(), url) {
				t.Fatalf("error %q should name the sidecar URL %s", err, url)
			}
		})
	}
}

func TestParseMD5Sidecar_WellFormedBodiesParse(t *testing.T) {
	const digest = "d41d8cd98f00b204e9800998ecf8427e"
	const url = "http://34.143.247.77/backup20250101/LiteFullNode_output-directory.tgz.md5sum"

	cases := []struct {
		name string
		body string
		want string
	}{
		// What the live mirrors publish: coreutils `md5sum` output.
		{"coreutils two-field form", digest + "  LiteFullNode_output-directory.tgz\n", digest},
		{"single space separator", digest + " LiteFullNode_output-directory.tgz\n", digest},
		{"binary mode marker", digest + " *LiteFullNode_output-directory.tgz\n", digest},
		{"no trailing newline", digest + "  LiteFullNode_output-directory.tgz", digest},
		{"bare digest", digest, digest},
		{"surrounding whitespace", "\n  " + digest + "  LiteFullNode_output-directory.tgz  \n", digest},
		{"uppercase digest", strings.ToUpper(digest) + "  LiteFullNode_output-directory.tgz\n", strings.ToUpper(digest)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMD5Sidecar([]byte(tc.body), url)
			if err != nil {
				t.Fatalf("well-formed sidecar %q rejected: %v", tc.body, err)
			}
			if got != tc.want {
				t.Fatalf("digest = %q, want %q", got, tc.want)
			}
		})
	}
}

// startSidecarMirror serves a small tarball plus a .md5sum sidecar whose
// body the test controls, so Download's sidecar handling is exercised
// over the real HTTP path (HEAD probe + GET) rather than in isolation.
func startSidecarMirror(t *testing.T, tgz []byte, sidecar string) Source {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/backup20250101/LiteFullNode_output-directory.tgz",
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(tgz)))
			w.Write(tgz) // body is discarded by net/http on HEAD
		})
	mux.HandleFunc("/backup20250101/LiteFullNode_output-directory.tgz.md5sum",
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(sidecar)))
			w.Write([]byte(sidecar))
		})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return Source{
		Network:       NetworkMainnet,
		DBKind:        DBKindLite,
		DBEngine:      EngineLevelDB,
		Region:        RegionSingapore,
		Domain:        "test",
		BaseURL:       srv.URL,
		IndexStrategy: "html",
	}
}

// A mirror (or a network attacker on the plain-http path to one) that
// answers the sidecar HEAD with 200 and the GET with an empty body used
// to crash the process here. It must be a clean error instead.
func TestDownload_EmptySidecarErrorsInsteadOfPanicking(t *testing.T) {
	bodies := map[string]string{
		"empty":           "",
		"whitespace only": "   \n",
		"html error page": "<html><body>403 Forbidden</body></html>\n",
		"truncated hex":   "d41d8cd98f00\n",
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			src := startSidecarMirror(t, buildTGZ(t, map[string]string{
				"output-directory/database/CURRENT": "ok",
			}), body)

			res, err := Download(context.Background(), DownloadOptions{
				Source:  src,
				Backup:  "backup20250101",
				Kind:    DBKindLite,
				DestDir: dest,
			})
			if err == nil {
				t.Fatalf("expected a malformed-sidecar error, got result %+v", res)
			}
			if res != nil {
				t.Fatalf("expected no result alongside the error, got %+v", res)
			}
			if !strings.Contains(err.Error(), "malformed md5 sidecar") {
				t.Fatalf("error should name the malformed sidecar, got: %v", err)
			}
			if !strings.Contains(err.Error(), ".tgz.md5sum") {
				t.Fatalf("error should carry the sidecar URL, got: %v", err)
			}
			// The refusal happens before the transfer, so nothing landed.
			if isNonEmptyDir(databasePath(dest)) {
				t.Fatal("nothing should have been extracted after a sidecar refusal")
			}
		})
	}
}

// The happy path is unchanged: a coreutils-form sidecar still yields the
// same digest and still gates the download on a match.
func TestDownload_WellFormedSidecarStillVerifies(t *testing.T) {
	dest := t.TempDir()
	tgz := buildTGZ(t, map[string]string{"output-directory/database/CURRENT": "ok"})
	sum := md5.Sum(tgz) //nolint:gosec // matches the upstream .md5sum sidecar format
	digest := hex.EncodeToString(sum[:])

	src := startSidecarMirror(t, tgz, digest+"  LiteFullNode_output-directory.tgz\n")

	res, err := Download(context.Background(), DownloadOptions{
		Source:  src,
		Backup:  "backup20250101",
		Kind:    DBKindLite,
		DestDir: dest,
	})
	if err != nil {
		t.Fatalf("download with a well-formed sidecar: %v", err)
	}
	if !res.MD5Verified {
		t.Fatal("expected MD5Verified = true")
	}
	if res.ExpectedMD5 != digest {
		t.Fatalf("ExpectedMD5 = %q, want %q", res.ExpectedMD5, digest)
	}
	if res.ActualMD5 != digest {
		t.Fatalf("ActualMD5 = %q, want %q", res.ActualMD5, digest)
	}
	if res.FilesExtracted != 1 {
		t.Fatalf("FilesExtracted = %d, want 1", res.FilesExtracted)
	}
}

// A sidecar that parses but doesn't match must still fail — the new
// validation must not have turned verification into a no-op.
func TestDownload_SidecarMismatchStillFails(t *testing.T) {
	dest := t.TempDir()
	tgz := buildTGZ(t, map[string]string{"output-directory/database/CURRENT": "ok"})
	wrong := strings.Repeat("0", md5HexLen)

	src := startSidecarMirror(t, tgz, wrong+"  LiteFullNode_output-directory.tgz\n")

	if _, err := Download(context.Background(), DownloadOptions{
		Source:  src,
		Backup:  "backup20250101",
		Kind:    DBKindLite,
		DestDir: dest,
	}); err == nil || !strings.Contains(err.Error(), "md5 mismatch") {
		t.Fatalf("expected an md5 mismatch error, got: %v", err)
	}
}
