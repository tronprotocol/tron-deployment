package snapshot

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/snapshot"
)

var jobsCmd = &cobra.Command{
	Use:   "jobs",
	Short: "List background snapshot download jobs",
	Long: `Show every recorded detached download. Each row carries the job id
(use it with logs/stop), the PID, whether the process is still running,
and the most recent line of its log so you can see progress at a glance.`,
	RunE: runJobs,
}

func runJobs(cmd *cobra.Command, _ []string) error {
	outputFmt, _ := cmd.Flags().GetString("output")
	jobs, err := snapshot.ListJobs(paths.SnapshotJobs())
	if err != nil {
		return output.NewError("JOBS_ERROR", output.ExitGeneralError, err.Error())
	}
	statuses := make([]snapshot.JobStatus, 0, len(jobs))
	for _, j := range jobs {
		statuses = append(statuses, snapshot.Status(paths.SnapshotJobs(), j))
	}

	if outputFmt == "json" {
		return output.WriteJSON(os.Stdout, map[string]any{"jobs": statuses})
	}

	if len(statuses) == 0 {
		fmt.Println("No background snapshot jobs recorded.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "JOB ID\tPID\tSTATE\tNETWORK\tKIND\tBACKUP\tAGE\tLAST LINE")
	for _, s := range statuses {
		state := "running"
		if !s.Running {
			state = "stopped"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.ID, s.PID, state, s.Network, s.Kind, s.Backup,
			time.Since(s.StartedAt).Round(time.Second), truncate(s.ExitNote, 60),
		)
	}
	return tw.Flush()
}

var (
	logsFollow bool
	logsLines  int
)

var logsCmd = &cobra.Command{
	Use:   "logs <job-id>",
	Short: "Print (or tail) a background download's log",
	Args:  cobra.ExactArgs(1),
	RunE:  runLogs,
}

func init() {
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Stream new log lines as they appear (Ctrl-C to stop)")
	logsCmd.Flags().IntVarP(&logsLines, "lines", "n", 0, "Print only the last N lines (0 = all)")
}

func runLogs(cmd *cobra.Command, args []string) error {
	id := args[0]
	job, err := snapshot.ReadJob(paths.SnapshotJobs(), id)
	if err != nil {
		return output.NewError("JOB_NOT_FOUND", output.ExitGeneralError, "no such job: "+id)
	}
	f, err := os.Open(job.LogPath)
	if err != nil {
		return output.NewError("LOG_NOT_FOUND", output.ExitGeneralError, err.Error())
	}
	defer f.Close()

	// Seek for --lines support: we don't bother with line-counting from
	// the end; jumping to a recent offset is enough for "tail" feel.
	if logsLines > 0 {
		const avgLine = 200
		off := int64(-(logsLines * avgLine))
		if size, _ := f.Seek(0, io.SeekEnd); size+off < 0 {
			f.Seek(0, io.SeekStart)
		} else {
			f.Seek(off, io.SeekEnd)
			// Skip partial first line so we don't mid-cut.
			discardLine(f)
		}
	}

	if _, err := io.Copy(os.Stdout, f); err != nil {
		return err
	}

	if !logsFollow {
		return nil
	}
	// Simple polling tail. We avoid inotify/fsevents because the log is
	// short-lived and a 500 ms cadence costs nothing.
	for {
		select {
		case <-cmd.Context().Done():
			return nil
		default:
		}
		time.Sleep(500 * time.Millisecond)
		if _, err := io.Copy(os.Stdout, f); err != nil {
			return err
		}
		if !snapshot.IsRunning(job.PID) {
			// Drain anything that arrived after the process exited, then stop.
			io.Copy(os.Stdout, f)
			return nil
		}
	}
}

func discardLine(f *os.File) {
	buf := make([]byte, 1)
	for {
		n, err := f.Read(buf)
		if err != nil || n == 0 {
			return
		}
		if buf[0] == '\n' {
			return
		}
	}
}

var stopForce bool

var stopCmd = &cobra.Command{
	Use:   "stop <job-id>",
	Short: "Send SIGTERM (or SIGKILL with --force) to a background download",
	Args:  cobra.ExactArgs(1),
	RunE:  runStop,
}

func init() {
	stopCmd.Flags().BoolVar(&stopForce, "force", false, "Use SIGKILL instead of SIGTERM (last resort)")
}

func runStop(cmd *cobra.Command, args []string) error {
	id := args[0]
	job, err := snapshot.ReadJob(paths.SnapshotJobs(), id)
	if err != nil {
		return output.NewError("JOB_NOT_FOUND", output.ExitGeneralError, "no such job: "+id)
	}
	if !snapshot.IsRunning(job.PID) {
		// ESRCH means the recorded process is gone. Remove stale bookkeeping;
		// unlike an identity mismatch, there is no live process to protect.
		if err := snapshot.RemoveJob(paths.SnapshotJobs(), id); err != nil {
			return output.NewError("STOP_ERROR", output.ExitGeneralError, err.Error())
		}
		fmt.Printf("Job %s already stopped (pid %d); removed stale manifest.\n", id, job.PID)
		return nil
	}
	if ok, err := snapshotProcessIdentity(job.PID); err != nil || !ok {
		message := fmt.Sprintf("refusing to signal pid %d: process identity does not match snapshot download", job.PID)
		if err != nil {
			message = fmt.Sprintf("%s (%v)", message, err)
		}
		return output.NewError("PROCESS_IDENTITY_MISMATCH", output.ExitGeneralError, message).
			WithSuggestions("Run: trond snapshot jobs to verify the recorded process")
	}
	sig := syscall.SIGTERM
	if stopForce {
		sig = syscall.SIGKILL
	}
	proc, err := os.FindProcess(job.PID)
	if err != nil {
		return output.NewError("STOP_ERROR", output.ExitGeneralError, err.Error())
	}
	if err := proc.Signal(sig); err != nil {
		return output.NewError("STOP_ERROR", output.ExitGeneralError, err.Error())
	}
	fmt.Printf("Sent %s to job %s (pid %d).\n", sig, id, job.PID)
	return nil
}

// snapshotProcessIdentity verifies the live PID still belongs to the detached
// trond snapshot download. This check is intentionally immediately before
// Signal; PID reuse between the check and signal remains a small unavoidable
// TOCTOU window, but an unrelated live PID is never signalled blindly.
func snapshotProcessIdentity(pid int) (bool, error) {
	var argv []string
	if data, err := os.ReadFile(filepath.Join("/proc", fmt.Sprint(pid), "cmdline")); err == nil {
		for _, arg := range strings.Split(string(data), "\x00") {
			if arg != "" {
				argv = append(argv, arg)
			}
		}
	} else {
		out, psErr := exec.Command("ps", "-p", fmt.Sprint(pid), "-o", "command=").Output()
		if psErr != nil {
			return false, psErr
		}
		argv = strings.Fields(strings.TrimSpace(string(out)))
	}
	if len(argv) == 0 || !strings.Contains(argv[0], "trond") {
		return false, nil
	}
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "snapshot" && argv[i+1] == "download" {
			return true, nil
		}
	}
	return false, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
