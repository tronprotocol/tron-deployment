package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/snapshot"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

var (
	dlNetwork  string
	dlDomain   string
	dlKind     string
	dlRegion   string
	dlEngine   string
	dlBackup   string
	dlDest     string
	dlNode     string
	dlForce    bool
	dlNoVerify bool
	dlSHA256   string
	dlDryRun   bool
	dlDetach   bool
)

var downloadCmd = &cobra.Command{
	Use:   "download",
	Short: "Stream a snapshot tarball into a local directory",
	Long: `Download a chain database snapshot, streaming the tarball through
gunzip + tar so the .tgz is never persisted to disk. Verifies the
upstream MD5 sidecar — if the sidecar cannot be fetched the download
aborts rather than extracting unchecked data, unless you pass
--no-verify. Also pre-checks free disk space and refuses to overwrite an
existing database without --force.
It refuses destinations belonging to running or error-state managed nodes;
stop the node first, then download.

The default destination is ./output-directory under the current working
directory — same convention as the official tron-docker tooling. Pass
--node <name> to write into a managed node's volume / install path
instead.

Transport: the six mainnet mirrors are bare IPs that publish no HTTPS
endpoint, so those transfers are cleartext and trond says so — on stderr
before the transfer, and as "plaintext_transport": true in -o json. On a
cleartext mirror the upstream .md5sum sidecar arrives over the same
connection as the tarball, so it proves the transfer was not corrupted,
not that the archive came from TRON. Pass --sha256 <hex> with a digest
you obtained out of band to get a check that can actually detect
substitution; the sha256 of every download is printed and reported so you
can pin it on later fetches of the same backup. The nile mirror is HTTPS.`,
	Example: `  # Latest mainnet lite snapshot, default mirror
  trond snapshot download --network mainnet

  # Pin to a specific backup, full archive, US mirror
  trond snapshot download --network mainnet --type full --region america --backup backup20250115

  # Show what would happen without downloading
  trond snapshot download --network nile --dry-run

  # Pipe straight into a managed node's storage path
  trond snapshot download --node my-fullnode --network mainnet`,
	RunE: runDownload,
}

func init() {
	downloadCmd.Flags().StringVar(&dlNetwork, "network", "", "Network: mainnet | nile")
	downloadCmd.Flags().StringVar(&dlDomain, "domain", "", "Mirror domain (overrides --network/--region)")
	downloadCmd.Flags().StringVar(&dlKind, "type", "lite", "Snapshot kind: lite | full")
	downloadCmd.Flags().StringVar(&dlRegion, "region", "", "Region: singapore | america")
	downloadCmd.Flags().StringVar(&dlEngine, "db-engine", "", "Engine: leveldb | rocksdb (mainnet full only)")
	downloadCmd.Flags().StringVar(&dlBackup, "backup", "", "Specific backup name (default: latest)")
	downloadCmd.Flags().StringVar(&dlDest, "to", "", "Destination directory (default ./output-directory)")
	downloadCmd.Flags().StringVar(&dlNode, "node", "", "Managed node name; resolves --to from state")
	downloadCmd.Flags().BoolVar(&dlForce, "force", false, "Overwrite existing database in destination")
	downloadCmd.Flags().BoolVar(&dlNoVerify, "no-verify", false, "Extract without checking the MD5 sidecar (UNSAFE; otherwise a missing sidecar aborts the download)")
	downloadCmd.Flags().StringVar(&dlSHA256, "sha256", "",
		"Expected SHA-256 of the tarball, obtained out of band (64 hex chars). "+
			"Unlike the upstream .md5sum — which travels the same channel as the "+
			"tarball — a pin supplied here can detect a substituted archive. "+
			"A mismatch fails the download.")
	downloadCmd.Flags().BoolVar(&dlDryRun, "dry-run", false, "Print what would be downloaded and exit")
	downloadCmd.Flags().BoolVar(&dlDetach, "detach", false, "Run in background; survives terminal close (logs to ~/.trond/snapshots/<id>.log)")
}

