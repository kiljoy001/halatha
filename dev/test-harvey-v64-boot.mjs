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
  if (text.includes("% ") || text.includes("panic:")) break;
  await page.waitForTimeout(500);
}

let shellOut = text;
if (text.includes("% ")) {
  await page.fill("#cmd", "echo HALATHA-SHELL");
  await page.press("#cmd", "Enter");
  await page.waitForTimeout(1500);
  shellOut = await page.evaluate(() => document.getElementById("serial")?.textContent || "");
}
await browser.close();

console.log(shellOut.split("\n").slice(-30).join("\n"));
for (const e of errors) console.log("page error:", e);
const ok = shellOut.includes("Halatha / Harvey on v64") &&
  shellOut.includes("% ") && shellOut.includes("HALATHA-SHELL");
console.log(`HARVEY-V64-BOOT ${ok ? "PASS" : "FAIL"} ${url}`);
process.exit(ok ? 0 : 1);
