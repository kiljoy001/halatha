// Smoke test for the Harvey-on-v64 boot path.
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
  if (text.includes("HALATHA: entering idle") || text.includes("panic:")) break;
  await page.waitForTimeout(500);
}
await browser.close();
console.log(text.split("\n").slice(-30).join("\n"));
for (const e of errors) console.log("page error:", e);
const ok = text.includes("Harvey") && text.includes("HALATHA: Harvey userland on v64") &&
  text.includes("HALATHA: root read");
console.log(`HARVEY-V64-BOOT ${ok ? "PASS" : "FAIL"} ${url}`);
process.exit(ok ? 0 : 1);
