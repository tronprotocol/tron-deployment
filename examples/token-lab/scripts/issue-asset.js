// Issue a TRC10 asset and print its numeric id as JSON.
//
// The unsigned transaction is built by the node's own /wallet/createassetissue
// rather than TronWeb's transactionBuilder: the builder's endpoint shape
// varies between TronWeb majors (v6 returned 405 here), while the HTTP API
// is the node's published contract. TronWeb is used only to sign and
// broadcast, which is stable.
const TronWeb = require('tronweb');
const http = require('http');
const HOST = process.env.TRON_HOST || 'http://127.0.0.1:8390';
const PK = process.env.TRON_PK;
if (!PK) { console.error('TRON_PK is required'); process.exit(2); }
const tw = new (TronWeb.TronWeb || TronWeb)({ fullHost: HOST, privateKey: PK });
const hex = s => Buffer.from(s, 'utf8').toString('hex');

const post = (path, body) => new Promise((res, rej) => {
  const d = JSON.stringify(body);
  const r = http.request(HOST + path, { method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(d) } },
    s => { let b = ''; s.on('data', c => b += c); s.on('end', () => res(JSON.parse(b))); });
  r.on('error', rej); r.write(d); r.end();
});

// getaccount mixes encodings in one response: asset_issued_name is hex,
// asset_issued_ID is a plain decimal string. Hex-decoding the id yields
// bytes that still look like a value ("1000001" -> 0x10 0x00 0x00), so the
// mistake survives as plausible-looking data rather than erroring.
const issuedID = async ownerHex => {
  const a = await post('/wallet/getaccount', { address: ownerHex });
  return a.asset_issued_ID ? String(a.asset_issued_ID) : null;
};

(async () => {
  const ownerB58 = tw.address.fromPrivateKey(PK);
  const ownerHex = tw.address.toHex(ownerB58);

  // An account may issue only one asset, so a re-run must find the existing
  // one rather than fail. Recipes get re-run.
  const existing = await issuedID(ownerHex);
  if (existing) { console.log(JSON.stringify({ trc10_id: existing, reused: true })); return; }

  const now = Date.now();
  const unsigned = await post('/wallet/createassetissue', {
    owner_address: ownerHex,
    name: hex('TrondTest10'), abbr: hex('TT10'),
    total_supply: 1_000_000_000_000, trx_num: 1, num: 1,
    start_time: now + 5_000, end_time: now + 3_600_000,
    description: hex('trond token-lab TRC10'),
    url: hex('https://example.invalid'),
    precision: 6,
  });
  if (!unsigned.raw_data) throw new Error('createassetissue: ' + JSON.stringify(unsigned).slice(0, 300));

  const res = await tw.trx.sendRawTransaction(await tw.trx.sign(unsigned, PK));
  if (!res.result) throw new Error('broadcast: ' + JSON.stringify(res).slice(0, 300));

  // The id is assigned at execution, so read it back off the account.
  for (let i = 0; i < 15; i++) {
    await new Promise(r => setTimeout(r, 2000));
    const id = await issuedID(ownerHex);
    if (id) { console.log(JSON.stringify({ trc10_id: id, reused: false })); return; }
  }
  throw new Error('asset issued but no id appeared on the account');
})().catch(e => { console.error('issue-asset failed:', e.message || e); process.exit(1); });
