// Smoke test for the Harvey/Halatha shell on v64.
// node dev/test-harvey-v64-boot.mjs [url] [--timeout=seconds]
// Requires a static server for the repository root (default :7070).
import { chromium } from "playwright";

const args = process.argv.slice(2);
const url = args.find((a) => !a.startsWith("--")) ||
  "http://127.0.0.1:7070/examples/basic-v64-harvey.html";
const timeout = Number(args.find((a) => a.startsWith("--timeout="))?.slice(10) || 30) * 1000;
const browser = await chromium.launch({ args: ["--no-sandbox"] });
const page = await (await browser.newContext()).newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
await page.goto(url, { waitUntil: "domcontentloaded" });

const started = Date.now();
let text = "";
while (Date.now() - started < timeout) {
  text = await page.evaluate(() => document.getElementById("serial")?.textContent || "");
  if (text.includes("ASPERA-LIVE READY") || text.includes("panic:")) break;
  await page.waitForTimeout(500);
}
const canvas = await page.evaluate(() => {
  const c = document.getElementById("aspera");
  if (!c) return null;
  const d = c.getContext("2d").getImageData(0, 0, 1024, 768).data;
  let nonblack = 0;
  for (let i = 0; i < d.length; i += 4)
    if (d[i] || d[i + 1] || d[i + 2]) nonblack++;
  return { nonblack };
});
await browser.close();
console.log(text.split("\n").slice(-30).join("\n"));
for (const e of errors) console.log("page error:", e);
const ok = text.includes("ASPERA-LIVE READY") && canvas && canvas.nonblack > 100000;
console.log(`HARVEY-V64-BOOT ${ok ? "PASS" : "FAIL"} ${url} canvas=${JSON.stringify(canvas)}`);
process.exit(ok ? 0 : 1);
