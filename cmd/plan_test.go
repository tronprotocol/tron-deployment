package cmd

import (
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/render"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

func TestSimpleHOCONDiff_DetectsLineChanges(t *testing.T) {
	old := []string{"node.ip = 127.0.0.1", "node.port = 8090", "shared = same"}
	new := []string{"node.ip = 127.0.0.1", "node.port = 9999", "shared = same"}

	diffs := render.DiffLines(old, new, 0)
	if len(diffs) != 2 {
		t.Fatalf("expected 2 diff lines (one - one +), got %d: %v", len(diffs), diffs)
	}
	if !strings.Contains(diffs[0], "8090") || !strings.HasPrefix(diffs[0], "- ") {
		t.Errorf("expected first diff to be removed-line for 8090, got %q", diffs[0])
	}
	if !strings.Contains(diffs[1], "9999") || !strings.HasPrefix(diffs[1], "+ ") {
		t.Errorf("expected second diff to be added-line for 9999, got %q", diffs[1])
	}
}

func TestSimpleHOCONDiff_EmptyWhenIdentical(t *testing.T) {
	lines := []string{"a = 1", "b = 2"}
	if got := render.DiffLines(lines, lines, 0); len(got) != 0 {
		t.Errorf("expected zero diffs for identical input, got %v", got)
	}
}

func TestSimpleHOCONDiff_HandlesUnequalLength(t *testing.T) {
	old := []string{"line1"}
	new := []string{"line1", "line2", "line3"}

	diffs := render.DiffLines(old, new, 0)
	// Two new lines added → two `+` entries.
	if len(diffs) != 2 {
		t.Fatalf("expected 2 added lines, got %d: %v", len(diffs), diffs)
	}
	for _, d := range diffs {
		if !strings.HasPrefix(d, "+ ") {
			t.Errorf("expected each diff to be a `+` add, got %q", d)
		}
	}
}

func TestPrintDiffSection_SkipsCleanly(t *testing.T) {
	// printDiffSection writes to stdout; we don't capture here, just
	// confirm the three branches don't panic.
	printDiffSection(nil, nil)                              // no existing
	printDiffSection(&state.ManagedNode{}, nil)             // existing but no diff
	printDiffSection(&state.ManagedNode{}, []string{"+ x"}) // existing + diff
}
