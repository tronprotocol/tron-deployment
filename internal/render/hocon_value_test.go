package render

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gurkankaymak/hocon"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

// parseHOCON is the oracle for the tests below. Comparing rendered strings
// only proves "the output changed"; parsing proves "the output is loadable",
// which is the property that was actually broken.
func parseHOCON(t *testing.T, body string) *hocon.Config {
	t.Helper()
	cfg, err := hocon.ParseString(body)
	if err != nil {
		t.Fatalf("rendered HOCON does not parse: %v\n--- body ---\n%s", err, body)
	}
	return cfg
}

// TestHoconValue_RendersParseableHOCON pins the defect hoconValue had: it used
// fmt, and Go syntax is only accidentally JSON. Every case below is a value an
// operator can legitimately put in config_overrides.
func TestHoconValue_RendersParseableHOCON(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string // exact rendered literal
	}{
		{
			// The case that blocked multi-witness private networks: a list
			// of objects rendered as Go's [map[k:v]] syntax, which no HOCON
			// parser accepts.
			name: "list of objects (genesis.block.witnesses)",
			in: []any{
				map[string]any{"address": "TN3zfjYUmMFK3ZsHSsrdJoNRtGkQmZLBLz", "voteCount": 5000},
			},
			want: `[{"address":"TN3zfjYUmMFK3ZsHSsrdJoNRtGkQmZLBLz","voteCount":5000}]`,
		},
		{
			name: "empty list",
			in:   []any{},
			want: `[]`,
		},
		{
			name: "list of scalars (seed.node.ip.list)",
			in:   []any{"127.0.0.1:18888", "10.0.0.1:18888"},
			want: `["127.0.0.1:18888","10.0.0.1:18888"]`,
		},
		{
			name: "nested map",
			in:   map[string]any{"engine": "ROCKSDB", "cache": 512},
			want: `{"cache":512,"engine":"ROCKSDB"}`,
		},
		{
			// %q emitted a Go \x-escape here, which is not a JSON/HOCON
			// escape, so the whole rendered config failed to load.
			name: "string with a control byte",
			in:   "ctrl\x01char",
			want: "\"ctrl\\u0001char\"",
		},
		{
			name: "string with quote and backslash",
			in:   `has "quote" and \backslash`,
			want: `"has \"quote\" and \\backslash"`,
		},
		{
			// SetEscapeHTML(false): a URL must survive verbatim rather than
			// having its ampersand rewritten to a & escape.
			name: "url with ampersand",
			in:   "http://tron.org/a?x=1&y=2",
			want: `"http://tron.org/a?x=1&y=2"`,
		},
		{
			name: "non-ASCII is passed through, not escaped",
			in:   "中文-emoji-\U0001F600",
			want: "\"中文-emoji-\U0001F600\"",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hoconValue(tc.in)
			if got != tc.want {
				t.Errorf("hoconValue() = %s, want %s", got, tc.want)
			}
			// The property that matters: the value is loadable in place.
			if cfg := parseHOCON(t, "k = "+got); cfg == nil {
				t.Fatal("parsed config is nil")
			}
		})
	}
}

// TestHoconValue_ScalarFormsUnchanged guards the numbers-and-bools path, which
// is deliberately still on fmt. If a later refactor routes these through the
// JSON encoder too, every rendered config in the wild changes; that should be
// a conscious decision, not a side effect of touching this function.
func TestHoconValue_ScalarFormsUnchanged(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{true, "true"},
		{false, "false"},
		{0, "0"},
		{5000, "5000"},
		{-1, "-1"},
		{int64(9223372036854775807), "9223372036854775807"},
		{1.5, "1.5"},
	}
	for _, tc := range cases {
		if got := hoconValue(tc.in); got != tc.want {
			t.Errorf("hoconValue(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRenderHOCON_MultiWitnessGenesis is the end-to-end form of the bug: the
// two-SR private network tron-docker documents (private_net_config_witness1 /
// _witness2) could not be expressed in an intent at all, because the witness
// list never survived rendering.
func TestRenderHOCON_MultiWitnessGenesis(t *testing.T) {
	const (
		sr1 = "TN3zfjYUmMFK3ZsHSsrdJoNRtGkQmZLBLz"
		sr2 = "TAt6GfSHVaGGx7Xzu3mgtCmqPBZzXWQhPP"
	)
	i := &intent.Intent{Name: "two-sr", Network: "private"}
	node := &intent.NodeSpec{
		Type: "witness",
		ConfigOverrides: map[string]any{
			"genesis.block.witnesses": []any{
				map[string]any{"address": sr1, "url": "http://tron.org", "voteCount": 5000},
				map[string]any{"address": sr2, "url": "http://tron.org", "voteCount": 5000},
			},
		},
	}

	out, err := RenderHOCON("", i, node)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// NOTE: we deliberately do not run hocon.ParseString over the whole
	// document. gurkankaymak/hocon is an incomplete HOCON implementation and
	// cannot parse the shipped java-tron templates at all — private fails at
	// 112:22 on its comma rule, mainnet at 37:5 and nile at 103:5 on a '#'
	// comment — even before any override is appended. Typesafe Config (what
	// java-tron actually uses) accepts them. So the oracle here is the value
	// itself: HOCON is a JSON superset, so a value that decodes as JSON is
	// loadable.
	line := findOverrideLine(t, out, "genesis.block.witnesses")
	var got []map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("rendered witness list is not valid JSON (so not valid HOCON): %v\nvalue: %s", err, line)
	}
	if len(got) != 2 {
		t.Fatalf("witness list has %d entries, want 2: %s", len(got), line)
	}
	for i, want := range []string{sr1, sr2} {
		if got[i]["address"] != want {
			t.Errorf("witness[%d].address = %v, want %s", i, got[i]["address"], want)
		}
	}
	if strings.Contains(out, "map[") {
		t.Error("rendered HOCON contains Go map syntax — the fmt fallback is back")
	}
}

// findOverrideLine returns the right-hand side of `key = <value>` from the
// trond overrides block, failing the test if the key is absent.
func findOverrideLine(t *testing.T, rendered, key string) string {
	t.Helper()
	for _, l := range strings.Split(rendered, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, key+" = ") {
			return strings.TrimPrefix(l, key+" = ")
		}
	}
	t.Fatalf("override key %q not found in rendered HOCON", key)
	return ""
}