func runDownload(cmd *cobra.Command, _ []string) error {
	outputFmt, _ := cmd.Flags().GetString("output")

	dest := dlDest
	if dlNode != "" {
		resolved, err := destFromNode(dlNode)
		if err != nil {
			return output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
		}
		dest = resolved
	}
	if dest == "" {
		dest = "./output-directory"
	}
	if err := refuseRunningNodeDestination(dest, dlNode); err != nil {
		return err
	}

	src, err := resolveSource(dlDomain, dlNetwork, dlKind, dlRegion, dlEngine)
	if err != nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
	}

	backup := dlBackup
	if backup == "" {
		latest, err := snapshot.LatestBackup(cmd.Context(), *src)
		if err != nil {
			return output.NewError("LIST_ERROR", output.ExitGeneralError, err.Error())
		}
		backup = latest
	}

	opts := snapshot.DownloadOptions{
		Source:         *src,
		Backup:         backup,
		Kind:           snapshot.DBKind(dlKind),
		DestDir:        dest,
		Force:          dlForce,
		NoVerify:       dlNoVerify,
		ExpectedSHA256: dlSHA256,
		// Warnings go to stderr in every mode — JSON callers keep a clean
		// stdout, and under --detach the re-execed child's stderr is the
		// job log, so the notice is preserved there too.
		WarnFn: func(msg string) { fmt.Fprint(cmd.ErrOrStderr(), msg) },
	}

	pre, err := snapshot.Preflight(cmd.Context(), opts)
	if err != nil {
		return output.NewError("PREFLIGHT_ERROR", output.ExitGeneralError, err.Error())
	}

	if dlDryRun {
		return emitPlan(outputFmt, src, backup, dest, pre)
	}

	// Refuse overwrite up front so the user sees the message before
	// the download starts (the same check inside Download fires after
	// preflight again, but we want to surface it pre-network).
	if pre.WouldOverwrite && !dlForce {
		return output.NewError("HUMAN_REQUIRED", output.ExitHumanRequired,
			fmt.Sprintf("destination %s already has a database; pass --force to overwrite",
				filepath.Join(dest, "output-directory", "database")))
	}
	if pre.NeededBytes > 0 && pre.FreeBytes < pre.NeededBytes {
		return output.NewError("DISK_SPACE_ERROR", output.ExitGeneralError,
			fmt.Sprintf("need ~%s free in %s, have %s",
				humanGB(pre.NeededBytes), dest, humanGB(pre.FreeBytes)))
	}

	// Hand off to a detached child if requested. We run the same trond
	// binary with the same args minus --detach, redirected to a log file
	// in the per-user state dir; the child survives terminal close (we
	// disown via Setsid so SIGHUP doesn't reach it). Returns immediately
	// with the job manifest.
	if dlDetach {
		// The child re-execs and warns into the job log, but the operator
		// is looking at *this* terminal right now — warn here too rather
		// than making them go read a file to learn the transfer is
		// cleartext.
		if pre.PlaintextTransport {
			fmt.Fprint(cmd.ErrOrStderr(), snapshot.PlaintextWarning(pre.URL, dlSHA256 != ""))
		}
		return spawnDetached(outputFmt, src, backup, dest)
	}

	if outputFmt != "json" {
		// Set up a periodic progress printer for the human user. JSON
		// callers see only the final result.
		opts.ProgressFn = makeProgressPrinter(cmd.ErrOrStderr())
	}

	res, err := snapshot.Download(cmd.Context(), opts)
	if err != nil {
		var running *snapshot.RunningNodeDestinationError
		if errors.As(err, &running) {
			return output.NewError("NODE_RUNNING", output.ExitGeneralError, err.Error()).WithSuggestions("Stop the node first: trond stop " + running.NodeName)
		}
		var ow *snapshot.OverwriteError
		if errors.As(err, &ow) {
			return output.NewError("HUMAN_REQUIRED", output.ExitHumanRequired, ow.Error())
		}
		var vu *snapshot.VerificationUnavailableError
		if errors.As(err, &vu) {
			return output.NewError("VERIFICATION_UNAVAILABLE", output.ExitGeneralError, vu.Error()).
				WithSuggestions(
					"Retry — the sidecar may be published a few minutes after the tarball, or the mirror may be briefly unhealthy",
					fmt.Sprintf("Pick another backup: trond snapshot list --network %s", src.Network),
					"Only if you accept an unauthenticated chain database: re-run with --no-verify",
				)
		}
		return output.NewError("DOWNLOAD_ERROR", output.ExitGeneralError, err.Error())
	}

	if outputFmt == "json" {
		return output.WriteJSON(os.Stdout, downloadPayload(src, backup, dest, res, pre))
	}

	fmt.Fprintf(cmd.OutOrStdout(),
		"Done. %s in %s, %d files extracted to %s",
		humanGB(uint64(res.BytesDownloaded)), res.Duration.Round(time.Second), res.FilesExtracted, dest)
	if res.MD5Verified {
		fmt.Fprint(cmd.OutOrStdout(), " (md5 ✓)")
	} else {
		// Only reachable with --no-verify: without it, a sidecar that is
		// missing or unfetchable aborts the download rather than landing
		// an unchecked chain database here.
		fmt.Fprint(cmd.OutOrStdout(), " (NOT VERIFIED — --no-verify was passed; this chain database is unauthenticated)")
	}
	if res.SHA256Verified {
		fmt.Fprint(cmd.OutOrStdout(), " (sha256 pin ✓)")
	}
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintf(cmd.OutOrStdout(), "sha256: %s\n", res.SHA256)
	if res.PlaintextTransport && !res.SHA256Verified {
		fmt.Fprintln(cmd.OutOrStdout(),
			"Note: fetched over cleartext HTTP with no --sha256 pin — the md5 above proves\n"+
				"transfer integrity, not that these bytes came from TRON. Record the sha256\n"+
				"and pass it as --sha256 on future fetches of this same backup.")
	}
	if pre.UserdataPresent {
		fmt.Fprintln(cmd.OutOrStdout(), "Note: pre-existing userdata/ was preserved.")
	}
	return nil
}

