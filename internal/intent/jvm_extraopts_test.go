package intent

import (
	"strings"
	"testing"
)

// TestValidateJVMExtraOpt_Accepts covers the tuning surface the field exists
// for. Anything here must survive to the rendered command line untouched.
func TestValidateJVMExtraOpt_Accepts(t *testing.T) {
	for _, ok := range []string{
		"-Dio.netty.allocator.type=pooled",
		"-Dsun.net.inetaddr.ttl=0",
		"-Dfile.encoding=UTF-8",
		"-Dempty.value=",
		"-XX:+UseZGC",
		"-XX:-UseTLAB",
		"-XX:MaxDirectMemorySize=2g",
		"-XX:ActiveProcessorCount=4",
	} {
		if err := validateJVMExtraOpt(0, 0, ok); err != nil {
			t.Errorf("validateJVMExtraOpt(%q) = %v, want nil", ok, err)
		}
	}
}

// TestValidateJVMExtraOpt_RejectsWhitespace is the load-bearing one.
// render.JVMArgs joins the set with spaces, so an entry holding a space does
// not stay one argument — it becomes two, and a second flag rides in looking
// like part of an innocuous property.
func TestValidateJVMExtraOpt_RejectsWhitespace(t *testing.T) {
	sneaky := []string{
		"-Dfoo=bar -XX:+UnlockDiagnosticVMOptions",
		"-Dfoo=bar\t-XX:+UnlockDiagnosticVMOptions",
		"-XX:+UseG1GC -javaagent:/tmp/evil.jar",
		" -Dfoo=bar",
	}
	for _, s := range sneaky {
		err := validateJVMExtraOpt(0, 0, s)
		if err == nil {
			t.Errorf("validateJVMExtraOpt(%q) = nil; a whitespace entry smuggles a second JVM argument", s)
			continue
		}
		if !strings.Contains(err.Error(), "whitespace") {
			t.Errorf("validateJVMExtraOpt(%q) error should name whitespace, got: %v", s, err)
		}
	}
}

// TestValidateJVMExtraOpt_RejectsCodeLoading — the allowlist is by
// construction, not by blocklist, so these fail for being outside -D/-XX:
// rather than for being individually named.
func TestValidateJVMExtraOpt_RejectsCodeLoading(t *testing.T) {
	for _, bad := range []string{
		"-javaagent:/tmp/evil.jar",
		"-agentlib:jdwp=transport=dt_socket,server=y,address=5005",
		"-cp",
		"-classpath:/tmp/evil",
		"@/tmp/argfile",
		"-jar",
		"--add-opens=java.base/java.lang=ALL-UNNAMED",
		"-Xshare:off",
	} {
		if err := validateJVMExtraOpt(0, 0, bad); err == nil {
			t.Errorf("validateJVMExtraOpt(%q) = nil, want refusal", bad)
		}
	}
}

// TestValidateJVMExtraOpt_RejectsQuotingChars guards the two render targets:
// the compose `command:` list entry and the systemd ExecStart line.
func TestValidateJVMExtraOpt_RejectsQuotingChars(t *testing.T) {
	for _, bad := range []string{
		`-Dfoo="bar"`,
		"-Dfoo='bar'",
		`-Dfoo=bar\baz`,
		"-Dfoo=$(id)",
		"-Dfoo=`id`",
	} {
		if err := validateJVMExtraOpt(0, 0, bad); err == nil {
			t.Errorf("validateJVMExtraOpt(%q) = nil; would not survive compose/systemd rendering intact", bad)
		}
	}
}

// TestValidateJVMExtraOpt_RejectsMalformed covers the -D shape itself.
func TestValidateJVMExtraOpt_RejectsMalformed(t *testing.T) {
	for _, bad := range []string{
		"",
		"-D",
		"-Dnokey",    // no '='
		"-D=novalue", // empty key
		"-XX:",
	} {
		if err := validateJVMExtraOpt(0, 0, bad); err == nil {
			t.Errorf("validateJVMExtraOpt(%q) = nil, want refusal", bad)
		}
	}
}

// TestValidateJVMExtraOpt_ErrorNamesTheEntry — with a list of flags on a
// multi-node intent, an error that does not say which one is nearly useless.
func TestValidateJVMExtraOpt_ErrorNamesTheEntry(t *testing.T) {
	err := validateJVMExtraOpt(2, 3, "-javaagent:/tmp/x.jar")
	if err == nil {
		t.Fatal("expected refusal")
	}
	if !strings.Contains(err.Error(), "nodes[2].jvm.extra_opts[3]") {
		t.Errorf("error does not locate the entry: %v", err)
	}
}

// TestCheckSafeStrings_JVMExtraOptsControlChars confirms the field is wired
// into the control-char sweep too, not only the shape check — a newline would
// inject a line into the systemd unit.
func TestCheckSafeStrings_JVMExtraOptsControlChars(t *testing.T) {
	n := &NodeSpec{JVM: &JVMConfig{ExtraOpts: []string{"-Dfoo=bar\nExecStartPre=/bin/evil"}}}
	if err := checkSafeStrings(0, n); err == nil {
		t.Error("newline in jvm.extra_opts accepted; it would inject a systemd directive")
	}
}
