# Driving trond from a Test Harness

trond is a deployment substrate. External test tools (CI scripts, custom
harnesses) drive it programmatically and bring their own assertions, traffic
generation, and reporting.

This guide describes the contract trond exposes and the conventions a
harness should follow.

## Isolation: `--state-dir`

Every concurrent harness run must use its own state directory. Without this,
two runs share `~/.trond/state.json` and collide on node names, the file
lock, and audit-log lines.

```bash
T="trond --state-dir /tmp/trond-$JOB_ID"
$T apply --intent fixture.yaml --auto-approve
```

`TROND_STATE_DIR` works the same and is convenient when wrapping trond from
another tool that already exports the variable.

## Port collisions: `auto_ports`

Set `target.auto_ports: true` in the intent. trond replaces every port
matching a java-tron default with an OS-allocated free port. The new ports
are persisted to state and surfaced in `inspect` output.

```yaml
target:
  type: local
  runtime: docker
  auto_ports: true
nodes:
  - type: fullnode
    image: tronprotocol/java-tron
```

## Discovering the topology: `inspect`

After deploy, query the manifest:

```bash
$T inspect --all -o json
```

Each node entry contains `endpoints` (http, grpc), `container_ip` (best
effort), `runtime`, `version`, `status`, `intent_hash`, `config_hash`.

For a multi-node network use `--network <prefix>` to scope.

## Blocking until ready: `apply --wait` and `wait`

Combined deploy + ready:

```bash
$T apply --intent fixture.yaml --auto-approve --wait --wait-timeout 5m
```

Standalone probe:

```bash
$T wait fullnode0 \
    --http '{http}/wallet/getnowblock' \
    --json-path block_header.raw_data.number \
    --json-gt 0 \
    --timeout 5m
```

Three probe families: `--port` (TCP from the trond host), `--http` (curl
inside the node, with optional `--json-path` + `--json-eq` / `--json-gt`),
and `--exec` (success on exit 0).

## Operating on nodes: `exec`, `files`

```bash
# Run a command inside a node.
$T exec fullnode0 -- curl -s http://127.0.0.1:8090/wallet/getnowblock

# Push a fixture into a node.
$T files put sr0 ./fixtures/genesis.conf /etc/tron/config.conf

# Pull logs out for the harness's own reporting.
$T files get sr0 /var/log/tron/tron.log /tmp/sr0.log
```

`files` is `docker cp` for docker nodes and direct host I/O for jar nodes.
`exec` is `docker exec` for docker nodes and direct host exec for jar nodes.

## Fault injection: `disconnect`, `partition`

```bash
# Drop a single peer link.
$T disconnect sr0 fullnode0

# 2/2 split.
$T partition --groups 'sr0,sr1 | sr2,fullnode0'

# Restore.
$T heal --groups 'sr0,sr1 | sr2,fullnode0'
$T connect sr0 fullnode0
```

These primitives operate on the docker network layer and only support
docker-runtime nodes on a local target. For jar / SSH targets, drive
`tc` / `iptables` directly.

## Subscribing to events: `events --follow`

Replace polling with a JSONL subscription:

```bash
$T events --follow > /tmp/trond-$JOB_ID/events.jsonl &
EVENTS_PID=$!
# ... run scenario ...
kill $EVENTS_PID
```

Each line is the audit-log entry for a mutating command (apply, stop, start,
remove, upgrade, rollback, restart, network create/destroy/add). Use
`--since <duration>` to filter to a recent window.

## Bringing up a private network

`trond network create` is the typical entry point for a multi-node
private testnet. trond pre-renders all node intents, then deploys them
in order. Two automations make this robust without per-node hand
configuration:

1. **`auto_ports: true`** under `target:` allocates fresh OS ports per
   node so concurrent enclaves don't collide on host ports. The
   in-container P2P port is randomized too, so peers must be addressed
   by container name + the rendered port.

2. **Auto-wired peering**: trond fills each node's
   `network_overrides.active_peers` with the addresses of every other
   node in the network using the form `<container-name>:<rendered-p2p-port>`
   — UNLESS the user supplied an explicit list (even an empty one).
   That makes the docker-network DNS resolve to the right port.

A complete 2-node private intent looks like:

```yaml
name: pn
network: private
target:
  type: local
  runtime: docker
  auto_ports: true
nodes:
  # Block-producing SR
  - type: witness
    image: tronprotocol/java-tron
    resources: {memory: 4GB}
    witness_key:
      private_key_env: SR_KEY    # value inlined into rendered HOCON
    networks: [tron-pn-mesh]     # pre-create with `docker network create`
    network_overrides:
      p2p_version: 88888         # unique value; isolates from public networks
      discovery: false
      need_sync_check: false     # required for first SR of a fresh chain

  # Fullnode that follows the SR
  - type: fullnode
    image: tronprotocol/java-tron
    resources: {memory: 4GB}
    networks: [tron-pn-mesh]
    network_overrides:
      p2p_version: 88888
      discovery: false
      # active_peers auto-wired to ["pn-node0:<p2p>"] by trond
```

Driver script:

```bash
docker network create tron-pn-mesh
SR_KEY=a31d54825aea2fc5127e3bd435fc2346021313005e5f304ab33372432784acae \
  trond --state-dir /tmp/trond-$JOB \
  network create --intent pn.yaml -o json
```

Verify success by tailing the logs volumes:

```bash
docker run --rm -v pn-node0_pn-node0-logs:/L alpine \
  grep -c "Produce block successfully" /L/tron.log    # SR is producing
docker run --rm -v pn-node1_pn-node1-logs:/L alpine \
  grep -oE 'header number = [0-9]+' /L/tron.log | tail -1    # fullnode synced height
```

The default private-net witness private key
(`a31d54825aea2fc5127e3bd435fc2346021313005e5f304ab33372432784acae`) matches
the genesis address `TM4yToQ1njkcFwi3ADY5x6dbdfNekU3rVi` baked into the
`private_net_config.conf` template. Use that exact key unless you also
supply a fresh genesis block.

This key is **public** — it ships in the template and in this document, so
treat any rig using it as unauthenticated: anyone who can reach the node can
sign as the witness and spend the genesis balance. That is fine for a
throwaway test network on a trusted host and unacceptable anywhere else.
Never reuse it on Mainnet, Nile, or any network carrying value.

## Tear-down: always clean up

```bash
$T network destroy --confirm <network-name>
# or for single-node:
$T remove <node> --confirm <node>
rm -rf /tmp/trond-$JOB_ID
```

The `--confirm` gate prevents accidental destruction; in CI mode pass the
network name explicitly to satisfy it.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | general error (incl. WAIT_TIMEOUT, EXEC_ERROR, CHAOS_ERROR, NODE_NOT_FOUND) |
| 2 | validation error |
| 3 | target unreachable |
| 4 | preflight failure |
| 10 | human required (destructive op needs --confirm or --auto-approve) |

Match on the exit code first, then on the `code` field of the structured
error JSON, which is more specific (e.g. `WAIT_TIMEOUT`, `UNSUPPORTED`).

## Output schema stability

Every command supports `-o json`. Field names are stable; new fields may be
added without notice. If a field is absent, treat it as zero / unset.
