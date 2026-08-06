// Assert TRC10 receivers hold exactly the amount sent.
//
// TRC10 balances live on the account (assetV2), not behind a contract call —
// which is the whole structural difference from the TRC20 lab, and the
// reason this is a separate script rather than a flag.
const fs = require('fs'), http = require('http');
const [host, trc10Id, ownerHex, receiversCsv, expectMoved] = process.argv.slice(2);
const post = (path, body) => new Promise((res, rej) => {
  const d = JSON.stringify(body);
  const r = http.request(host + path, { method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(d) } },
    s => { let b = ''; s.on('data', c => b += c); s.on('end', () => res(JSON.parse(b))); });
  r.on('error', rej); r.write(d); r.end();
});
const bal = async hexAddr => {
  const a = await post('/wallet/getaccount', { address: hexAddr });
  const hit = (a.assetV2 || []).find(e => String(e.key) === String(trc10Id));
  return hit ? BigInt(hit.value) : 0n;
};
(async () => {
  const rows = fs.readFileSync(receiversCsv, 'utf8').trim().split('\n').slice(1)
    .map(l => l.split(',')).filter(c => c.length >= 2);
  let held = 0n;
  for (const c of rows) held += await bal(c[1]);
  const owner = await bal(ownerHex);
  const ok = held.toString() === expectMoved;
  console.log(JSON.stringify({ trc10_id: trc10Id, receivers_total: held.toString(),
    owner_remaining: owner.toString(), expected: expectMoved, ok }));
  process.exit(ok ? 0 : 1);
})().catch(e => { console.error(e.message || e); process.exit(1); });
