package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// filesCmd is the parent for "trond files get" and "trond files put".
//
// For Docker runtime nodes the path inside the container is reached via
// "docker cp"; for jar runtime nodes the path is on the target host.
// SSH+jar targets transparently use the SSH-side WriteFile/ReadFile.
//
// SSH+Docker is currently rejected: copying through "docker cp" on the
// remote host would require staging the file on that host first via
// Target.Upload, which adds a round-trip and doesn't fit how test harnesses
// drive trond today (they typically run on the same host as docker). When
// that combination matters, the harness can do "files put" on a local
// runtime and SSH the artefact into place separately.
var filesCmd = &cobra.Command{
	Use:   "files",
	Short: "Push or pull files to/from a managed node",
	Long: `Move files between the host and a managed node.

  trond files put  <node> <local-src>  <remote-dst>
  trond files get  <node> <remote-src> <local-dst>

For Docker nodes the remote path is inside the container. For jar nodes the
remote path is on the host where the service runs.`,
}

var filesPutCmd = &cobra.Command{
	Use:   "put <node> <local-src> <remote-dst>",
	Short: "Upload a local file into a managed node",
	Args:  cobra.ExactArgs(3),
	RunE:  runFilesPut,
}

var filesGetCmd = &cobra.Command{
	Use:   "get <node> <remote-src> <local-dst>",
	Short: "Download a file from a managed node to the local host",
	Args:  cobra.ExactArgs(3),
	RunE:  runFilesGet,
}

func init() {
	filesCmd.AddCommand(filesPutCmd)
	filesCmd.AddCommand(filesGetCmd)
	rootCmd.AddCommand(filesCmd)
}

func runFilesPut(cmd *cobra.Command, args []string) error {
	nodeName, localSrc, remoteDst := args[0], args[1], args[2]
	outputFmt, _ := cmd.Flags().GetString("output")

	// put writes caller-supplied bytes to a caller-supplied path on the
	// node — on a jar node that is the target host, so it can land a
	// systemd unit, a node config, or an authorized_keys. That is a
	// mutation of the rig whatever the file happens to contain, so it
	// takes the gate. `files get` is the read counterpart and stays
	// allowed.
	//
	// Ahead of resolveNodeContext, per requirePrivateForNode's contract.
	start := time.Now()
	if err := requirePrivateForNode(nodeName); err != nil {
		return err
	}

	nc, err := resolveNodeContext(nodeName)
	if err != nil {
		return err
	}
	defer nc.Close()

	data, err := os.ReadFile(localSrc)
	if err != nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("read local file: %v", err))
	}

	ctx := cmd.Context()
	if nc.Node.Runtime == "jar" {
		// Direct host write via Target.WriteFile.
		if err := nc.Target.WriteFile(ctx, remoteDst, data, 0o644); err != nil {
			writeAudit(auditEvent{Command: "files put", Node: nodeName, Target: nc.Target.String(),
				Result: "error", ErrorCode: "FILES_ERROR", Detail: remoteDst, Start: start})
			return output.NewError("FILES_ERROR", output.ExitGeneralError,
				fmt.Sprintf("write to %s: %v", remoteDst, err))
		}
	} else {
		if nc.Node.Target.Type == "ssh" {
			return output.NewError("UNSUPPORTED", output.ExitGeneralError,
				"files put on a docker node over SSH is not yet supported (would require remote staging)")
		}
		// Docker node: stage the file on the host then docker cp into the
		// container. We write to a temp path so concurrent files put don't
		// collide.
		stage, err := os.CreateTemp("", "trond-put-*")
		if err != nil {
			return output.NewError("FILES_ERROR", output.ExitGeneralError, err.Error())
		}
		stagePath := stage.Name()
		defer os.Remove(stagePath)
		if _, err := stage.Write(data); err != nil {
			stage.Close()
			return output.NewError("FILES_ERROR", output.ExitGeneralError, err.Error())
		}
		stage.Close()

		if _, err := nc.Target.Exec(ctx, "docker", "cp", stagePath, fmt.Sprintf("%s:%s", nodeName, remoteDst)); err != nil {
			writeAudit(auditEvent{Command: "files put", Node: nodeName, Target: nc.Target.String(),
				Result: "error", ErrorCode: "FILES_ERROR", Detail: remoteDst, Start: start})
			return output.NewError("FILES_ERROR", output.ExitGeneralError,
				fmt.Sprintf("docker cp into %s: %v", nodeName, err))
		}
	}

	// Records where the bytes went, never what they were.
	writeAudit(auditEvent{Command: "files put", Node: nodeName, Target: nc.Target.String(),
		Result: "success", Detail: remoteDst, Start: start})
	return writeFilesResult(outputFmt, "put", nodeName, localSrc, remoteDst, len(data))
}

