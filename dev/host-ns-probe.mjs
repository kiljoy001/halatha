// Fast host-side check: does the browser namespace have everything the guest
// needs? No guest boot required, so this runs in seconds instead of minutes.
//
//   node dev/host-ns-probe.mjs [url]
import { chromium } from "playwright";

const url = process.argv[2] || "https://halatha.rentonsoftworks.com/?host9p=0";
const browser = await chromium.launch({ args: ["--no-sandbox"] });
const page = await (await browser.newContext()).newPage();

const warns = [];
page.on("console", (m) => {
  const t = m.text();
  if (/seed |libtab|error|fail|REJECT/i.test(t)) warns.push(t);
});

await page.goto(url, { waitUntil: "domcontentloaded", timeout: 60000 });
await page.waitForFunction(() => !!window.hostRoot, { timeout: 60000 }).catch(() => {});
await page.waitForTimeout(8000);

const report = await page.evaluate(async () => {
  const r = window.hostRoot;
  if (!r) return { error: "hostRoot never appeared" };
  const dec = new TextDecoder();
  const out = { files: {}, dirs: {} };
  for (const d of ["boot", "bin", "clip"]) {
    try { out.dirs[d] = await r.readDir(d); }
    catch (e) { out.dirs[d] = `ERR ${e.message}`; }
  }
  for (const f of ["bin/STATUS", "clip/text", "READY", "boot/halatha.local", "boot/pkg.rc", "boot/clip.rc"]) {
    try {
      const b = await r.readFile(f);
      const s = dec.decode(b);
      out.files[f] = s.length > 70 ? `${s.slice(0, 70)}... (${s.length}B)` : s;
    } catch (e) { out.files[f] = `ERR ${e.message}`; }
  }
  return out;
});

console.log("=== host namespace ===");
console.log(JSON.stringify(report, null, 2));
if (warns.length) {
  console.log("\n=== console (seed/libtab/errors) ===");
  console.log([...new Set(warns)].slice(0, 20).join("\n"));
}
await browser.close();
