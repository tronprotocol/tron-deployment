package cmd

import (
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/render"
)

// TestLineDiff_PinsContractShape exercises the small diff helper
// that backs `trond verify-config`. The contract:
//   - identical → empty []
//   - one line changed → 2 entries (- old, + new)
//   - one line added at the end → 1 entry (+ new)
//   - one line removed at the end → 1 entry (- old)
//   - context > 0 includes prefix-matching lines for human readers
func TestLineDiff_PinsContractShape(t *testing.T) {
	cases := []struct {
		name     string
		live     string
		desired  string
		context  int
		wantLen  int
		wantHave []string
	}{
		{
			name:    "identical",
			live:    "a\nb\nc\n",
			desired: "a\nb\nc\n",
			wantLen: 0,
		},
		{
			name:     "one-line-change",
			live:     "a\nb\nc\n",
			desired:  "a\nx\nc\n",
			wantLen:  2,
			wantHave: []string{"- b", "+ x"},
		},
		{
			name:     "appended",
			live:     "a\nb\n",
			desired:  "a\nb\nc\n",
			wantLen:  1,
			wantHave: []string{"+ c"},
		},
		{
			name:     "removed",
			live:     "a\nb\nc\n",
			desired:  "a\nb\n",
			wantLen:  1,
			wantHave: []string{"- c"},
		},
		{
			name:     "context-1-includes-prev",
			live:     "a\nb\nc\n",
			desired:  "a\nb\nx\n",
			context:  1,
			wantLen:  3,
			wantHave: []string{"  b", "- c", "+ x"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := render.DiffText(tc.live, tc.desired, tc.context)
			if len(got) != tc.wantLen {
				t.Fatalf("len: got %d, want %d\nactual:\n%s",
					len(got), tc.wantLen, strings.Join(got, "\n"))
			}
			joined := strings.Join(got, "\n")
			for _, want := range tc.wantHave {
				if !strings.Contains(joined, want) {
					t.Errorf("missing %q in:\n%s", want, joined)
				}
			}
		})
	}
}

func TestCountLines(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"a\n", 2},
		{"a\nb\n", 3},
		{"a\nb", 2},
	}
	for _, tc := range cases {
		if got := countLines(tc.in); got != tc.want {
			t.Errorf("countLines(%q): got %d, want %d", tc.in, got, tc.want)
		}
	}
}
