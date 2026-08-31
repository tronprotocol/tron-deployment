package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/fsclone"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/render"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// clone wires `trond snapshot clone <src> <dst>` — a thin presentation
// layer over internal/fsclone.CloneDir. It exists so an operator/agent can
// build a warm pool of chain-DB fixtures: fork a cached snapshot (or a
// STOPPED node's data dir) into a fresh isolated directory in seconds via
// a copy-on-write clone, instead of re-downloading or re-copying 30-90GB.
//
//	src ──stat/guard──> CloneDir(src,dst) ──> {clonefile|ficlone|copy}
//	     same FS? ─no─> free-space preflight (full copy is guaranteed)
//	     dst ⊄ src?  (canonical-path containment guard)
//
// Mutating (creates dst) so it is CLI-only — deliberately NOT an MCP tool,
// keeping filesystem-mutating capability out of the read-only agent fleet.
var cloneFromNode string

var cloneCmd = &cobra.Command{
	Use:   "clone <src> <dst>  |  clone --from-node <name> <dst>",
	Short: "Copy-on-write clone a chain-DB directory into a fresh path",
	Long: `Clone an existing chain database directory into a new, independent
directory. When source and destination live on the same filesystem this
is a copy-on-write clone (APFS clonefile / Linux FICLONE): seconds and
near-zero extra disk even for a 30-90GB store. Across filesystems (or on a
filesystem without CoW) it falls back to a full byte copy — a warning is
printed and the "method" field reports "copy".

Two source modes:
  <src>               an explicit directory (a downloaded snapshot, etc.),
                      cloned verbatim.
  --from-node <name>  resolve a managed node's chain-DB dir from state. The
                      node must be STOPPED and on a LOCAL target. jar nodes
                      resolve to <install_path>/output-directory; docker
                      nodes must use bind-mount storage (storage.path /
                      storage.data) — a default named docker volume is not a
                      host path and cannot be cloned (redeploy with
                      storage.path, or pass the path directly).

<dst> must not already exist — clone refuses rather than overwrite. The
source must be QUIESCENT: cloning a running node's live DB yields an
undefined point-in-time view.`,
	Example: `  # Fork a cached snapshot into an isolated fixture (instant on APFS/btrfs/xfs)
  trond snapshot clone ./output-directory ./rig-a/output-directory

  # Fork a stopped managed node's chain DB by name
  trond snapshot clone --from-node fn0 ./rig-a/output-directory -o json`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runClone,
}

func init() {
	cloneCmd.Flags().StringVar(&cloneFromNode, "from-node", "",
		"Resolve <src> from a STOPPED local managed node's chain-DB dir (then only <dst> is positional)")
}

func runClone(cmd *cobra.Command, args []string) error {
	outputFmt, _ := cmd.Flags().GetString("output")

	// Source comes either from --from-node (resolved from state) or from a
	// positional <src>. The two are mutually exclusive; the positional arity
	// differs (1 vs 2), so validate explicitly rather than via cobra.
	var src, dst string
	if cloneFromNode != "" {
		if len(args) != 1 {
			return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
				"with --from-node, pass exactly one positional argument: the destination")
		}
		dst = args[0]
		resolved, err := resolveNodeDBDir(cmd.Context(), cloneFromNode)
		if err != nil {
			return err
		}
		src = resolved
	} else {
		if len(args) != 2 {
			return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
				"expected two arguments: <src> <dst> (or use --from-node <name> <dst>)")
		}
		src, dst = args[0], args[1]
	}

	// Source must exist and be a directory. CloneDir re-checks, but we
	// want a clean VALIDATION_ERROR before any staging work.
	srcInfo, err := os.Stat(src)
	if err != nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("source %s: %v", src, err))
	}
	if !srcInfo.IsDir() {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("source %s is not a directory", src))
	}

	// Refuse a pre-existing destination — clone never overwrites (matches
	// CloneDir's contract). No --force / destructive path in trond.
	if _, statErr := os.Stat(dst); statErr == nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("destination %s already exists; choose a fresh path or remove it first", dst))
	} else if !os.IsNotExist(statErr) {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("stat destination %s: %v", dst, statErr))
	}

	// Containment guard on CANONICAL paths. A dst inside src would make the
	// clone walk re-enter its own staging area (corruption/bloat). Raw
	// string-prefix checks are bypassable via symlinks/relative paths, so
	// resolve to absolute, symlink-free paths first.
	if err := assertDisjoint(src, dst); err != nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
	}

	// Free-space preflight, but ONLY when CoW cannot fire (src and dst on
	// different filesystems → a full byte copy is guaranteed). A same-FS
	// CoW clone shares extents and needs ~no space, so a free>=size check
	// there would falsely refuse on a near-full disk.
	anc := firstExistingAncestor(dst)
	if sameFS, devErr := sameFilesystem(src, anc); devErr == nil && !sameFS {
		need, sizeErr := dirSize(src)
		free, freeErr := target.NewLocalTarget().DiskFree(cmd.Context(), anc)
		if sizeErr == nil && freeErr == nil && need > 0 && free < need {
			return output.NewError("DISK_SPACE_ERROR", output.ExitGeneralError,
				fmt.Sprintf("cross-filesystem clone of %s requires a full byte copy (~%d bytes) "+
					"but only %d bytes are free at %s", src, need, free, anc))
		}
	}

	start := time.Now()
	method, err := fsclone.CloneDir(src, dst)
	if err != nil {
		return output.NewError("CLONE_ERROR", output.ExitGeneralError, err.Error())
	}
	durMs := time.Since(start).Milliseconds()

	// CoW didn't fire — surface it loudly. The "fast clone" became a slow
	// full copy; the caller (and any human watching) should know.
	if method == methodCopyName {
		fmt.Fprintf(os.Stderr,
			"warning: copy-on-write unavailable (cross-filesystem or unsupported FS) — "+
				"fell back to a full byte copy of %s; this is slow and uses full disk\n", src)
	}

	res := map[string]any{
		"source":      src,
		"dest":        dst,
		"method":      method,
		"duration_ms": durMs,
	}
	if outputFmt == "json" {
		return output.WriteJSON(os.Stdout, res)
	}
	fmt.Printf("Cloned %s -> %s (method: %s, %dms)\n", src, dst, method, durMs)
	return nil
}

