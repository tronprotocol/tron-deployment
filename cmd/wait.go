package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// waitCmd is the generic readiness primitive. Test harnesses use it to
// block until a deployed node satisfies a probe before kicking off
// assertions, traffic, or the next deploy step.
//
// Three probe families, mutually exclusive:
//
//	trond wait <node> --port 8090            — TCP connect succeeds
//	trond wait <node> --http <url>           — HTTP 2xx (optional --json-eq)
//	trond wait <node> --exec '<cmd>'         — command exits 0 inside container
//
// All probes share --timeout, --interval, and emit a JSON record on success.
var waitCmd = &cobra.Command{
	Use:   "wait <node>",
	Short: "Block until a node satisfies a probe",
	Long: `Poll a probe against a managed node until it succeeds or --timeout elapses.

Probes (one of):
  --port <n>             TCP connect to 127.0.0.1:<n> on the trond host
                         (i.e. the host-mapped port for docker nodes)
  --http <url>           HTTP GET via curl run inside the node;
                         "{http}" expands to the node's HTTP endpoint;
                         combine with --json-path / --json-eq / --json-gt
  --exec '<cmd>'         shell command run inside the node; success when exit 0

Options:
  --timeout <duration>   Total wait budget (default 5m)
  --interval <duration>  Poll interval (default 2s)
  --json-path <path>     dotted path into the HTTP response JSON (e.g. "block_header.raw_data.number")
  --json-eq <value>      success when --json-path equals this string value
  --json-gt <number>     success when --json-path is numerically greater than this`,
	Args: cobra.ExactArgs(1),
	RunE: runWait,
}

var (
	waitPort      int
	waitHTTP      string
	waitExec      string
	waitTimeout   time.Duration
	waitInterval  time.Duration
	waitJSONPath  string
	waitJSONEq    string
	waitJSONGt    float64
	waitJSONGtSet bool
)

func init() {
	waitCmd.Flags().IntVar(&waitPort, "port", 0, "TCP port to probe inside the node")
	waitCmd.Flags().StringVar(&waitHTTP, "http", "", "HTTP URL to probe; {http} expands to the node's http endpoint")
	waitCmd.Flags().StringVar(&waitExec, "exec", "", "Shell command; success on exit 0")
	waitCmd.Flags().DurationVar(&waitTimeout, "timeout", 5*time.Minute, "Total wait budget")
	waitCmd.Flags().DurationVar(&waitInterval, "interval", 2*time.Second, "Poll interval")
	waitCmd.Flags().StringVar(&waitJSONPath, "json-path", "", "Dotted path into HTTP response JSON")
	waitCmd.Flags().StringVar(&waitJSONEq, "json-eq", "", "With --json-path: success when path == value")
	// json-gt is set via Float64Var directly; pflag doesn't track "was it
	// provided" so we mirror with a sibling bool registered by visiting.
	waitCmd.Flags().Float64Var(&waitJSONGt, "json-gt", 0, "With --json-path: success when path > number")
	rootCmd.AddCommand(waitCmd)
}

