# token-lab — TRC20 and TRC10 on a private chain

Two recipes that stand up a private chain, mint a token, drive transfers
through it with `txgen`, and assert the receivers hold exactly what was sent.

```bash
npm install                                  # once — tronweb, for signing
export SR_PRIVATE_KEY=da146374a75310b9666e834ee4ad0866d6f4035967bfc76217c5a495fff9f0d0

trond recipe run --file examples/token-lab/trc20.yaml --allow-host-exec \
  --param lab_dir=examples/token-lab --param sender_key=$SR_PRIVATE_KEY

trond recipe run --file examples/token-lab/trc10.yaml --allow-host-exec \
  --param lab_dir=examples/token-lab --param sender_key=$SR_PRIVATE_KEY
```

Around 8–12 seconds each once the java-tron image is pulled and
`node_modules` exists. First run on a fresh machine is a few minutes, almost
all of it the 678 MB image.

`--allow-host-exec` is required because minting, loading and verifying all
happen outside trond. The recipes are refused outright under
`--require-private`, which is correct: a host step names no node, so the
gate cannot vouch for it.

## The part worth reading: chain parameters

**A private chain will happily deploy a contract that can never execute, and
happily hold a TRC10 that nothing can transfer.** Both failures look like
broken tooling rather than a mis-seeded chain, and both cost real time to
diagnose. `network.yaml` sets four things because of it:

| parameter | without it |
|---|---|
| `vm.supportConstant` | read-only contract calls are refused with "this node does not support constant" |
| `committee.allowTvmConstantinople`<br>`committee.allowTvmSolidity059`<br>`committee.allowTvmIstanbul` | the deploy returns `contractRet: SUCCESS`, and then **every call returns empty with no energy used** — it reads as a broken contract, not a chain missing its TVM upgrades |
| `committee.allowSameTokenName` | `transferasset` wants the token **name**, not its numeric id. Every tool that sends the id — txgen included — gets `No asset!`, as though the asset was never issued |

`allowCreationOfContracts` is already 1 in the shipped private template,
which is what makes the TVM case so confusing: deployment works, execution
does not.

**These seed the dynamic property store at genesis.** Changing them on a
running chain does nothing — the chain has to be recreated. `trond` will
restart the node for you when the config changes, and the restart is real,
but the already-seeded properties do not move.

`getchainparameters` is the way to check rather than reason:

```bash
curl -s -X POST http://127.0.0.1:8390/wallet/getchainparameters \
  | python3 -c "import sys,json;[print(p['key'],'=',p.get('value','(unset)')) \
      for p in json.load(sys.stdin)['chainParameter'] if 'Tvm' in p['key'] or 'TokenName' in p['key']]"
```

## Layout

```
network.yaml              the chain, and the four parameters above
trc20.yaml / trc10.yaml   the recipes
contract/TestToken.sol    minimal TRC20 — transfer + balanceOf, what txgen drives
contract/abi.json         compiled artifacts, committed so no solc is needed
contract/bytecode.hex
scripts/deploy.js         TRC20 deploy   (prints JSON -> {{ steps.deploy.address_hex }})
scripts/issue-asset.js    TRC10 issuance (prints JSON -> {{ steps.issue.trc10_id }})
scripts/verify-*.js       balance assertions
scripts/expect.js         computes tx_count * amount for the verifier
txgen-*.tmpl.json         txgen configs, filled in by the load step
```

The artifacts are committed rather than compiled on demand, so running this
needs no solc. If you change `TestToken.sol`, recompile with **evmVersion
`istanbul`** — solc 0.8.20+ emits `PUSH0`, which java-tron's TVM does not
implement:

```bash
npx solc@0.8.18 --optimize --evm-version istanbul --abi --bin contract/TestToken.sol
```

## How the two differ

The shape is identical — create, await, mint, load, verify — and only two
steps differ:

- **Minting.** TRC20 deploys a contract; TRC10 issues an asset. An account
  may issue only one asset, so `issue-asset.js` reuses an existing one
  rather than failing, because recipes get re-run.
- **Verification.** A TRC20 balance sits behind a contract call; a TRC10
  balance sits on the account in `assetV2`.

Adding a third token type is those two steps and nothing else.

Two encoding traps if you write your own scripts against the HTTP API:
`getaccount` returns `asset_issued_name` **hex-encoded** but
`asset_issued_ID` as a **plain decimal string** — hex-decoding the id yields
bytes that still look like a value (`"1000001"` becomes `0x10 0x00 0x00`),
so the mistake survives as plausible data instead of erroring. And
`createassetissue` wants `name`, `abbr`, `description` and `url` hex-encoded.

## The key

`SR_PRIVATE_KEY` above is java-tron's published private-net key. It already
appears in `AGENTS.md` and the shipped `private_net_config.conf`; it is not a
secret and controls nothing outside a local chain.