// refuseRunningNodeDestination protects managed node databases from being
// overwritten while java-tron (jar) or Docker holds them open. --force only
// controls the ordinary existing-database overwrite prompt; it never bypasses
// this data-corruption guard.
func refuseRunningNodeDestination(dest, explicitNode string) error {
	err := snapshot.RefuseRunningNodeDestination(dest, explicitNode)
	if err == nil {
		return nil
	}
	var running *snapshot.RunningNodeDestinationError
	if errors.As(err, &running) {
		return output.NewError("NODE_RUNNING", output.ExitGeneralError, err.Error()).WithSuggestions("Stop the node first: trond stop " + running.NodeName)
	}
	return output.NewError("STATE_ERROR", output.ExitGeneralError, err.Error())
}

// downloadPayload builds the `-o json` body for a completed foreground
// download. Extracted from runDownload so a unit test can validate the
// exact shipped shape against schemas/output/snapshot-download.schema.json
// without needing a live multi-GB transfer.
func downloadPayload(src *snapshot.Source, backup, dest string, res *snapshot.DownloadResult, pre *snapshot.PreflightResult) map[string]any {
	payload := map[string]any{
		"source":           src,
		"backup":           backup,
		"dest":             dest,
		"bytes_downloaded": res.BytesDownloaded,
		"duration_ms":      res.DurationMs,
		"md5_verified":     res.MD5Verified,
		// Distinguishes "checked and good" from "deliberately unchecked":
		// a missing sidecar is now an error, never a silent skip.
		"verification_skipped": res.VerificationSkipped,
		"actual_md5":           res.ActualMD5,
		"files_extracted":      res.FilesExtracted,
		"userdata_present":     pre.UserdataPresent,
		"sha256":               res.SHA256,
		"sha256_verified":      res.SHA256Verified,
		// The cleartext-transport fact has to reach agents that read
		// stdout only and never see the stderr warning.
		"plaintext_transport": res.PlaintextTransport,
	}
	if res.ExpectedSHA256 != "" {
		payload["expected_sha256"] = res.ExpectedSHA256
	}
	return payload
}

// planPayload builds the `--dry-run -o json` body. Extracted alongside
// downloadPayload so both shipped shapes are unit-testable against the
// published schema.
func planPayload(src *snapshot.Source, backup, dest string, pre *snapshot.PreflightResult) map[string]any {
	return map[string]any{
		"source":    src,
		"backup":    backup,
		"dest":      dest,
		"preflight": pre,
	}
}

