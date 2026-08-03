package cmd

import (
	"strings"
	"testing"
)

// F3 regression tests for the two positional, LCS-free differs that
// live in package cmd: simpleHOCONDiff (plan --diff, whose output goes
// into result["config_diff"] and out through output.WriteJSON) and
// lineDiff (verify-config, whose output goes into diffs[]).
//
// Both compare a live/deployed .conf — which carries the real SR key —
// against a freshly rendered one. Because neither differ aligns lines,
// ANY line-count change above the `localwitness` assignment misaligns
// the whole tail and pushes the key into the emitted diff.

const (
	keyA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	keyB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func assertNoKeys(t *testing.T, where string, lines []string) {
	t.Helper()
	for _, l := range lines {
		for _, k := range []string{keyA, keyB} {
			if strings.Contains(l, k) {
				t.Errorf("%s leaked a witness private key: %q", where, l)
			}
		}
	}
}

func countRedactedMarkers(lines []string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, `localwitness = ["<REDACTED>"]`) {
			n++
		}
	}
	return n
}

func TestSimpleHOCONDiff_RedactsWitnessKey(t *testing.T) {
	cases := []struct {
		name         string
		old, new     []string
		wantDiffs    int
		wantRedacted int
	}{
		{
			// Key rotated in the environment: the assignment genuinely
			// differs and must still be reported, redacted on both sides.
			name:         "key-rotation",
			old:          []string{"a = 1", `localwitness = ["` + keyA + `"]`},
			new:          []string{"a = 1", `localwitness = ["` + keyB + `"]`},
			wantDiffs:    2,
			wantRedacted: 2,
		},
		{
			// Operator added network_overrides.seeds: one extra line
			// above the assignment misaligns the positional differ, so
			// the (unchanged) key lands on both sides of a diff pair.
			name:         "line-shift-same-key",
			old:          []string{"a = 1", `localwitness = ["` + keyA + `"]`},
			new:          []string{"a = 1", "seed.node.ip.list = []", `localwitness = ["` + keyA + `"]`},
			wantDiffs:    3,
			wantRedacted: 2,
		},
		{
			// Tail truncation: the key line only exists on the old side.
			name:         "old-side-only",
			old:          []string{"a = 1", `localwitness = ["` + keyA + `"]`},
			new:          []string{"a = 1"},
			wantDiffs:    1,
			wantRedacted: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diffs := simpleHOCONDiff(tc.old, tc.new)
			assertNoKeys(t, "simpleHOCONDiff", diffs)
			if len(diffs) != tc.wantDiffs {
				t.Errorf("drift reporting changed: got %d diff lines, want %d: %v",
					len(diffs), tc.wantDiffs, diffs)
			}
			if got := countRedactedMarkers(diffs); got != tc.wantRedacted {
				t.Errorf("expected %d redacted markers, got %d: %v", tc.wantRedacted, got, diffs)
			}
		})
	}
}

// TestSimpleHOCONDiff_StillDetectsRotation guards against the opposite
// failure: redacting BEFORE comparing would make a rotated key look
// identical and silently swallow real drift.
func TestSimpleHOCONDiff_StillDetectsRotation(t *testing.T) {
	old := []string{`localwitness = ["` + keyA + `"]`}
	new := []string{`localwitness = ["` + keyB + `"]`}
	if diffs := simpleHOCONDiff(old, new); len(diffs) != 2 {
		t.Fatalf("a rotated witness key must still be reported as drift, got %v", diffs)
	}
	if diffs := simpleHOCONDiff(old, old); len(diffs) != 0 {
		t.Fatalf("an unchanged witness key must not be reported as drift, got %v", diffs)
	}
}

func TestLineDiff_RedactsWitnessKey(t *testing.T) {
	live := "a = 1\n" + `localwitness = ["` + keyA + `"]` + "\nz = 9\n"

	t.Run("key-rotation", func(t *testing.T) {
		desired := "a = 1\n" + `localwitness = ["` + keyB + `"]` + "\nz = 9\n"
		diffs := lineDiff(live, desired, 0)
		assertNoKeys(t, "lineDiff", diffs)
		if len(diffs) != 2 {
			t.Errorf("rotated key must still be reported: %v", diffs)
		}
		if got := countRedactedMarkers(diffs); got != 2 {
			t.Errorf("expected 2 redacted markers, got %d: %v", got, diffs)
		}
	})

	t.Run("line-shift", func(t *testing.T) {
		desired := "a = 1\nseed.node.ip.list = []\n" + `localwitness = ["` + keyA + `"]` + "\nz = 9\n"
		diffs := lineDiff(live, desired, 0)
		assertNoKeys(t, "lineDiff", diffs)
		if len(diffs) == 0 {
			t.Error("a line shift must still be reported as drift")
		}
	})

	// --context > 0 emits neighbouring lines that MATCH. Those come
	// from the live side and carry the real key, so they need the same
	// treatment as the changed lines.
	t.Run("context-lines", func(t *testing.T) {
		desired := "a = 1\n" + `localwitness = ["` + keyA + `"]` + "\nz = CHANGED\n"
		diffs := lineDiff(live, desired, 2)
		assertNoKeys(t, "lineDiff --context", diffs)
		joined := strings.Join(diffs, "\n")
		if !strings.Contains(joined, `  localwitness = ["<REDACTED>"]`) {
			t.Errorf("context line should be emitted redacted, got:\n%s", joined)
		}
	})

	t.Run("identical-is-in-sync", func(t *testing.T) {
		if diffs := lineDiff(live, live, 0); len(diffs) != 0 {
			t.Errorf("identical configs must stay in_sync, got %v", diffs)
		}
	})
}
