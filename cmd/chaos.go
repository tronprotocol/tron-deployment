package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// Chaos primitives — disconnect, connect, partition, heal.
//
// These exist so test harnesses can simulate network faults (peer drops,
// chain-splits, link flapping) without going under trond's back to docker
// or iptables. Each primitive maps to docker network membership changes:
// "disconnect" detaches the container from a network, "connect" re-attaches.
// "partition" / "heal" are convenience wrappers over the pair primitives.
//
// All chaos commands operate on docker-runtime nodes with a "local" target.
// jar / SSH targets are out of scope for now — fault injection on
// systemd-managed processes typically goes through tc / iptables and is
// better handled by a dedicated chaos tool.

var disconnectCmd = &cobra.Command{
	Use:   "disconnect <node-a> <node-b>",
	Short: "Disconnect two managed nodes at the docker network layer",
	Long: `Detach <node-a> from every docker network it shares with <node-b>.

After this runs, packets cannot flow between A and B; their docker compose
project is otherwise untouched (volumes, env, restart policy preserved).
Reverse with: trond connect <node-a> <node-b>.`,
	Args: cobra.ExactArgs(2),
	RunE: runDisconnect,
}

var connectCmd = &cobra.Command{
	Use:   "connect <node-a> <node-b>",
	Short: "Reconnect two managed nodes that were previously disconnected",
	Args:  cobra.ExactArgs(2),
	RunE:  runConnect,
}

var partitionCmd = &cobra.Command{
	Use:   "partition --groups 'a,b|c,d'",
	Short: "Split managed nodes into isolated groups",
	Long: `Apply a network partition. Nodes inside a group remain connected to
each other; cross-group traffic is dropped at the docker network layer.

Example:
    trond partition --groups 'sr0,sr1 | sr2,fullnode0'

reproduce a 2/2 split. Reverse with: trond heal --groups '...'`,
	RunE: runPartition,
}

var healCmd = &cobra.Command{
	Use:   "heal --groups 'a,b|c,d'",
	Short: "Reverse a previously applied partition",
	RunE:  runHeal,
}

var partitionGroups string

func init() {
	rootCmd.AddCommand(disconnectCmd)
	rootCmd.AddCommand(connectCmd)
	partitionCmd.Flags().StringVar(&partitionGroups, "groups", "", "Pipe-separated, comma-separated groups (e.g. 'a,b|c,d')")
	healCmd.Flags().StringVar(&partitionGroups, "groups", "", "Same shape as 'partition --groups'")
	if err := partitionCmd.MarkFlagRequired("groups"); err != nil {
		panic(err)
	}
	if err := healCmd.MarkFlagRequired("groups"); err != nil {
		panic(err)
	}
	rootCmd.AddCommand(partitionCmd)
	rootCmd.AddCommand(healCmd)
}

func runDisconnect(cmd *cobra.Command, args []string) error {
	return chaosPair(cmd.Context(), "disconnect", args[0], args[1])
}

func runConnect(cmd *cobra.Command, args []string) error {
	return chaosPair(cmd.Context(), "connect", args[0], args[1])
}

func runPartition(cmd *cobra.Command, args []string) error {
	return chaosGroups(cmd.Context(), "disconnect", partitionGroups)
}

func runHeal(cmd *cobra.Command, args []string) error {
	return chaosGroups(cmd.Context(), "connect", partitionGroups)
}