// methodCopyName mirrors fsclone's "copy" fallback label. Kept as a local
// const so the warning trigger doesn't hard-code a bare string twice.
const methodCopyName = "copy"

// assertDisjoint rejects src==dst, dst-inside-src, and src-inside-dst using
// canonical (absolute, symlink-resolved) paths. dst need not exist yet, so
// its nearest existing ancestor is resolved and the remainder re-appended.
func assertDisjoint(src, dst string) error {
	cs, err := canonical(src)
	if err != nil {
		return fmt.Errorf("resolve source %s: %v", src, err)
	}
	cd, err := canonicalDst(dst)
	if err != nil {
		return fmt.Errorf("resolve destination %s: %v", dst, err)
	}
	if cs == cd {
		return fmt.Errorf("source and destination resolve to the same path: %s", cs)
	}
	if within(cd, cs) {
		return fmt.Errorf("destination %s is inside source %s; choose a destination outside the source tree", dst, src)
	}
	if within(cs, cd) {
		return fmt.Errorf("source %s is inside destination %s", src, dst)
	}
	return nil
}

// canonical returns the absolute, symlink-resolved form of an existing path.
func canonical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// canonicalDst canonicalises a path that may not exist yet: resolve symlinks
// on the nearest existing ancestor, then re-join the not-yet-created tail.
func canonicalDst(dst string) (string, error) {
	abs, err := filepath.Abs(dst)
	if err != nil {
		return "", err
	}
	anc := firstExistingAncestor(abs)
	resolvedAnc, err := filepath.EvalSymlinks(anc)
	if err != nil {
		return "", err
	}
	rest, err := filepath.Rel(anc, abs)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedAnc, rest), nil
}

// within reports whether child is strictly inside parent (not equal).
func within(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// firstExistingAncestor walks up from an absolute path until it finds a
// directory that exists (the filesystem root always does).
func firstExistingAncestor(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	for {
		if _, statErr := os.Stat(abs); statErr == nil {
			return abs
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return abs
		}
		abs = parent
	}
}

// sameFilesystem reports whether two existing paths sit on the same device,
// i.e. whether a copy-on-write clone between them can share extents.
func sameFilesystem(a, b string) (bool, error) {
	var sa, sb syscall.Stat_t
	if err := syscall.Stat(a, &sa); err != nil {
		return false, err
	}
	if err := syscall.Stat(b, &sb); err != nil {
		return false, err
	}
	return sa.Dev == sb.Dev, nil
}

// dirSize sums the apparent size of all regular files under root.
func dirSize(root string) (uint64, error) {
	var total uint64
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type().IsRegular() {
			info, infoErr := d.Info()
			if infoErr != nil {
				return infoErr
			}
			total += uint64(info.Size())
		}
		return nil
	})
	return total, err
}

