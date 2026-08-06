// Read TRC20 balances back off the chain and assert the arithmetic.
// Emits JSON so a later recipe step can reference it.
const fs=require('fs'), http=require('http');
const [host, contractHex, ownerHex, receiversCsv, expectMoved] = process.argv.slice(2);
const post=(path,body)=>new Promise((res,rej)=>{
  const d=JSON.stringify(body);
  const r=http.request(host+path,{method:'POST',headers:{'Content-Type':'application/json','Content-Length':d.length}},
    s=>{let b='';s.on('data',c=>b+=c);s.on('end',()=>res(JSON.parse(b)))});
  r.on('error',rej); r.write(d); r.end();
});
const bal=async hex=>{
  const d=await post('/wallet/triggerconstantcontract',{owner_address:ownerHex,contract_address:contractHex,
    function_selector:'balanceOf(address)',parameter:'0'.repeat(24)+hex.slice(2)});
  const cr=(d.constant_result||[''])[0];
  return cr? BigInt('0x'+cr) : 0n;
};
(async()=>{
  const rows=fs.readFileSync(receiversCsv,'utf8').trim().split('\n').slice(1)
    .map(l=>l.split(',')).filter(c=>c.length>=2);
  let held=0n;
  for(const c of rows) held += await bal(c[1]);
  const owner=await bal(ownerHex);
  const moved=held.toString();
  const ok = moved === expectMoved;
  console.log(JSON.stringify({receivers_total:moved, owner_remaining:owner.toString(), expected:expectMoved, ok}));
  process.exit(ok?0:1);
})().catch(e=>{console.error(e.message||e);process.exit(1)});
