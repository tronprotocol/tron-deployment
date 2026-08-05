package render

import (
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

// TestJVMArgs_ExtraOptsAppendedLast pins the ordering contract. The JVM takes
// the LAST occurrence of -XX:± and -D, so an operator override only works if
// extra_opts lands after everything trond derives. Appending it earlier would
// make the field look wired while trond's own flag silently won.
func TestJVMArgs_ExtraOptsAppendedLast(t *testing.T) {
	args := JVMArgs(32, 17, &intent.JVMConfig{
		GC:        "G1",
		ExtraOpts: []string{"-Dio.netty.allocator.type=pooled", "-XX:+UseTLAB"},
	})

	if len(args) < 2 {
		t.Fatalf("too few args: %v", args)
	}
	lasttwo := args[len(args)-2:]
	want := []string{"-Dio.netty.allocator.type=pooled", "-XX:+UseTLAB"}
	for i := range want {
		if lasttwo[i] != want[i] {
			t.Errorf("args[%d] from the end = %q, want %q (full: %v)",
				len(want)-i, lasttwo[i], want[i], args)
		}
	}
}

// TestJVMArgs_ExtraOptsOverrideDerived is the reason the field exists: the
// duplicate must come after trond's own, so the JVM resolves to the
// operator's value.
func TestJVMArgs_ExtraOptsOverrideDerived(t *testing.T) {
	args := JVMArgs(32, 17, &intent.JVMConfig{
		ExtraOpts: []string{"-XX:-UseTLAB"},
	})

	firstTLAB, lastTLAB := -1, -1
	for i, a := range args {
		if strings.HasSuffix(a, "UseTLAB") {
			if firstTLAB == -1 {
				firstTLAB = i
			}
			lastTLAB = i
		}
	}
	if firstTLAB == lastTLAB {
		t.Fatalf("expected both trond's -XX:+UseTLAB and the override: %v", args)
	}
	if args[lastTLAB] != "-XX:-UseTLAB" {
		t.Errorf("last UseTLAB flag = %q, want the operator's -XX:-UseTLAB; "+
			"trond's derived value would win instead", args[lastTLAB])
	}
}

// TestJVMArgs_NoExtraOptsUnchanged is the regression guard: the field is
// optional and absent in every shipped example, so its introduction must not
// move a single byte of the default render.
func TestJVMArgs_NoExtraOptsUnchanged(t *testing.T) {
	withNil := JVMArgsString(32, 17, nil)
	withEmpty := JVMArgsString(32, 17, &intent.JVMConfig{})
	if strings.Contains(withNil, "-D") {
		t.Errorf("default render gained a -D flag: %s", withNil)
	}
	if !strings.HasSuffix(withEmpty, "-XX:+UseTLAB") {
		t.Errorf("render no longer ends with the common flags: %s", withEmpty)
	}
}

// TestJVMArgs_TheNettyAllocatorCase is the concrete motivating scenario:
// java-tron sets this in gradle/java-tron.vmoptions, which its distribution
// launcher reads and trond never does, so without extra_opts a trond-deployed
// node runs on netty 4.2's adaptive allocator that upstream opted out of.
func TestJVMArgs_TheNettyAllocatorCase(t *testing.T) {
	got := JVMArgsString(16, 17, &intent.JVMConfig{
		ExtraOpts: []string{"-Dio.netty.allocator.type=pooled"},
	})
	if !strings.Contains(got, "-Dio.netty.allocator.type=pooled") {
		t.Errorf("allocator flag absent from rendered args: %s", got)
	}
	// One argument, not two — JVMArgsString joins with spaces.
	if n := strings.Count(got, "-Dio.netty.allocator.type=pooled"); n != 1 {
		t.Errorf("allocator flag appears %d times, want 1: %s", n, got)
	}
}