// resolveNodeDBDir resolves a managed node's chain-DB host directory for
// --from-node, enforcing the narrow-scope guards from the eng-review:
//
//	ssh target        → refuse (fsclone is local-only; remote clone unsupported)
//	not stopped       → refuse (cloning a live DB is an undefined snapshot)
//	jar               → <install_path>/output-directory (+ live is-active check)
//	docker bind-mount → the host Source of the /java-tron/output-directory mount
//	docker named vol  → refuse (not a host path; redeploy with storage.path)
//
// The docker path + liveness both come from a single `docker inspect`.
func resolveNodeDBDir(ctx context.Context, name string) (string, error) {
	store, err := state.NewStore(paths.State())
	if err != nil {
		return "", err
	}
	st, err := store.Load()
	if err != nil {
		return "", err
	}
	node := store.GetNode(st, name)
	if node == nil {
		return "", output.NewError("NODE_NOT_FOUND", output.ExitGeneralError,
			fmt.Sprintf("node %q not found in state", name)).
			WithSuggestions("Run: trond list", "Pass an explicit <src> path instead of --from-node")
	}
	if node.Target.Type == "ssh" {
		return "", output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("--from-node supports local-target nodes only; %q is on an ssh target", name)).
			WithSuggestions("Clone on the remote host, or copy the DB over and pass an explicit <src> path")
	}
	if node.Status != "stopped" {
		return "", output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("node %q is %q, not stopped; stop it before cloning (a live DB yields an undefined point-in-time view)", name, node.Status)).
			WithSuggestions(fmt.Sprintf("Run: trond stop %s", name))
	}

	switch node.Runtime {
	case "jar":
		if node.InstallPath == "" {
			return "", output.NewError("VALIDATION_ERROR", output.ExitValidationError,
				fmt.Sprintf("node %q has no recorded install_path; pass an explicit <src> path", name))
		}
		if jarActive(ctx, name) {
			return "", output.NewError("VALIDATION_ERROR", output.ExitValidationError,
				fmt.Sprintf("node %q is still active (systemd); stop it before cloning", name)).
				WithSuggestions(fmt.Sprintf("Run: trond stop %s", name))
		}
		return filepath.Join(node.InstallPath, "output-directory"), nil
	case "docker":
		return dockerNodeDBDir(ctx, name)
	default:
		return "", output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("node %q has unsupported runtime %q for --from-node", name, node.Runtime))
	}
}

// dockerNodeDBDir inspects a stopped docker container to find the host bind
// path backing its chain DB, refusing if it's still running or if the DB is
// on a named volume (not a host path).
// dockerInspect returns raw `docker inspect <name>` JSON. A package var so
// tests can swap it for canned output without a live docker daemon.
var dockerInspect = func(ctx context.Context, name string) ([]byte, error) {
	return exec.CommandContext(ctx, "docker", "inspect", name).Output()
}

func dockerNodeDBDir(ctx context.Context, name string) (string, error) {
	out, err := dockerInspect(ctx, name)
	if err != nil {
		return "", output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("docker inspect %q failed (container removed?): %v", name, err)).
			WithSuggestions("The container must still exist (a stopped, not removed, node)",
				"Or pass an explicit <src> path")
	}
	var inspected []struct {
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
		Mounts []struct {
			Type        string `json:"Type"`
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
		} `json:"Mounts"`
	}
	if err := json.Unmarshal(out, &inspected); err != nil || len(inspected) == 0 {
		return "", output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("could not parse docker inspect output for %q", name))
	}
	c := inspected[0]
	if c.State.Running {
		return "", output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("node %q is still running; stop it before cloning", name)).
			WithSuggestions(fmt.Sprintf("Run: trond stop %s", name))
	}
	for _, m := range c.Mounts {
		if m.Destination != render.ContainerDataDir {
			continue
		}
		// Reject by mount TYPE, not by sniffing Source: a Linux named volume
		// can expose a host-ish path, but it's still docker-managed storage.
		if m.Type != "bind" {
			return "", output.NewError("VALIDATION_ERROR", output.ExitValidationError,
				fmt.Sprintf("node %q stores its chain DB in a %s (not a host bind-mount); it cannot be copy-on-write cloned", name, m.Type)).
				WithSuggestions("Redeploy the node with storage.path set to a host directory",
					"Or extract the volume manually and pass an explicit <src> path")
		}
		return m.Source, nil
	}
	return "", output.NewError("VALIDATION_ERROR", output.ExitValidationError,
		fmt.Sprintf("node %q has no chain-DB mount at %s", name, render.ContainerDataDir))
}

// jarActive reports whether the node's systemd unit is currently active. A
// missing/erroring systemctl (e.g. non-systemd host) is treated as "not
// active" — the state.Status==stopped gate already ran; this is the live
// belt-and-suspenders check, and only an explicit "active" blocks the clone.
func jarActive(ctx context.Context, name string) bool {
	out, _ := exec.CommandContext(ctx, "systemctl", "is-active", "tron-"+name+".service").Output()
	return strings.TrimSpace(string(out)) == "active"
}
