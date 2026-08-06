// Run a verifier with the expected total computed from the load parameters,
// rather than a literal. A hardcoded expectation silently stops matching
// the moment someone changes tx_count.
const { spawnSync } = require('child_process');
const a = process.argv.slice(2);
const verifier = a[0], rest = a.slice(1, -2);
const count = BigInt(a[a.length - 2]), amount = BigInt(a[a.length - 1]);
const r = spawnSync(process.execPath, [verifier, ...rest, (count * amount).toString()],
  { stdio: 'inherit' });
process.exit(r.status === null ? 1 : r.status);
