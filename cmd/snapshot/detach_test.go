package snapshot

import (
	"reflect"
	"testing"
)

func TestStripDetach(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "no detach",
			in:   []string{"trond", "snapshot", "download", "--network", "mainnet"},
			want: []string{"trond", "snapshot", "download", "--network", "mainnet"},
		},
		{
			name: "bare --detach",
			in:   []string{"trond", "snapshot", "download", "--detach", "--network", "mainnet"},
			want: []string{"trond", "snapshot", "download", "--network", "mainnet"},
		},
		{
			name: "single-dash -detach",
			in:   []string{"trond", "snapshot", "download", "-detach"},
			want: []string{"trond", "snapshot", "download"},
		},
		{
			name: "--detach=true",
			in:   []string{"trond", "--detach=true", "snapshot", "download"},
			want: []string{"trond", "snapshot", "download"},
		},
		{
			name: "--detach=false (still stripped — child must run foreground)",
			in:   []string{"trond", "snapshot", "download", "--detach=false"},
			want: []string{"trond", "snapshot", "download"},
		},
		{
			name: "--detach=1",
			in:   []string{"trond", "snapshot", "download", "--detach=1"},
			want: []string{"trond", "snapshot", "download"},
		},
		{
			name: "--detach=0",
			in:   []string{"trond", "snapshot", "download", "--detach=0"},
			want: []string{"trond", "snapshot", "download"},
		},
		{
			name: "exact --detach= (malformed but defensively stripped)",
			in:   []string{"trond", "snapshot", "download", "--detach="},
			want: []string{"trond", "snapshot", "download"},
		},
		{
			name: "preserves other --network=value style flags",
			in:   []string{"trond", "snapshot", "download", "--detach", "--network=mainnet", "--type=lite"},
			want: []string{"trond", "snapshot", "download", "--network=mainnet", "--type=lite"},
		},
		{
			name: "--detach in middle of args",
			in:   []string{"trond", "--state-dir", "/tmp", "snapshot", "download", "--detach", "--to", "/data"},
			want: []string{"trond", "--state-dir", "/tmp", "snapshot", "download", "--to", "/data"},
		},
		{
			// The detached child is the process that actually fetches the
			// sidecar and refuses to extract without it, so the operator's
			// deliberate opt-out has to survive the re-exec.
			name: "preserves --no-verify so the opt-out reaches the detached child",
			in:   []string{"trond", "snapshot", "download", "--detach", "--no-verify", "--network", "nile"},
			want: []string{"trond", "snapshot", "download", "--no-verify", "--network", "nile"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stripDetach(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("stripDetach(%v)\n  got:  %v\n  want: %v", c.in, got, c.want)
			}
		})
	}
}

func TestStripDetach_DoesNotPanicOnEmpty(t *testing.T) {
	got := stripDetach(nil)
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
	got = stripDetach([]string{})
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}
