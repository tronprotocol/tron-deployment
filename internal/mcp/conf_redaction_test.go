package mcp

import (
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/render"
)

// trond://nodes/{name}/conf hands the whole conf to an MCP client and
// from there to a model provider. A witness node's conf carries its
// signing key, so nothing that reaches that resource may contain one.
func TestRedactConfTextRemovesWitnessKey(t *testing.T) {
	const key = "da146374a75310b9666e834ee4ad0866d6f4035967bfc76217c5a495fff9f0d0"

	for name, conf := range map[string]string{
		"multi line":                "storage = {\n}\nlocalwitness = [\n  " + key + "\n]\n",
		"single line":               `localwitness = ["` + key + `"]` + "\n",
		"comment carries a bracket": "localwitness = [ # ] here\n  " + key + "\n]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got := strings.Join(render.RedactWitnessLines(strings.Split(conf, "\n")), "\n"); strings.Contains(got, key) {
				t.Errorf("witness key survived redaction:\n%s", got)
			}
		})
	}
}

// A conf with no key must come back untouched — the resource is how an
// agent reads a node's real configuration.
func TestRedactConfTextLeavesOrdinaryConfAlone(t *testing.T) {
	conf := "storage = {\n  db.engine = \"LEVELDB\"\n}\nnode.p2p.version = 11111\n"
	if got := strings.Join(render.RedactWitnessLines(strings.Split(conf, "\n")), "\n"); got != conf {
		t.Errorf("conf without a witness key was rewritten:\ngot  %q\nwant %q", got, conf)
	}
}

func TestRedactConfTextEmpty(t *testing.T) {
	if got := strings.Join(render.RedactWitnessLines(strings.Split("", "\n")), "\n"); got != "" {
		t.Errorf("empty conf became %q", got)
	}
}