func emitPlan(outputFmt string, src *snapshot.Source, backup, dest string, pre *snapshot.PreflightResult) error {
	if outputFmt == "json" {
		return output.WriteJSON(os.Stdout, planPayload(src, backup, dest, pre))
	}
	fmt.Println("Snapshot download plan:")
	fmt.Printf("  source:           %s (%s, %s, %s)\n", src.Domain, src.Network, src.DBKind, src.Region)
	fmt.Printf("  backup:           %s\n", backup)
	fmt.Printf("  url:              %s\n", pre.URL)
	fmt.Printf("  expected size:    %s\n", humanGB(uint64(pre.ExpectedSize)))
	fmt.Printf("  destination:      %s\n", dest)
	fmt.Printf("  free space:       %s\n", humanGB(pre.FreeBytes))
	fmt.Printf("  needed (~2x DL):  %s\n", humanGB(pre.NeededBytes))
	fmt.Printf("  database present: %t\n", pre.DatabasePresent)
	fmt.Printf("  userdata present: %t (preserved across extraction)\n", pre.UserdataPresent)
	fmt.Printf("  md5 sidecar:      %t\n", pre.HasMD5Sidecar)
	fmt.Printf("  transport:        %s\n", transportLabel(pre.PlaintextTransport))
	if pre.PlaintextTransport {
		fmt.Print(snapshot.PlaintextWarning(pre.URL, dlSHA256 != ""))
	}
	if !pre.HasMD5Sidecar {
		fmt.Println("  WARNING: the sidecar did not answer this probe — the download will")
		fmt.Println("           refuse to extract unverified data unless you pass --no-verify.")
	}
	if pre.WouldOverwrite {
		fmt.Println("  WARNING: existing database would be overwritten (use --force).")
	}
	if pre.NeededBytes > 0 && pre.FreeBytes < pre.NeededBytes {
		fmt.Println("  WARNING: insufficient disk space for safe extraction.")
	}
	return nil
}

// transportLabel renders the plaintext-transport fact for the human dry-run
// table. Kept blunt on purpose: "https (authenticated)" would overclaim, so
// we name the transport and let the warning below carry the consequence.
func transportLabel(plaintext bool) string {
	if plaintext {
		return "cleartext http — NOT authenticated"
	}
	return "https"
}

// destFromNode looks up a managed node and returns a path that maps to
// its chain-data root. For docker runtime we point at the named volume
// — but volumes aren't a filesystem path the user can extract into, so
// we surface a helpful error instead. For jar runtime we use install_path.
func destFromNode(name string) (string, error) {
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
		return "", fmt.Errorf("node %q not in state", name)
	}
	if node.Runtime != "jar" {
		return "", fmt.Errorf("--node only supports jar runtime; for docker, extract to a host path "+
			"and bind-mount via storage.path in your intent (current runtime: %s)", node.Runtime)
	}
	if node.InstallPath == "" {
		return "", fmt.Errorf("node %q has no install_path recorded; rerun apply or pass --to", name)
	}
	return node.InstallPath, nil
}

// makeProgressPrinter returns a ProgressFn that emits a single repeating
// status line to stderr — no curses, no bars, just numbers. Plays nicely
// with non-tty environments (CI, nohup) and with the existing log format.
func makeProgressPrinter(w interface{ Write(p []byte) (int, error) }) func(int64, int64) {
	var lastPercent int = -1
	start := time.Now()
	return func(downloaded, total int64) {
		if total <= 0 {
			fmt.Fprintf(w, "\rDownloaded %s, elapsed %s", humanGB(uint64(downloaded)), time.Since(start).Round(time.Second))
			return
		}
		percent := int(float64(downloaded) * 100 / float64(total))
		if percent == lastPercent {
			return
		}
		lastPercent = percent
		eta := "--"
		if downloaded > 0 {
			elapsed := time.Since(start)
			remain := time.Duration(float64(elapsed) * float64(total-downloaded) / float64(downloaded))
			eta = remain.Round(time.Second).String()
		}
		fmt.Fprintf(w, "\r%3d%%  %s / %s  eta %s",
			percent,
			humanGB(uint64(downloaded)),
			humanGB(uint64(total)),
			eta,
		)
		if percent == 100 {
			fmt.Fprintln(w)
		}
	}
}

// humanGB renders a byte count in GB (or "unknown" for zero) — trimmed
// for log lines where seconds-of-progress matter more than precision.
func humanGB(n uint64) string {
	if n == 0 {
		return "unknown"
	}
	const GB = 1 << 30
	if n < GB {
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.2f GB", float64(n)/float64(GB))
}