func runWait(cmd *cobra.Command, args []string) error {
	nodeName := args[0]
	outputFmt, _ := cmd.Flags().GetString("output")

	// Detect "was --json-gt actually passed?" so 0 isn't ambiguous with unset.
	waitJSONGtSet = cmd.Flag("json-gt").Changed

	// Validate that exactly one probe family is specified.
	probeCount := 0
	if waitPort != 0 {
		probeCount++
	}
	if waitHTTP != "" {
		probeCount++
	}
	if waitExec != "" {
		probeCount++
	}
	if probeCount != 1 {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			"specify exactly one of --port, --http, --exec")
	}

	// --exec runs `sh -c <caller string>` on the node (probeExec below), so
	// under the gate it is the same capability as `trond exec` and takes
	// the same refusal. --port and --http stay allowed: those are read
	// probes, and waiting for a mainnet node to come up is exactly the
	// non-mutating call the gate is documented to permit (same reasoning
	// as `auto-heal --dry-run`).
	//
	// Ahead of resolveNodeContext, per requirePrivateForNode's contract.
	if waitExec != "" {
		if err := requirePrivateForNode(nodeName); err != nil {
			return err
		}
	}

	nc, err := resolveNodeContext(nodeName)
	if err != nil {
		return err
	}
	defer nc.Close()

	ctx, cancel := context.WithTimeout(cmd.Context(), waitTimeout)
	defer cancel()

	start := time.Now()
	attempts := 0

	for {
		attempts++
		var probeErr error
		switch {
		case waitPort != 0:
			probeErr = probeTCP(ctx, nc.Target, waitPort)
		case waitHTTP != "":
			probeErr = probeHTTP(ctx, nc, expandHTTPURL(waitHTTP, nc.Node.HTTPPort))
		case waitExec != "":
			probeErr = probeExec(ctx, nc, waitExec)
		}

		if probeErr == nil {
			result := map[string]any{
				"name":        nodeName,
				"ready":       true,
				"attempts":    attempts,
				"duration_ms": time.Since(start).Milliseconds(),
			}
			if outputFmt == "json" {
				return output.WriteJSON(os.Stdout, result)
			}
			fmt.Fprintf(os.Stderr, "ready after %d attempts (%s)\n", attempts, time.Since(start).Round(time.Millisecond))
			return nil
		}

		select {
		case <-ctx.Done():
			return output.NewError("WAIT_TIMEOUT", output.ExitGeneralError,
				fmt.Sprintf("%s not ready after %s (last error: %v)", nodeName, waitTimeout, probeErr)).
				WithSuggestions("Run: trond logs "+nodeName, "Run: trond diagnose "+nodeName)
		case <-time.After(waitInterval):
		}
	}
}

// expandHTTPURL substitutes the {http} placeholder with the node's HTTP
// endpoint (host:port). Lets harnesses write generic probes once:
//
//	trond wait $name --http '{http}/wallet/getnowblock'
func expandHTTPURL(raw string, port int) string {
	if !strings.Contains(raw, "{http}") {
		return raw
	}
	if port == 0 {
		port = 8090
	}
	return strings.ReplaceAll(raw, "{http}", fmt.Sprintf("http://127.0.0.1:%d", port))
}

func probeTCP(ctx context.Context, _ target.Target, port int) error {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// probeHTTP runs curl inside the node via Target.Exec so the probe sees the
// container's network. For local docker nodes this routes through `docker
// exec`; for jar / SSH nodes it runs on the target host directly.
func probeHTTP(ctx context.Context, nc *nodeContext, url string) error {
	args := []string{"-fsS", "--max-time", "5", url}
	out, err := nc.runtimeExec(ctx, "curl", args...)
	if err != nil {
		return fmt.Errorf("curl: %w", err)
	}
	if waitJSONPath == "" {
		return nil // any 2xx counts
	}
	val, err := jsonPath(out, waitJSONPath)
	if err != nil {
		return fmt.Errorf("json path %q: %w", waitJSONPath, err)
	}
	switch {
	case waitJSONEq != "":
		if fmt.Sprintf("%v", val) != waitJSONEq {
			return fmt.Errorf("path %s = %v, want %s", waitJSONPath, val, waitJSONEq)
		}
	case waitJSONGtSet:
		num, ok := numericValue(val)
		if !ok {
			return fmt.Errorf("path %s = %v, not numeric", waitJSONPath, val)
		}
		if num <= waitJSONGt {
			return fmt.Errorf("path %s = %v, want > %v", waitJSONPath, num, waitJSONGt)
		}
	}
	return nil
}

func probeExec(ctx context.Context, nc *nodeContext, command string) error {
	_, err := nc.runtimeExec(ctx, "sh", "-c", command)
	return err
}

// jsonPath does a dotted lookup into an arbitrary JSON document. Array
// indexing is not supported (yet) — keep the surface tiny; harnesses can
// post-process via jq for anything fancier.
func jsonPath(data []byte, path string) (any, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	for key := range strings.SplitSeq(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("path segment %q applied to non-object", key)
		}
		v, ok = m[key]
		if !ok {
			return nil, fmt.Errorf("missing key %q", key)
		}
	}
	return v, nil
}

func numericValue(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}
