# Cloud Deployment Guide

## Recommended Instance Types

### AWS

| Node Type | Instance | vCPU | RAM | Notes |
|---|---|---|---|---|
| Lite FullNode | m6i.2xlarge | 8 | 32 GB | Development, light API |
| FullNode | m6i.4xlarge | 16 | 64 GB | Production API |
| Witness | c6i.4xlarge | 16 | 32 GB | Block production (compute-optimized) |
| High-traffic API | m6i.8xlarge | 32 | 128 GB | Heavy query workloads |

### GCP

| Node Type | Instance | vCPU | RAM |
|---|---|---|---|
| Lite FullNode | n2-standard-8 | 8 | 32 GB |
| FullNode | n2-standard-16 | 16 | 64 GB |
| Witness | c2-standard-16 | 16 | 64 GB |

### Azure

| Node Type | Instance | vCPU | RAM |
|---|---|---|---|
| Lite FullNode | Standard_D8s_v5 | 8 | 32 GB |
| FullNode | Standard_D16s_v5 | 16 | 64 GB |
| Witness | Standard_F16s_v2 | 16 | 32 GB |

## Storage

### Requirements

- **Type**: SSD only. HDD is not viable for java-tron due to random I/O patterns.
- **IOPS**: Minimum 3,000 IOPS for FullNode; 10,000+ IOPS recommended for Witness.
- **Throughput**: 250 MB/s minimum.
- **Size**: 2 TB for FullNode (plan for growth), 500 GB for Lite FullNode.

### Cloud Storage Options

| Provider | Recommended Volume | Notes |
|---|---|---|
| AWS | gp3 (3,000-16,000 IOPS configurable) | Cost-effective; adjust IOPS independently |
| AWS | io2 | For Witness nodes needing guaranteed IOPS |
| GCP | pd-ssd | Standard SSD persistent disk |
| GCP | pd-extreme | For high IOPS witness workloads |
| Azure | Premium SSD v2 | Configurable IOPS and throughput |

### Storage Tips

- Enable volume encryption at rest (negligible performance impact on modern instances)
- Use a separate volume for the database directory (not the root volume)
- Enable cloud disk snapshots on a daily schedule for backup
- Monitor disk queue depth; sustained values above 4 indicate an IOPS bottleneck

## Deployment Workflow

### Terraform + SSH + trond

The recommended workflow provisions infrastructure with Terraform, then uses trond over SSH to configure and manage nodes.

**Step 1: Provision with Terraform**
- Create compute instance, SSD volume, security group, and elastic/static IP
- Output the instance public IP and SSH key path

**Step 2: Prepare the intent + preflight**

SSH targets are declared in the intent file's `target:` block (full
example: `examples/remote-ssh-fullnode.yaml`):

```yaml
target:
  type: ssh
  host: node01.example.com
  user: deploy
  port: 22
  identity_file: ~/.ssh/id_rsa
  runtime: docker
```

Then preflight that intent:
```bash
trond preflight --intent intent.yaml --output json
```
This checks JDK, disk, memory, and port availability on the target.

**Step 3: Deploy**
```bash
trond apply --intent intent.yaml --output json
```

**Step 4: Verify**
```bash
trond status <node-name> --output json
```

### Infrastructure as Code Tips

- Tag instances with `role=tron-fullnode` or `role=tron-witness` for inventory management
- Use launch templates or instance templates for reproducible deployments
- Store intent files alongside Terraform configs in the same repository

## Networking

### Security Groups / Firewall Rules

Inbound rules:
- TCP 18888 from 0.0.0.0/0 (P2P -- required for sync)
- TCP 8090, 8091, 50051, 50061 from trusted CIDR only (API access)
- TCP 9527 from monitoring CIDR only (Prometheus)
- TCP 22 from management CIDR only (SSH)

Outbound rules:
- Allow all outbound (required for P2P peer connections)

### Load Balancer Considerations

- **Use case**: Distribute API traffic across multiple FullNode instances
- **Type**: Layer 4 (TCP) load balancer for gRPC; Layer 7 (HTTP) for REST API
- **Health check**: HTTP GET `/wallet/getnowblock` on port 8090; expect 200
- **Session affinity**: Not required for stateless API calls
- **Do not load-balance P2P ports** -- each node must maintain its own peer connections
- **Do not load-balance Witness nodes** -- only one instance should produce blocks

### DNS and Static IPs

- Assign a static/elastic IP to Witness nodes (peer reputation is tied to IP)
- For API nodes behind a load balancer, static IPs on individual instances are optional
- Use DNS names in intent files for portability

## Multi-Region Considerations

- Witness nodes benefit from low-latency regions close to other SRs (primarily East Asia)
- API nodes can be deployed in any region close to your users
- Cross-region database replication is not supported; each node syncs independently
- Use health-check-based DNS (Route 53, Cloud DNS) to route users to the nearest healthy node

## Cost Optimization

- Use reserved instances or committed use discounts for long-running nodes (30-50% savings)
- Lite FullNode uses significantly less storage, reducing cost for non-archival use cases
- Schedule non-production nodes to shut down outside business hours
- Right-size instances after monitoring actual CPU/memory usage for two weeks
