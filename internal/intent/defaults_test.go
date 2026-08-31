package intent

import (
	"fmt"
	"strings"
	"testing"
)

func TestApplyDefaultsArtifactSourceImageDefaults(t *testing.T) {
	tests := []struct {
		name      string
		build     *BuildSpec
		jar       *JarSource
		image     string
		wantImage string
	}{
		{
			name: "jar suppresses image default",
			jar:  &JarSource{URL: "https://example.com/FullNode.jar"},
		},
		{
			name: "build suppresses image default",
			build: &BuildSpec{
				Source: "/tmp/java-tron",
			},
		},
		{
			name:      "no artifact source gets legacy image default",
			wantImage: "tronprotocol/java-tron",
		},
		{
			name:      "explicit image is preserved",
			jar:       &JarSource{URL: "https://example.com/FullNode.jar"},
			image:     "example/java-tron:test",
			wantImage: "example/java-tron:test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := &Intent{
				Target: Target{Type: "local"},
				Nodes: []NodeSpec{{
					Type:  "fullnode",
					Build: tt.build,
					Jar:   tt.jar,
					Image: tt.image,
				}},
			}
			ApplyDefaults(i)
			if got := i.Nodes[0].Image; got != tt.wantImage {
				t.Fatalf("Image = %q, want %q", got, tt.wantImage)
			}
		})
	}
}

func TestParseJarWithoutImagePassesValidationAndSuppressesDefault(t *testing.T) {
	const jarSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	data := []byte(`
name: jar-fullnode
network: nile
target:
  type: local
  runtime: jar
nodes:
  - type: fullnode
    jar:
      url: https://example.com/FullNode.jar
      sha256: ` + jarSHA256 + `
`)

	i, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if i.Nodes[0].Image != "" {
		t.Fatalf("Image = %q, want empty for jar source", i.Nodes[0].Image)
	}
}

func TestParseExplicitImageAndJarRejected(t *testing.T) {
	const jarSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	data := []byte(`
name: conflicting-fullnode
network: nile
target:
  type: local
  runtime: jar
nodes:
  - type: fullnode
    image: example/java-tron:test
    jar:
      url: https://example.com/FullNode.jar
      sha256: ` + jarSHA256 + `
`)

	if _, err := Parse(data); err == nil {
		t.Fatal("Parse succeeded for intent with both image and jar")
	}
}

func TestParseJarRuntimeGuard(t *testing.T) {
	const jarSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const jar = `
name: jar-runtime-test
network: nile
target:
%s
nodes:
  - type: fullnode
    jar:
      url: https://example.com/FullNode.jar
      sha256: ` + jarSHA256 + `
`
	tests := []struct {
		name    string
		target  string
		wantErr bool
	}{
		{name: "default docker", target: "  type: local", wantErr: true},
		{name: "explicit docker", target: "  type: local\n  runtime: docker", wantErr: true},
		{name: "jar runtime", target: "  type: local\n  runtime: jar"},
		{name: "ssh jar runtime", target: "  type: ssh\n  host: example.com\n  user: tron\n  runtime: jar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(fmt.Sprintf(jar, tt.target)))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Parse error = %v, wantErr=%v", err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), "target.runtime: jar") {
				t.Fatalf("error = %v, want jar runtime guidance", err)
			}
		})
	}
}
