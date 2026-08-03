package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/snapshot"
)

// registerSnapshotTools wires the snapshot subsystem as MCP tools.
// All read-only tools are unannotated; download is destructive (it
// writes hundreds of GB to disk and can clobber an existing chain DB
// if --force).

type snapshotListArgs struct {
	Network string `json:"network" jsonschema:"target network: mainnet | nile"`
	Domain  string `json:"domain,omitempty" jsonschema:"specific mirror domain to query; overrides network/region"`
	Kind    string `json:"kind,omitempty" jsonschema:"snapshot kind: lite (default) | full"`
	Region  string `json:"region,omitempty" jsonschema:"singapore | america"`
}

type snapshotDownloadArgs struct {
	Network string `json:"network,omitempty" jsonschema:"network to pull from: mainnet | nile"`
	Domain  string `json:"domain,omitempty" jsonschema:"specific mirror domain (overrides network/region)"`
	Kind    string `json:"kind,omitempty" jsonschema:"lite | full (default lite)"`
	Region  string `json:"region,omitempty" jsonschema:"singapore | america"`
	Backup  string `json:"backup,omitempty" jsonschema:"specific backup name; defaults to latest"`
	Dest    string `json:"dest" jsonschema:"absolute destination directory; the snapshot expands to <dest>/output-directory/..."`
	Force   bool   `json:"force,omitempty" jsonschema:"overwrite an existing chain DB (DESTRUCTIVE)"`
	DryRun  bool   `json:"dry_run,omitempty" jsonschema:"print the plan and exit without downloading"`
	SHA256  string `json:"sha256,omitempty" jsonschema:"expected SHA-256 of the tarball (64 hex chars) obtained out of band; a mismatch fails the download. The mainnet mirrors are cleartext HTTP and their .md5sum sidecar rides the same channel, so this pin is the only check that can detect a substituted archive"`
}

func registerSnapshotTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "snapshot_sources",
		Title:       "List snapshot mirrors",
		Description: "Returns every chain-database snapshot mirror trond knows about (mainnet ×6 + nile). Use this before snapshot_download to pick a region or kind. Equivalent to `trond snapshot sources -o json`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, snapshotSourcesTool)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "snapshot_list",
		Title:       "List available backups for a source",
		Description: "Returns the backup names available at a given mirror, newest-first. Mainnet sources are scraped from the Apache index; nile uses date generation. Equivalent to `trond snapshot list --network <net> -o json`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, snapshotListTool)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "snapshot_jobs",
		Title:       "List background download jobs",
		Description: "Inventory of detached snapshot downloads (started with snapshot_download + detach=true). Each row carries id, pid, running, last_log_line. Equivalent to `trond snapshot jobs -o json`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, snapshotJobsTool)

	mcp.AddTool(s, &mcp.Tool{
		Name:  "snapshot_download",
		Title: "Download a chain DB snapshot",
		Description: `Stream a snapshot tarball into a destination directory, gunzip + tar in one pipeline (no .tgz on disk). Pre-checks free disk space (HEAD probe + Statfs, requires 2× headroom). Refuses overwrite of an existing chain DB unless force=true. Preserves any pre-existing userdata/. MD5-verifies inline against the published sidecar when present.

Use dry_run=true to inspect the plan first. The tool emits MCP progress notifications during the actual download so the client can render a live progress bar. NOTE: this MCP tool runs the download in-process and blocks until completion or context cancellation; for fire-and-forget mainnet-full sized downloads (multi-hour) prefer the CLI with --detach.

Equivalent to ` + "`trond snapshot download --network <net> --to <dest> -o json`" + `.`,
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: ptrTrue(),
		},
	}, snapshotDownloadTool)
}

func snapshotSourcesTool(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
	rows := make([]map[string]any, 0, len(snapshot.SourceTable))
	for _, s := range snapshot.SourceTable {
		rows = append(rows, map[string]any{
			"network":        s.Network,
			"kind":           s.DBKind,
			"engine":         s.DBEngine,
			"region":         s.Region,
			"domain":         s.Domain,
			"base_url":       s.BaseURL,
			"approx_size_gb": s.ApproxSizeGB,
			"description":    s.Description,
		})
	}
	return jsonResult(map[string]any{"sources": rows})
}

func snapshotListTool(ctx context.Context, _ *mcp.CallToolRequest, args snapshotListArgs) (*mcp.CallToolResult, any, error) {
	src, err := pickSource(args.Domain, args.Network, args.Kind, args.Region, "")
	if err != nil {
		return errResult(err)
	}
	backups, err := snapshot.ListBackups(ctx, *src)
	if err != nil {
		return errResult(err)
	}
	return jsonResult(map[string]any{
		"domain":  src.Domain,
		"network": src.Network,
		"kind":    src.DBKind,
		"backups": backups,
	})
}

