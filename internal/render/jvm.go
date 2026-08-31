package render

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

// ParseMemoryGB converts an intent memory string into whole gigabytes.
// GB values and MB values are rounded up, so the result never silently
// requests less memory than the user specified (for example, 2.5GB is 3GB
// and 1537MB is 2GB). Invalid, empty, zero, and negative values return an
// error; callers must not replace a bad value with a default.
func ParseMemoryGB(s string) (int, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, fmt.Errorf("memory must not be empty")
	}
	unit := "GB"
	hasUnit := false
	for _, u := range []string{"GB", "MB", "G", "M"} {
		if strings.HasSuffix(s, u) {
			hasUnit = true
			unit = u
			s = strings.TrimSuffix(s, u)
			break
		}
	}
	if !hasUnit && strings.Contains(s, ".") {
		return 0, fmt.Errorf("fractional memory values require a GB unit")
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return 0, fmt.Errorf("invalid memory %q", s)
	}
	switch unit {
	case "GB", "G":
		return int(math.Ceil(n)), nil
	case "MB", "M":
		return int(math.Ceil(n / 1024)), nil
	}
	return 0, fmt.Errorf("invalid memory unit in %q", s)
}

// JVMArgs generates JVM command-line arguments based on system memory and intent overrides.
//
// By default we only emit heap sizing — the official java-tron Docker image's
// start.sh adds its own GC flags, and emitting ours unconditionally produces
// "Multiple garbage collectors selected" and a restart loop. GC tuning and GC
// logging are therefore opt-in: a node only gets them when intent.jvm.gc or
// intent.jvm.gc_log are explicitly set. The jar runtime path (no upstream
// start.sh) still gets safe defaults via the explicit-config call sites.
func JVMArgs(totalMemoryGB int, jdkVersion int, jvm *intent.JVMConfig) []string {
	var args []string

	// Heap sizing — always safe to emit; java-tron's start.sh defers to JAVA_OPTS.
	heapMax := calculateHeapMax(totalMemoryGB, jvm)
	heapNew := calculateHeapNew(heapMax, jvm)
	directMemory := calculateDirectMemory(heapMax, jvm)

	args = append(args, fmt.Sprintf("-Xmx%s", heapMax))
	args = append(args, fmt.Sprintf("-Xms%s", heapMax))
	args = append(args, fmt.Sprintf("-Xmn%s", heapNew))
	args = append(args, fmt.Sprintf("-XX:MaxDirectMemorySize=%s", directMemory))

	// GC tuning is opt-in. "auto" is treated as "let the image / runtime decide".
	if jvm != nil && jvm.GC != "" && jvm.GC != "auto" {
		args = append(args, gcArgs(jvm.GC, jdkVersion)...)
	}

	// GC logging is opt-in: only when explicitly set to true.
	if jvm != nil && jvm.GCLog != nil && *jvm.GCLog {
		args = append(args, gcLogArgs(jdkVersion)...)
	}

	// Common JVM flags — heap dump on OOM is universally safe.
	args = append(args,
		"-XX:+HeapDumpOnOutOfMemoryError",
		"-XX:+UseTLAB",
	)

	// Operator escape hatch, appended LAST so it wins: the JVM takes the
	// final occurrence for -XX:± and -D, so an extra_opt can override
	// anything trond derived above. Entries are shape-checked at load
	// time (intent.validateJVMExtraOpt) — restricted to -D<k>=<v> and
	// -XX:…, no whitespace, no quoting characters — so appending them
	// verbatim cannot add an argument or break the compose/systemd line.
	if jvm != nil {
		args = append(args, jvm.ExtraOpts...)
	}

	return args
}

