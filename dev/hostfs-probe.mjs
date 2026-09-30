// The page imports the server's 9P namespace at hostfs (see the
// <wanix-bind type="import" dst="hostfs"> in index.html). With --ipfs-api
// set, that namespace now contains ipfs/. Check it end to end.
import { chromium } from "playwright";

const url = process.argv[2] || "https://halatha.rentonsoftworks.com/?host9p=0";
const browser = await chromium.launch({ args: ["--no-sandbox"] });
const page = await (await browser.newContext()).newPage();
page.on("console", (m) => { const t = m.text(); if (/hostfs|import|ipfs/i.test(t)) console.log("  [page]", t.slice(0, 160)); });

await page.goto(url, { waitUntil: "domcontentloaded", timeout: 60000 });
await page.waitForFunction(() => !!window.hostRoot, { timeout: 60000 });
await page.waitForTimeout(6000);

const out = await page.evaluate(async () => {
  const r = window.hostRoot, dec = new TextDecoder(), res = {};
  try { res.root = await r.readDir("hostfs"); } catch (e) { res.root = `ERR ${e.message}`; }
  try { res.ipfs = await r.readDir("hostfs/ipfs"); } catch (e) { res.ipfs = `ERR ${e.message}`; }
  const cid = "QmYwAPJzv5CZsnA625s3Xf2nemtYgPpHdWEz79ojWnPbdG";
  try { res.cidDir = await r.readDir(`hostfs/ipfs/${cid}`); } catch (e) { res.cidDir = `ERR ${e.message}`; }
  try {
    const b = await r.readFile(`hostfs/ipfs/${cid}/readme`);
    res.readme = dec.decode(b).slice(0, 60);
  } catch (e) { res.readme = `ERR ${e.message}`; }
  return res;
});

console.log("\n=== hostfs namespace ===");
console.log(JSON.stringify(out, null, 2));
await browser.close();
