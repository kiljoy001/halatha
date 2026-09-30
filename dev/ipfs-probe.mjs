// Exercise the browser-side IPFS bridge without needing a booted guest:
// drive /n/host/ipfs/ctl exactly as ipfs.rc does and watch status + output.
import { chromium } from "playwright";

const CID = process.argv[2]
  // "hello worlds" -- the classic tiny IPFS test object
  || "bafkreidon73zrzjbmvsi4ug535hjmfszgapahvmnrpuh3upyunrvvjptqm";
const url = "https://halatha.rentonsoftworks.com/?host9p=0";

const browser = await chromium.launch({ args: ["--no-sandbox"] });
const page = await (await browser.newContext()).newPage();
const logs = [];
page.on("console", (m) => { const t = m.text(); if (/ipfs/i.test(t)) { logs.push(t); console.log("  [page]", t); } });

await page.goto(url, { waitUntil: "domcontentloaded", timeout: 60000 });
await page.waitForFunction(() => !!window.hostRoot, { timeout: 60000 });
await page.waitForTimeout(4000);

console.log(`\n=== writing "get ${CID}" to ipfs/ctl ===`);
await page.evaluate(async (cid) => {
  const enc = new TextEncoder();
  await window.hostRoot.writeFile("ipfs/ctl", enc.encode(`get ${cid}\n`));
}, CID);

// poll status like the guest would
for (let i = 0; i < 30; i++) {
  await page.waitForTimeout(1000);
  const st = await page.evaluate(async () => {
    try { return new TextDecoder().decode(await window.hostRoot.readFile("ipfs/status")).trim(); }
    catch (e) { return `ERR ${e.message}`; }
  });
  console.log(`  ${i + 1}s status: ${st}`);
  if (/^done|^error/.test(st)) break;
}

const out = await page.evaluate(async (cid) => {
  const r = window.hostRoot, dec = new TextDecoder(), res = {};
  for (const f of ["ipfs/status", "ipfs/cid", "ipfs/data"]) {
    try { const b = await r.readFile(f); res[f] = dec.decode(b).slice(0, 200); }
    catch (e) { res[f] = `ERR ${e.message}`; }
  }
  try { res["ipfs/out"] = await r.readDir("ipfs/out"); } catch (e) { res["ipfs/out"] = `ERR ${e.message}`; }
  return res;
}, CID);
console.log("\n=== result ===");
console.log(JSON.stringify(out, null, 2));
await browser.close();