// calculateHeapMax determines -Xmx based on total system memory.
// Logic ported from start.sh: 14g for 32GB+, 8g for 16GB+, 4g for 8GB+, etc.
func calculateHeapMax(totalMemoryGB int, jvm *intent.JVMConfig) string {
	if jvm != nil && jvm.HeapMax != "" {
		return jvm.HeapMax
	}

	var heapMax string
	switch {
	case totalMemoryGB >= 64:
		heapMax = "24g"
	case totalMemoryGB >= 32:
		heapMax = "14g"
	case totalMemoryGB >= 16:
		heapMax = "8g"
	case totalMemoryGB >= 8:
		heapMax = "4g"
	default:
		heapMax = "2g"
	}

	// Keep native headroom for ZGC in the container. For sub-8GB limits,
	// cap the start.sh tier at half the limit, computed in whole MB so 3GB
	// becomes 1536m rather than truncating to 1g.
	if totalMemoryGB > 0 && totalMemoryGB < 8 {
		halfMB := totalMemoryGB * 1024 / 2
		if halfMB < 2*1024 {
			return fmt.Sprintf("%dm", halfMB)
		}
	}
	return heapMax
}

func calculateHeapNew(heapMax string, jvm *intent.JVMConfig) string {
	if jvm != nil && jvm.HeapNew != "" {
		return jvm.HeapNew
	}

	// HeapNew ≈ HeapMax / 4
	switch heapMax {
	case "512m":
		return "128m"
	case "1024m":
		return "256m"
	case "1536m":
		return "384m"
	case "24g":
		return "6g"
	case "14g":
		return "3g"
	case "8g":
		return "2g"
	case "4g":
		return "1g"
	default:
		return "512m"
	}
}

func calculateDirectMemory(heapMax string, jvm *intent.JVMConfig) string {
	if jvm != nil && jvm.DirectMemory != "" {
		return jvm.DirectMemory
	}

	// DirectMemory ≈ HeapMax / 14, minimum 1g
	switch heapMax {
	case "24g":
		return "2g"
	case "14g":
		return "1g"
	case "8g":
		return "1g"
	case "4g":
		return "512m"
	default:
		return "256m"
	}
}

func selectGC(jdkVersion int, jvm *intent.JVMConfig) string {
	if jvm != nil && jvm.GC != "" && jvm.GC != "auto" {
		return jvm.GC
	}
	// G1 for JDK 17+, CMS for JDK 8
	if jdkVersion >= 17 {
		return "G1"
	}
	return "CMS"
}

func gcArgs(gc string, jdkVersion int) []string {
	switch gc {
	case "G1":
		return []string{
			"-XX:+UseG1GC",
			"-XX:G1HeapRegionSize=16m",
			"-XX:G1RSetUpdatingPauseTimePercent=5",
			"-XX:InitiatingHeapOccupancyPercent=35",
			"-XX:MaxGCPauseMillis=200",
		}
	case "CMS":
		args := []string{
			"-XX:+UseConcMarkSweepGC",
			"-XX:+CMSParallelRemarkEnabled",
			"-XX:+CMSScavengeBeforeRemark",
			"-XX:CMSInitiatingOccupancyFraction=70",
			"-XX:+UseCMSInitiatingOccupancyOnly",
		}
		if jdkVersion == 8 {
			args = append(args, "-XX:NewRatio=2")
		}
		return args
	default:
		return []string{"-XX:+UseG1GC"}
	}
}

func gcLogArgs(jdkVersion int) []string {
	if jdkVersion >= 9 {
		// JDK 9+ unified logging
		return []string{
			"-Xlog:gc*:file=gc.log:time,uptime,level,tags:filecount=10,filesize=100m",
		}
	}
	// JDK 8 GC logging
	return []string{
		"-XX:+PrintGCDetails",
		"-Xloggc:gc.log",
		"-XX:+PrintGCDateStamps",
	}
}

// JVMArgsString returns JVM args as a single space-separated string.
func JVMArgsString(totalMemoryGB int, jdkVersion int, jvm *intent.JVMConfig) string {
	return strings.Join(JVMArgs(totalMemoryGB, jdkVersion, jvm), " ")
}
