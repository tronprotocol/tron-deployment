package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/render"
)

var (
	renderOutputDir  string
	renderOverlay    string
	renderNodeFilter int
)

var renderCmd = &cobra.Command{
	Use:   "render <intent-path>",
	Short: "Render configuration from an intent file",
	Long: `Render HOCON config and docker-compose/systemd files from an intent.

  trond config render foo.yaml                   # all nodes, stdout
  trond config render foo.yaml --output-dir out  # all nodes, files
  trond config render foo.yaml --node 1          # only the second node
  trond config render base.yaml --overlay env.yaml   # merge env on top
  trond config render foo.yaml -o json           # structured payload`,
	Args: cobra.ExactArgs(1),
	RunE: runRender,
}

func init() {
	renderCmd.Flags().StringVar(&renderOutputDir, "output-dir", "", "Directory to write rendered files (default: stdout)")
	renderCmd.Flags().StringVar(&renderOverlay, "overlay", "", "Second intent merged on top of the primary one")
	renderCmd.Flags().IntVar(&renderNodeFilter, "node", -1, "Render only the node at this index (default: all)")
}

// renderedNode is what `config render -o json` emits per node. Field
// names are stable; consumers can rely on missing strings meaning
// "not produced for this runtime" (e.g. compose stays empty for jar
// runtime, systemd stays empty for docker).
type renderedNode struct {
	Index    int    `json:"index"`
	NodeName string `json:"name"`
	Type     string `json:"type"`
	HOCON    string `json:"hocon"`
	Compose  string `json:"compose,omitempty"`
	Systemd  string `json:"systemd,omitempty"`
	JVMArgs  string `json:"jvm_args"`
	// Redacted marks a node whose witness private key was replaced by
	// a `<REDACTED:ENV_NAME>` placeholder in HOCON. The rendered config
	// is then a PREVIEW: java-tron rejects the placeholder, so the file
	// written by --output-dir must not be deployed as-is. Deploy with
	// `trond apply`, which inlines the real key without printing it.
	Redacted bool `json:"redacted,omitempty"`
}

func runRender(cmd *cobra.Command, args []string) error {
	intentPath := args[0]
	outputFmt, _ := cmd.Flags().GetString("output")

	var parsed *intent.Intent
	var err error
	if renderOverlay != "" {
		parsed, err = intent.LoadWithOverlay(intentPath, renderOverlay)
	} else {
		parsed, err = intent.Load(intentPath)
	}
	if err != nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
	}

	// Find templates directory (relative to binary or working directory)
	templateDir := apply.FindTemplatesDir()

	rendered := make([]renderedNode, 0, len(parsed.Nodes))
	anyRedacted := false

	for i, node := range parsed.Nodes {
		if renderNodeFilter >= 0 && i != renderNodeFilter {
			continue
		}
		// Render HOCON config. This is a PREVIEW surface — the result
		// is printed to stdout, inlined into the JSON payload and
		// written to --output-dir — so we take the redacted display
		// form, never the deployable one.
		r, err := render.RenderHOCONWithSecrets(templateDir, parsed, &node)
		if err != nil {
			return output.NewError("RENDER_ERROR", output.ExitGeneralError, err.Error())
		}
		hocon := r.Config
		if r.Redacted {
			anyRedacted = true
		}

		// Render JVM args. Without a live target we can't probe JDK or real
		// host memory, so we size from the intent's resources.memory and
		// default to JDK 17 — both are safe static assumptions for the
		// `config render` preview path.
		memGB, err := render.ParseMemoryGB(node.Resources.Memory)
		if err != nil {
			return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
				fmt.Sprintf("invalid resources.memory %q: %v", node.Resources.Memory, err))
		}
		jvmArgs := render.JVMArgsString(memGB, 17, node.JVM)

		// Render runtime artifacts
		var composeYAML, systemdUnit string
		if parsed.Target.Runtime == "docker" || parsed.Target.Runtime == "" {
			composeYAML = render.RenderCompose(parsed.Name, parsed, &node, "", jvmArgs, "")
		}
		if parsed.Target.Runtime == "jar" {
			systemdUnit = render.RenderSystemdUnit(parsed, &node, jvmArgs, "", "")
		}

		rendered = append(rendered, renderedNode{
			Index:    i,
			NodeName: parsed.Name,
			Type:     node.Type,
			HOCON:    hocon,
			Compose:  composeYAML,
			Systemd:  systemdUnit,
			JVMArgs:  jvmArgs,
			Redacted: r.Redacted,
		})

		// --output-dir is an explicit "write to disk" request — we
		// honour it regardless of --output text/json. Earlier this
		// branch was gated on outputFmt != "json", which silently
		// suppressed file writes when an agent wanted both the JSON
		// manifest AND the rendered files (the common pipeline:
		// render → docker compose up). The JSON manifest includes
		// the rendered bodies inline; that doesn't preclude also
		// writing them.
		if renderOutputDir != "" {
			nodeDir := renderOutputDir
			if len(parsed.Nodes) > 1 {
				nodeDir = filepath.Join(renderOutputDir, fmt.Sprintf("node%d", i))
			}
			if err := writeRenderedFiles(nodeDir, parsed.Name, hocon, composeYAML, systemdUnit); err != nil {
				return err
			}
		}
	}

	// A redacted render is a preview, not a deployable artifact:
	// java-tron rejects the `<REDACTED:...>` placeholder. Say so on
	// stderr (which never mixes with the stdout payload) so an operator
	// or agent piping render → docker compose up isn't left wondering
	// why the witness refuses to sign.
	if anyRedacted {
		fmt.Fprintln(os.Stderr,
			"warning: witness private key redacted from the rendered config — "+
				"this output is a PREVIEW and java-tron will reject it. "+
				"Use `trond apply` to deploy, which inlines the real key without printing it.")
	}

	if outputFmt == "json" {
		payload := map[string]any{
			"name":    parsed.Name,
			"network": parsed.Network,
			"nodes":   rendered,
		}
		if anyRedacted {
			// Machine-readable twin of the stderr warning, so agents
			// parsing only stdout still see that the artifact is a
			// preview.
			payload["redacted"] = true
		}
		return output.WriteJSON(os.Stdout, payload)
	}

	if renderOutputDir != "" {
		// Files already written above; nothing else to print.
		return nil
	}

	// Text mode: stream each artifact with banner separators.
	for _, r := range rendered {
		if r.Index > 0 {
			fmt.Println("---")
		}
		fmt.Printf("# HOCON Config (node %d: %s)\n", r.Index, r.Type)
		fmt.Println(r.HOCON)
		if r.Compose != "" {
			fmt.Println("# docker-compose.yaml")
			fmt.Println(r.Compose)
		}
		if r.Systemd != "" {
			fmt.Println("# systemd unit")
			fmt.Println(r.Systemd)
		}
	}

	return nil
}

