package intent

import (
	"os"
	"strings"
	"testing"
)

func TestParseRejectsUnknownTopLevelAndNestedFields(t *testing.T) {
	for _, body := range []string{
		"name: n\nnetwork: mainnet\nunknown: true\n",
		"name: n\nnetwork: mainnet\nnodes:\n  - type: fullnode\n    resources:\n      memory: 8GB\n      unknown: true\n",
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Fatalf("accepted unknown field: %s", body)
		}
	}
}

func TestLoadWithOverlayPreservesUntouchedTargetFields(t *testing.T) {
	dir := t.TempDir()
	base := dir + "/base.yaml"
	overlay := dir + "/overlay.yaml"
	baseData := []byte("name: n\nnetwork: mainnet\ntarget:\n  type: ssh\n  host: example\n  port: 22\n  user: tron\n  identity_file: /key\nnodes:\n  - type: fullnode\n    resources:\n      memory: 8GB\n")
	if err := os.WriteFile(base, baseData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte("target:\n  user: other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadWithOverlay(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if got.Target.Host != "example" || got.Target.IdentityFile != "/key" || got.Target.User != "other" {
		t.Fatalf("target=%+v", got.Target)
	}
	if strings.TrimSpace(got.Target.Type) != "ssh" {
		t.Fatalf("target type=%q", got.Target.Type)
	}
}

func TestLoadWithOverlayValidatesJarRuntimeAfterMerge(t *testing.T) {
	const jarSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const baseData = `name: jar-overlay-test
network: nile
target:
  type: local
  runtime: jar
nodes:
  - type: fullnode
    jar:
      url: https://example.com/FullNode.jar
      sha256: ` + jarSHA256 + "\n"

	for _, tc := range []struct {
		name      string
		overlay   string
		wantError bool
	}{
		{name: "docker runtime rejects jar", overlay: "target:\n  runtime: docker\n", wantError: true},
		{name: "jar runtime is valid", overlay: "target:\n  runtime: jar\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			base := dir + "/base.yaml"
			overlay := dir + "/overlay.yaml"
			if err := os.WriteFile(base, []byte(baseData), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(overlay, []byte(tc.overlay), 0600); err != nil {
				t.Fatal(err)
			}

			_, err := LoadWithOverlay(base, overlay)
			if (err != nil) != tc.wantError {
				t.Fatalf("LoadWithOverlay error = %v, wantError=%v", err, tc.wantError)
			}
			if tc.wantError && !strings.Contains(err.Error(), "target.runtime: jar") {
				t.Fatalf("error = %v, want jar runtime guidance", err)
			}
		})
	}
}
