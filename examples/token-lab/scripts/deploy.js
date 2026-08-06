// Deploy the compiled TRC20 to a private chain and print the address.
// Kept dependency-light on purpose: this is what a `kind: host` recipe step
// invokes, so it must be runnable with node and nothing assumed.
const fs = require('fs');
const TronWeb = require('tronweb');
const HOST = process.env.TRON_HOST || 'http://127.0.0.1:8390';
const PK = process.env.TRON_PK;
if (!PK) { console.error('TRON_PK is required'); process.exit(2); }

const tw = new (TronWeb.TronWeb || TronWeb)({
  fullHost: HOST, privateKey: PK,
});

(async () => {
  const abi = JSON.parse(fs.readFileSync(__dirname + '/../contract/abi.json', 'utf8'));
  const bytecode = fs.readFileSync(__dirname + '/../contract/bytecode.hex', 'utf8').trim();
  const c = await tw.contract().new({
    abi, bytecode,
    feeLimit: 1_000_000_000,
    callValue: 0,
    userFeePercentage: 100,
    originEnergyLimit: 10_000_000,
    parameters: ['1000000000000'],   // 1e12 base units
  });
  const hex = tw.address.toHex(c.address);
  console.log(JSON.stringify({ address_base58: tw.address.fromHex(hex), address_hex: hex,
    txid: c.transactionId || (c.deployed && c.deployed.txid) || null }));
})().catch(e => { console.error('deploy failed:', e.message || e); process.exit(1); });