func writeRenderedFiles(dir, name string, hocon, compose, systemd string) error {
	// 0700, not 0755: the .conf we are about to drop in here carries the
	// witness signing key inlined by render.RenderHOCON (typesafe-config
	// does no ${ENV} substitution, so the raw key ends up in the body).
	// An existing directory keeps whatever mode it already has — a 0755
	// directory holding a 0600 file is not an exposure, and silently
	// tightening a path the operator chose is not ours to do.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	configName := fmt.Sprintf("%s.conf", name)
	if err := writeSecretFile(filepath.Join(dir, configName), []byte(hocon)); err != nil {
		return fmt.Errorf("write hocon: %w", err)
	}

	// compose / systemd stay 0644, matching what the deploy path already
	// writes for the same artifacts. trond never *resolves* a secret into
	// them: the keystore password is emitted as the literal ${NAME}
	// placeholder and the witness key fields are never referenced here.
	// (An operator who types a literal secret into intent.extra_env has it
	// copied verbatim into both — that is their own value, handled exactly
	// as the deploy path handles it, and not what this finding is about.)
	if compose != "" {
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.yaml"), []byte(compose), 0644); err != nil {
			return fmt.Errorf("write compose: %w", err)
		}
	}

	if systemd != "" {
		unitName := fmt.Sprintf("tron-%s.service", name)
		if err := os.WriteFile(filepath.Join(dir, unitName), []byte(systemd), 0644); err != nil {
			return fmt.Errorf("write systemd: %w", err)
		}
	}

	return nil
}

// writeSecretFile writes data to path with mode 0600, enforcing that mode
// even when path already exists.
//
// os.WriteFile's perm argument applies only when it creates the file, so a
// re-render into a stable --output-dir would otherwise keep a mode left
// behind by an earlier run (0644 for anything written before this change)
// and publish the witness key again. The chmod runs on the open descriptor
// (fchmod), so it can only ever affect the inode we just opened, never a
// path an attacker swapped in behind us; and it runs *before* the body is
// written, so the secret bytes never exist in a loosely-moded file even
// momentarily, and a failed write leaves an empty 0600 file rather than a
// truncated world-readable one.
func writeSecretFile(path string, data []byte) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return nil
}