func runFilesGet(cmd *cobra.Command, args []string) error {
	nodeName, remoteSrc, localDst := args[0], args[1], args[2]
	outputFmt, _ := cmd.Flags().GetString("output")

	// get reads an arbitrary path off the node. That is not a mutation, so
	// it is not gated — but a jar node's config.conf carries the
	// block-signing key in localwitness, so which path was read is worth
	// recording even when reading it was entirely legitimate.
	start := time.Now()

	nc, err := resolveNodeContext(nodeName)
	if err != nil {
		return err
	}
	defer nc.Close()

	ctx := cmd.Context()
	var data []byte

	if nc.Node.Runtime == "jar" {
		data, err = nc.Target.ReadFile(ctx, remoteSrc)
		if err != nil {
			return output.NewError("FILES_ERROR", output.ExitGeneralError,
				fmt.Sprintf("read %s: %v", remoteSrc, err))
		}
	} else {
		if nc.Node.Target.Type == "ssh" {
			return output.NewError("UNSUPPORTED", output.ExitGeneralError,
				"files get on a docker node over SSH is not yet supported")
		}
		// Docker node: docker cp out to a temp file, then read it.
		stage, err := os.CreateTemp("", "trond-get-*")
		if err != nil {
			return output.NewError("FILES_ERROR", output.ExitGeneralError, err.Error())
		}
		stage.Close()
		stagePath := stage.Name()
		defer os.Remove(stagePath)

		if _, err := nc.Target.Exec(ctx, "docker", "cp", fmt.Sprintf("%s:%s", nodeName, remoteSrc), stagePath); err != nil {
			return output.NewError("FILES_ERROR", output.ExitGeneralError,
				fmt.Sprintf("docker cp from %s: %v", nodeName, err))
		}
		data, err = os.ReadFile(stagePath)
		if err != nil {
			return output.NewError("FILES_ERROR", output.ExitGeneralError, err.Error())
		}
	}

	if err := os.MkdirAll(filepath.Dir(localDst), 0o755); err != nil {
		return output.NewError("FILES_ERROR", output.ExitGeneralError, err.Error())
	}
	if err := target.WriteLocalFile(localDst, data, 0o600); err != nil {
		return output.NewError("FILES_ERROR", output.ExitGeneralError, err.Error())
	}

	writeAudit(auditEvent{Command: "files get", Node: nodeName, Target: nc.Target.String(),
		Result: "success", Detail: remoteSrc, Start: start})
	return writeFilesResult(outputFmt, "get", nodeName, remoteSrc, localDst, len(data))
}

func writeFilesResult(format, op, node, src, dst string, size int) error {
	if quiet {
		return nil
	}
	result := map[string]any{
		"op":    op,
		"node":  node,
		"src":   src,
		"dst":   dst,
		"bytes": size,
	}
	if format == "json" {
		return output.WriteJSON(os.Stdout, result)
	}
	fmt.Printf("%s %s: %s -> %s (%d bytes)\n", op, node, src, dst, size)
	return nil
}
