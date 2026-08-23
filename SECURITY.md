# Security Policy

## Supported Versions

| Version | Supported |
|---|---|
| 0.1.x | Yes |

`0.1.x` is a `0.x` series under semantic versioning: the intent schema, the
`--output json` schemas and the exit-code contract may still change between
minor releases. Security fixes land on the newest `0.1.x`.

## Reporting a Vulnerability

If you discover a security vulnerability in trond, please report it responsibly.

**Do NOT open a public GitHub issue for security vulnerabilities.**

### Reporting Channel

Email: **security@tron.network**

Include:
- Description of the vulnerability
- Steps to reproduce
- Affected version(s)
- Impact assessment (if known)

### Response SLA

| Stage | Timeline |
|---|---|
| Acknowledgment | Within 48 hours |
| Initial assessment | Within 5 business days |
| Fix or mitigation | Within 30 days for critical issues |

### What to Expect

1. We will acknowledge your report within 48 hours
2. We will provide an initial assessment of the issue
3. We will work on a fix and coordinate disclosure
4. You will be credited in the security advisory (unless you prefer anonymity)

## Security Design

trond incorporates the following security measures:

- **Private key protection**: Witness private keys are passed via environment variables, never written into intent files, and the `PrivateKey` type redacts them in every string representation and JSON serialization. They are not, however, absent from disk: at apply time the key is inlined into the rendered HOCON, because that is where java-tron reads `localwitness` from. That file is created 0600. Treat the rendered config as key material.
- **Public private-net key**: `private_net_config.conf` ships a *published* witness key (`a31d54…acae`, address `TM4yToQ1njkcFwi3ADY5x6dbdfNekU3rVi`) because the private network's genesis block is derived from it — without it the template cannot produce blocks. It is public, it is in this repository, and it must never be used on mainnet, Nile, or any network carrying value.
- **SSH command whitelist**: Only pre-approved commands are executed over SSH connections.
- **Audit log**: All mutating operations (apply, stop, start, remove, upgrade, rollback, network create/destroy) are logged in append-only JSONL format to `~/.trond/audit.log`, or to `<dir>/audit.log` when `--state-dir` / `TROND_STATE_DIR` relocates the state directory.
- **Confirmation gates**: Destructive operations (remove, destroy) require explicit `--confirm` flags.
- **State file permissions**: State and audit files are created with restricted permissions (0600/0700).

## Scope

The following are in scope for security reports:

- Command injection via intent files or CLI arguments
- Private key leakage through logs, output, or state files
- SSH connection security issues
- Privilege escalation via trond commands
- State file tampering leading to unintended deployments

Out of scope:
- Security of the java-tron node software itself (report to [java-tron](https://github.com/tronprotocol/java-tron))
- Vulnerabilities in Docker or systemd
- Social engineering
