package render

import (
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

func TestParseMemoryGB(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"16GB", 16},
		{"32gb", 32},
		{"8G", 8},
		{"64g", 64},
		{"4096MB", 4},
		{"2048M", 2},
		{"2048m", 2},
		{"2.5GB", 3},
		{"1537MB", 2},
		{" 16 GB ", 16},
	}
	for _, c := range cases {
		got, err := ParseMemoryGB(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseMemoryGB(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseMemoryGBRejectsInvalidInput(t *testing.T) {
	for _, in := range []string{"", "garbage", "-4GB", "0", "0GB", "1TB", "2.5"} {
		if got, err := ParseMemoryGB(in); err == nil || got != 0 {
			t.Errorf("ParseMemoryGB(%q) = (%d, %v), want error", in, got, err)
		}
	}
}

func TestCalculateHeapMax(t *testing.T) {
	cases := []struct {
		memGB int
		want  string
	}{
		{64, "24g"},
		{96, "24g"}, // 64GB+ tier
		{32, "14g"},
		{31, "8g"}, // falls into 16GB+ tier
		{16, "8g"},
		{8, "4g"},
		{4, "2g"},
		{3, "1536m"},
		{2, "1024m"},
		{1, "512m"},
	}
	for _, c := range cases {
		got := calculateHeapMax(c.memGB, nil)
		if got != c.want {
			t.Errorf("calculateHeapMax(%dGB) = %s, want %s", c.memGB, got, c.want)
		}
	}
}

func TestJVMArgs_HeapHeadroom(t *testing.T) {
	cases := []struct {
		memGB int
		heap  string
		new   string
	}{
		{2, "1024m", "256m"},
		{1, "512m", "128m"},
		{3, "1536m", "384m"},
		{4, "2g", "512m"},
		{8, "4g", "1g"},
		{16, "8g", "2g"},
	}
	for _, tc := range cases {
		got := JVMArgsString(tc.memGB, 17, nil)
		for _, want := range []string{"-Xmx" + tc.heap, "-Xms" + tc.heap, "-Xmn" + tc.new} {
			if !strings.Contains(got, want) {
				t.Errorf("JVMArgs(%dGB) missing %q in %q", tc.memGB, want, got)
			}
		}
	}
}

func TestJVMArgs_HeapMaxOverrideRemainsVerbatim(t *testing.T) {
	got := JVMArgsString(2, 17, &intent.JVMConfig{HeapMax: "1536m"})
	if !strings.Contains(got, "-Xmx1536m") || !strings.Contains(got, "-Xms1536m") {
		t.Errorf("heap_max override not applied verbatim: %q", got)
	}
}

func TestJVMArgs_MBMemoryInput(t *testing.T) {
	memGB, err := ParseMemoryGB("2048m")
	if err != nil {
		t.Fatalf("ParseMemoryGB: %v", err)
	}
	got := JVMArgsString(memGB, 17, nil)
	if !strings.Contains(got, "-Xmx1024m") || !strings.Contains(got, "-Xms1024m") {
		t.Errorf("2048m input produced unsafe heap: %q", got)
	}
}

func TestCalculateHeapMax_Override(t *testing.T) {
	jvm := &intent.JVMConfig{HeapMax: "12g"}
	if got := calculateHeapMax(32, jvm); got != "12g" {
		t.Errorf("override ignored: got %s", got)
	}
}

func TestSelectGC(t *testing.T) {
	cases := []struct {
		jdk  int
		want string
	}{
		{8, "CMS"},
		{11, "CMS"},
		{17, "G1"},
		{21, "G1"},
	}
	for _, c := range cases {
		if got := selectGC(c.jdk, nil); got != c.want {
			t.Errorf("selectGC(jdk=%d) = %s, want %s", c.jdk, got, c.want)
		}
	}
}

func TestSelectGC_Override(t *testing.T) {
	jvm := &intent.JVMConfig{GC: "G1"}
	if got := selectGC(8, jvm); got != "G1" {
		t.Errorf("override ignored: got %s", got)
	}
	jvm.GC = "auto"
	if got := selectGC(8, jvm); got != "CMS" {
		t.Errorf("auto should delegate to default: got %s", got)
	}
}

func TestJVMArgs_DefaultIsHeapOnly(t *testing.T) {
	// With nil JVM config, GC selection and GC logging stay off so the args
	// don't collide with the java-tron image's own start.sh tuning.
	args := JVMArgs(32, 17, nil)
	joined := strings.Join(args, " ")

	for _, want := range []string{"-Xmx14g", "-Xms14g", "-XX:+HeapDumpOnOutOfMemoryError"} {
		if !strings.Contains(joined, want) {
			t.Errorf("JVMArgs missing %q in: %s", want, joined)
		}
	}
	for _, unwanted := range []string{"-XX:+UseG1GC", "-XX:+UseConcMarkSweepGC", "-Xlog:gc", "gc.log"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("JVMArgs should not include %q by default: %s", unwanted, joined)
		}
	}
}

func TestJVMArgs_GCOptIn(t *testing.T) {
	args := JVMArgs(32, 17, &intent.JVMConfig{GC: "G1"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-XX:+UseG1GC") {
		t.Errorf("opt-in G1 not emitted: %s", joined)
	}
}

func TestJVMArgs_GCAutoStaysOff(t *testing.T) {
	args := JVMArgs(32, 17, &intent.JVMConfig{GC: "auto"})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-XX:+UseG1GC") || strings.Contains(joined, "-XX:+UseConcMarkSweepGC") {
		t.Errorf("auto should stay off: %s", joined)
	}
}

func TestJVMArgs_GCLogOptIn(t *testing.T) {
	on := intent.BoolPtr(true)
	args := JVMArgs(16, 17, &intent.JVMConfig{GCLog: on})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-Xlog:gc") {
		t.Errorf("GC log should be enabled when opted in: %s", joined)
	}
}

func TestJVMArgsString_SpaceJoined(t *testing.T) {
	s := JVMArgsString(16, 17, nil)
	if !strings.HasPrefix(s, "-Xmx") {
		t.Errorf("unexpected prefix: %s", s)
	}
	if strings.Contains(s, "  ") {
		t.Errorf("double space in args: %s", s)
	}
}