func snapshotJobsTool(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
	jobs, err := snapshot.ListJobs(paths.SnapshotJobs())
	if err != nil {
		return errResult(err)
	}
	statuses := make([]snapshot.JobStatus, 0, len(jobs))
	for _, j := range jobs {
		statuses = append(statuses, snapshot.Status(paths.SnapshotJobs(), j))
	}
	return jsonResult(map[string]any{"jobs": statuses})
}

func snapshotDownloadTool(ctx context.Context, req *mcp.CallToolRequest, args snapshotDownloadArgs) (*mcp.CallToolResult, any, error) {
	src, err := pickSource(args.Domain, args.Network, args.Kind, args.Region, "")
	if err != nil {
		return errResult(err)
	}

	backup := args.Backup
	if backup == "" {
		latest, err := snapshot.LatestBackup(ctx, *src)
		if err != nil {
			return errResult(err)
		}
		backup = latest
	}

	if args.Dest == "" {
		return errResult(fmt.Errorf("dest is required"))
	}

	kind := snapshot.DBKind(args.Kind)
	if kind == "" {
		kind = snapshot.DBKindLite
	}

	opts := snapshot.DownloadOptions{
		Source:         *src,
		Backup:         backup,
		Kind:           kind,
		DestDir:        args.Dest,
		Force:          args.Force,
		ExpectedSHA256: args.SHA256,
	}

	pre, err := snapshot.Preflight(ctx, opts)
	if err != nil {
		return errResult(err)
	}

	if args.DryRun {
		return jsonResult(map[string]any{
			"source":    src,
			"backup":    backup,
			"dest":      args.Dest,
			"preflight": pre,
		})
	}

	// Wire MCP progress notifications when the client supplied a token.
	// Without a token (older clients), the download still runs but no
	// progress is emitted — that's the protocol contract.
	if token := req.Params.GetProgressToken(); token != nil && pre.ExpectedSize > 0 {
		opts.ProgressFn = func(downloaded, total int64) {
			_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token,
				Progress:      float64(downloaded),
				Total:         float64(total),
				Message:       fmt.Sprintf("snapshot %s/%s", backup, kind),
			})
		}
	}

	res, err := snapshot.Download(ctx, opts)
	if err != nil {
		return errResult(err)
	}
	payload := map[string]any{
		"source":           src,
		"backup":           backup,
		"dest":             args.Dest,
		"bytes_downloaded": res.BytesDownloaded,
		"duration_ms":      res.DurationMs,
		"md5_verified":     res.MD5Verified,
		"actual_md5":       res.ActualMD5,
		"files_extracted":  res.FilesExtracted,
		"userdata_present": pre.UserdataPresent,
		"sha256":           res.SHA256,
		"sha256_verified":  res.SHA256Verified,
		// An MCP caller has no stderr to read the warning from, so the
		// cleartext-transport fact has to travel in the payload.
		"plaintext_transport": res.PlaintextTransport,
	}
	if res.ExpectedSHA256 != "" {
		payload["expected_sha256"] = res.ExpectedSHA256
	}
	return jsonResult(payload)
}

// pickSource resolves --domain / --network / --kind / --region into a
// Source struct, mirroring cmd/snapshot/list.go::resolveSource. We
// duplicate the small bit of logic rather than import cmd/ to keep
// the dependency graph one-way (cmd → internal, never the reverse).
func pickSource(domain, network, kind, region, engine string) (*snapshot.Source, error) {
	if domain != "" {
		s := snapshot.LookupDomain(domain)
		if s == nil {
			return nil, fmt.Errorf("unknown snapshot domain %q (call snapshot_sources for the full list)", domain)
		}
		return s, nil
	}
	if network == "" {
		return nil, fmt.Errorf("must pass network or domain (call snapshot_sources for available mirrors)")
	}
	f := snapshot.Filter{
		Network:  snapshot.Network(network),
		DBKind:   snapshot.DBKind(kind),
		Region:   snapshot.Region(region),
		DBEngine: snapshot.DBEngine(engine),
	}
	if f.DBKind == "" {
		f.DBKind = snapshot.DBKindLite
	}
	s := snapshot.Pick(f)
	if s == nil {
		return nil, fmt.Errorf("no source matches network=%s kind=%s region=%s",
			network, kind, region)
	}
	return s, nil
}