// chaosPair detaches/attaches both directions on every shared network.
// Done once per direction so the operation is idempotent for connect after
// disconnect (docker errors on duplicate connect are benign and ignored).
func chaosPair(ctx context.Context, op, a, b string) error {
	store, err := state.NewStore(statePath())
	if err != nil {
		return err
	}
	deployState, err := store.Load()
	if err != nil {
		return output.NewError("STATE_ERROR", output.ExitGeneralError, err.Error())
	}
	nodeA := store.GetNode(deployState, a)
	nodeB := store.GetNode(deployState, b)
	if nodeA == nil {
		return output.NewError("NODE_NOT_FOUND", output.ExitGeneralError, "node "+a+" not found in state")
	}
	if nodeB == nil {
		return output.NewError("NODE_NOT_FOUND", output.ExitGeneralError, "node "+b+" not found in state")
	}
	// --require-private: both endpoints must be private before we touch the
	// rig's network. Uses the already-loaded nodes (no extra state read).
	if err := guard.EnforceNodes([]guard.NodeRef{
		{Name: nodeA.Name, Network: nodeA.Network},
		{Name: nodeB.Name, Network: nodeB.Network},
	}); err != nil {
		return err
	}
	// Chaos works through "docker network ..." on the local host. Both jar
	// runtime and SSH targets fall outside that — the harness should reach
	// for tc / iptables on those.
	for _, n := range []*state.ManagedNode{nodeA, nodeB} {
		if n.Runtime != "docker" || n.Target.Type != "local" {
			return output.NewError("UNSUPPORTED", output.ExitGeneralError,
				"chaos primitives currently require docker runtime on a local target; "+
					n.Name+" is "+n.Runtime+"/"+n.Target.Type)
		}
	}

	// Network-set selection differs by op:
	//   disconnect — operate on networks A and B currently share, since
	//                that's exactly the link we're tearing down.
	//   connect    — A may have already been disconnected (so the
	//                intersection is empty); fall back to B's networks so
	//                we can reattach A to the same plane.
	aNets, err := dockerNetworksOf(ctx, a)
	if err != nil {
		return output.NewError("CHAOS_ERROR", output.ExitGeneralError, err.Error())
	}
	bNets, err := dockerNetworksOf(ctx, b)
	if err != nil {
		return output.NewError("CHAOS_ERROR", output.ExitGeneralError, err.Error())
	}

	var targets []string
	switch op {
	case "disconnect":
		targets = intersect(aNets, bNets)
	case "connect":
		// Prefer the intersection so we don't accidentally over-connect; if
		// empty (A is detached), use B's networks as the rejoin set.
		targets = intersect(aNets, bNets)
		if len(targets) == 0 {
			targets = bNets
		}
	}

	if len(targets) == 0 {
		// For disconnect, "no shared network" can mean we already detached
		// A as part of an earlier pair in the same partition call. That's
		// the desired end state, so treat it as a no-op success and
		// surface it explicitly in the result for caller observability.
		if op == "disconnect" && len(aNets) == 0 {
			output.WriteJSON(os.Stdout, map[string]any{
				"op":     op,
				"node_a": a,
				"node_b": b,
				"note":   "already disconnected — no networks remain on " + a,
			})
			return nil
		}
		return output.NewError("CHAOS_ERROR", output.ExitGeneralError,
			fmt.Sprintf("%s and %s share no docker network (op=%s)", a, b, op))
	}

	for _, net := range targets {
		switch op {
		case "disconnect":
			if _, err := localDockerExec(ctx, "network", "disconnect", net, a); err != nil {
				// Already-disconnected is fine; report all others.
				if !strings.Contains(err.Error(), "is not connected") {
					return output.NewError("CHAOS_ERROR", output.ExitGeneralError, err.Error())
				}
			}
		case "connect":
			if _, err := localDockerExec(ctx, "network", "connect", net, a); err != nil {
				if !strings.Contains(err.Error(), "already exists") &&
					!strings.Contains(err.Error(), "endpoint with name") {
					return output.NewError("CHAOS_ERROR", output.ExitGeneralError, err.Error())
				}
			}
		}
	}

	output.WriteJSON(os.Stdout, map[string]any{
		"op":       op,
		"node_a":   a,
		"node_b":   b,
		"networks": targets,
	})
	return nil
}

// chaosGroups parses a 'g1,n1|g1,n2|...' spec and applies the op to every
// cross-group pair. Within-group pairs are left untouched.
func chaosGroups(ctx context.Context, op, spec string) error {
	groups := parseGroups(spec)
	if len(groups) < 2 {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			"--groups must contain at least two groups separated by '|'")
	}

	// --require-private: enforce across EVERY node in the spec up front, so
	// a non-private member refuses with one clean error before any pair is
	// touched (chaosGroups accumulates per-pair errors and continues, so a
	// per-pair guard alone would partially apply / spam errors).
	var allNodes []string
	for _, g := range groups {
		allNodes = append(allNodes, g...)
	}
	if err := requirePrivateForNodes(allNodes...); err != nil {
		return err
	}

	var pairs [][2]string
	for i := range groups {
		for j := i + 1; j < len(groups); j++ {
			for _, a := range groups[i] {
				for _, b := range groups[j] {
					pairs = append(pairs, [2]string{a, b})
				}
			}
		}
	}

	results := make([]map[string]any, 0, len(pairs))
	for _, p := range pairs {
		if err := chaosPair(ctx, op, p[0], p[1]); err != nil {
			results = append(results, map[string]any{
				"op":     op,
				"node_a": p[0],
				"node_b": p[1],
				"error":  err.Error(),
			})
		}
	}
	if len(results) > 0 {
		output.WriteJSON(os.Stdout, map[string]any{"errors": results})
		return output.NewError("CHAOS_ERROR", output.ExitGeneralError,
			fmt.Sprintf("%d pair operation(s) failed", len(results)))
	}
	return nil
}

// parseGroups breaks "a,b | c,d" into [["a","b"], ["c","d"]]. Whitespace
// around commas and pipes is trimmed.
func parseGroups(spec string) [][]string {
	var out [][]string
	for g := range strings.SplitSeq(spec, "|") {
		var members []string
		for m := range strings.SplitSeq(g, ",") {
			m = strings.TrimSpace(m)
			if m != "" {
				members = append(members, m)
			}
		}
		if len(members) > 0 {
			out = append(out, members)
		}
	}
	return out
}

// dockerNetworksOf lists every docker network the container is currently
// attached to. We use the inspect template so we don't have to parse JSON.
func dockerNetworksOf(ctx context.Context, container string) ([]string, error) {
	out, err := localDockerExec(ctx, "inspect", "-f", "{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}", container)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(out)
	return fields, nil
}

func intersect(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, x := range b {
		set[x] = true
	}
	var out []string
	for _, x := range a {
		if set[x] {
			out = append(out, x)
		}
	}
	return out
}
