// Drive the on-demand libtab install the way `pkg install` does from the
// guest: write "install <name>" to bin/ctl and watch bin/status.
// Proves demand-loading works for a binary that is NOT preloaded.
import { chromium } from "playwright";

const target = process.argv[2] || "yggdrasil";
const browser = await chromium.launch({ args: ["--no-sandbox"] });
const page = await (await browser.newContext()).newPage();
page.on("console", (m) => { const t = m.text(); if (/libtab|install|error/i.test(t)) console.log("  [page]", t.slice(0, 140)); });

await page.goto("https://halatha.rentonsoftworks.com/?host9p=0", { waitUntil: "domcontentloaded", timeout: 60000 });
await page.waitForFunction(() => !!window.hostRoot, { timeout: 60000 });
await page.waitForTimeout(6000);

const before = await page.evaluate(async () => (await window.hostRoot.readDir("bin")).length);
console.log(`bin/ entries before: ${before}`);

console.log(`\n=== install ${target} ===`);
await page.evaluate(async (t) => {
  await window.hostRoot.writeFile("bin/ctl", new TextEncoder().encode(`install ${t}\n`));
}, target);

for (let i = 0; i < 40; i++) {
  await page.waitForTimeout(1000);
  const st = await page.evaluate(async () =>
    new TextDecoder().decode(await window.hostRoot.readFile("bin/status")).trim());
  if (i % 4 === 0 || /installed|error/.test(st)) console.log(`  ${i + 1}s: ${st}`);
  if (/installed|error/.test(st)) break;
}

const out = await page.evaluate(async (t) => {
  const r = window.hostRoot;
  const entries = await r.readDir("bin");
  let size = null;
  try { size = (await r.readFile(`bin/${t}`)).length; } catch (e) { size = `ERR ${e.message}`; }
  return { count: entries.length, present: entries.includes(t), size };
}, target);
console.log(`\nbin/ entries after: ${out.count}`);
console.log(`${target} present: ${out.present}, size: ${out.size}`);
await browser.close();
