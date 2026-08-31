package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/render"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// `trond verify-config <node> --intent <path>` answers the question:
// is the .conf currently in use by the running container the same
// thing trond would produce from the supplied intent.yaml *now*?
//
// Why this matters: an operator may have run `docker exec <node> vi
// /java-tron/conf/<name>.conf` to test a setting, or someone changed
// the intent.yaml since the last apply but never re-applied. Either
// way, agents reconciling desired-vs-actual state need a signal —
// without verify-config they'd have to call apply just to see if
// anything would change (and apply has the HUMAN_REQUIRED gate, so
// it's an expensive way to ask).
//
// This command is read-only: never modifies the running container,
// state, or the on-disk deployments dir. Output schema:
// schemas/output/verify-config.schema.json.

var (
	verifyConfigIntentPath string
	verifyConfigContext    int
)

var verifyConfigCmd = &cobra.Command{
	Use:   "verify-config <node>",
	Short: "Compare a running node's live config against the latest intent",
	Long: `Compare the .conf actively used by a managed node's runtime against
the HOCON trond would render right now from --intent.

Agents reconciling desired-vs-actual state read the in_sync field and
the diffs[] array to decide whether a re-apply is warranted. Read-only:
never mutates the container, state, or on-disk artifacts.`,
	Args: cobra.ExactArgs(1),
	RunE: runVerifyConfig,
}

func init() {
	verifyConfigCmd.Flags().StringVar(&verifyConfigIntentPath, "intent", "",
		"Path to the intent.yaml to render against (required)")
	verifyConfigCmd.Flags().IntVar(&verifyConfigContext, "context", 0,
		"Context lines to include around each diff (0 = no context, just changed lines)")
	mustMarkRequired(verifyConfigCmd, "intent")
	rootCmd.AddCommand(verifyConfigCmd)
}

func runVerifyConfig(cmd *cobra.Command, args []string) error {
	name := args[0]

	parsed, err := intent.Load(verifyConfigIntentPath)
	if err != nil {
		return exitWithError("VALIDATION_ERROR", output.ExitValidationError, err.Error(),
			"Run: trond config validate "+verifyConfigIntentPath)
	}
	// We don't enforce parsed.Name == name — agents legitimately
	// rename intents but keep the running node alive; they pass the
	// running node's actual name as the positional arg and the new
	// intent as --intent to detect that very rename.

	nc, err := resolveNodeContext(name)
	if err != nil {
		return err
	}
	defer nc.Close()

	// Pull the live conf out of the container. For docker runtime
	// the conf path is /java-tron/conf/<name>.conf; for jar runtime
	// the binary's working dir holds it under conf/.
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()
	live, err := target.ReadLiveConfig(ctx, nc.Target, nc.Node)
	if err != nil && nc.Node.Runtime == "jar" {
		path := filepath.Join(nc.Node.InstallPath, "conf", name+".conf")
		err = fmt.Errorf("read %s: %w", path, err)
	} else if err != nil {
		err = fmt.Errorf("docker exec cat: %w", err)
	}
	if err != nil {
		return exitWithError("LIVE_CONFIG_UNREACHABLE", output.ExitGeneralError, err.Error(),
			"Confirm the node is running: trond status "+name,
			"Confirm the runtime: trond inspect "+name)
	}

	// Render what trond *would* produce from the current intent. The
	// comparison runs against the REAL bytes (redacting here would make
	// every witness node report permanent false drift against its live
	// conf); render.DiffText redacts each line as it emits it.
	renderedDesired, err := render.RenderHOCONWithSecrets(apply.FindTemplatesDir(), parsed, &parsed.Nodes[0])
	if err != nil {
		return exitWithError("RENDER_ERROR", output.ExitGeneralError, err.Error())
	}
	desired := renderedDesired.Deployable()

	diffs := render.DiffText(live, desired, verifyConfigContext)
	result := map[string]any{
		"name":          name,
		"intent":        parsed.Name,
		"intent_path":   verifyConfigIntentPath,
		"in_sync":       len(diffs) == 0,
		"live_lines":    countLines(live),
		"desired_lines": countLines(desired),
		"diff_count":    len(diffs),
		"diffs":         diffs,
	}

	outputFmt, _ := cmd.Flags().GetString("output")
	if outputFmt == "json" {
		return output.WriteJSON(os.Stdout, result)
	}
	if len(diffs) == 0 {
		fmt.Printf("✓ %s in sync with %s\n", name, verifyConfigIntentPath)
		return nil
	}
	fmt.Printf("✗ %s drift from %s (%d changed line(s)):\n",
		name, verifyConfigIntentPath, len(diffs))
	for _, d := range diffs {
		fmt.Println(d)
	}
	return nil
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
