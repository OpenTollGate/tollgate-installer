import { createServer } from 'http';
const ALLOT = 104857600; let usage = 0;
const adv = { id:'aa'.repeat(32), pubkey:'bb'.repeat(32), created_at:1700000000, kind:10021,
  tags:[['metric','bytes'],['step_size','10485760'],['step_purchase_limits','1','0'],['tips','1','2','3','4'],
        ['price_per_step','cashu','210','sat','https://testnut.cashu.space',1]], content:'', sig:'cc'.repeat(64) };
const H = { 'Access-Control-Allow-Origin':'*', 'Access-Control-Allow-Headers':'*', 'Access-Control-Allow-Methods':'GET,POST,OPTIONS' };
createServer((req, res) => {
  if (req.method === 'OPTIONS') { res.writeHead(204, H); return res.end(); }
  const u = req.url.split('?')[0];
  const send = (body, code = 200, ct = 'application/json') => { res.writeHead(code, { ...H, 'Content-Type': ct }); res.end(body); };
  if (req.method === 'POST' && u === '/') {
    let data = ''; req.on('data', (c) => (data += c)); req.on('end', () => { usage = 0; send(JSON.stringify({ success: true, session_active: true, added: ALLOT })); });
    return;
  }
  if (u === '/') return send(JSON.stringify(adv));
  if (u === '/usage') return send(`${usage}/${ALLOT}`, 200, 'text/plain');
  if (u === '/balance') return send(JSON.stringify({ success: true, session_active: usage > 0, usage, allotment: ALLOT, remaining: ALLOT - usage }));
  if (u === '/whoami') return send('mac=00:11:22:33:44:55', 200, 'text/plain');
  if (u === '/ln-invoice') { if (!req.url.includes('amount')) return send(JSON.stringify({ status: 0, error: 'quote is required' })); return send(JSON.stringify({ status: 1, invoice: 'lnbc2100n1ptestnutfakeinvoiceforportaldemo0000000000000000000000000000000000', amount: 210, expiry: 600 })); }
  if (u === '/__speedtest') { usage += 40 * 1048576; return send('{"ok":true}'); }
  send('{}', 404);
}).listen(2121, '0.0.0.0', () => console.log('mock on :2121'));
