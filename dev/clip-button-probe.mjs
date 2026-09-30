// Does [Paste to Plan 9] actually write clip/text? That path does NOT touch
// snarf at all -- it is browser clipboard -> host namespace file. If this
// works, the failure is on the guest side; if not, it is the button.
import { chromium } from "playwright";

const url = process.argv[2] || "https://halatha.rentonsoftworks.com/?host9p=0";
const browser = await chromium.launch({ args: ["--no-sandbox"] });
const ctx = await browser.newContext({
  permissions: ["clipboard-read", "clipboard-write"],
});
const page = await ctx.newPage();
const logs = [];
page.on("console", (m) => logs.push(m.text()));
page.on("pageerror", (e) => logs.push("PAGEERROR: " + e.message));

await page.goto(url, { waitUntil: "domcontentloaded", timeout: 60000 });
await page.waitForFunction(() => !!window.hostRoot, { timeout: 60000 });
await page.waitForTimeout(4000);

const before = await page.evaluate(async () =>
  new TextDecoder().decode(await window.hostRoot.readFile("clip/text")));
console.log("clip/text before:", JSON.stringify(before));

const marker = "PASTE_TEST_" + Date.now();
await page.evaluate(async (t) => navigator.clipboard.writeText(t), marker);
console.log("host clipboard set to:", marker);

const btn = await page.$("#btn-paste-guest");
console.log("paste button found:", !!btn);
if (btn) { await btn.click(); await page.waitForTimeout(2000); }

const after = await page.evaluate(async () =>
  new TextDecoder().decode(await window.hostRoot.readFile("clip/text")));
console.log("clip/text after: ", JSON.stringify(after));

const toast = await page.evaluate(() => {
  const t = document.getElementById("clip-toast");
  return t ? t.textContent.trim() : "(no toast element)";
});
console.log("toast:", JSON.stringify(toast));
console.log(after.includes(marker) ? "\n=> BUTTON WORKS" : "\n=> BUTTON DID NOT WRITE clip/text");

const rel = logs.filter((l) => /clip|paste|error|denied/i.test(l));
if (rel.length) { console.log("\n=== console ==="); console.log(rel.slice(0, 15).join("\n")); }
await browser.close();
