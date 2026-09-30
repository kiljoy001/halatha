// Does keyboard input reach the v86 guest at all in headless Chromium?
// Types into the canvas and reports whether v86's keyboard bus saw it.
import { chromium } from "playwright";

const url = process.argv[2] || "https://halatha.rentonsoftworks.com/";
const wait = Number(process.argv[3] || 60);

const browser = await chromium.launch({ args: ["--no-sandbox"] });
const page = await (await browser.newContext()).newPage();
await page.goto(url, { waitUntil: "domcontentloaded", timeout: 60000 });
await page.waitForTimeout(wait * 1000);

const pre = await page.evaluate(() => ({
  hasEmu: !!window.emulator,
  canvases: document.querySelectorAll("canvas").length,
  active: document.activeElement ? document.activeElement.tagName + "#" + document.activeElement.id : "none",
}));
console.log("before:", JSON.stringify(pre));

// Count keyboard events v86 actually receives.
await page.evaluate(() => {
  window.__keys = 0;
  const c = document.querySelector("#screen_container") || document.body;
  c.addEventListener("keydown", () => { window.__keys++; }, true);
  document.addEventListener("keydown", () => { window.__keys++; }, true);
});

const canvas = await page.$("#screen_container canvas, canvas");
if (canvas) { await canvas.click(); console.log("clicked canvas"); }
else console.log("NO CANVAS FOUND");
await page.waitForTimeout(500);

await page.keyboard.type("echo hi", { delay: 80 });
await page.waitForTimeout(1500);

const post = await page.evaluate(() => ({
  keys: window.__keys,
  active: document.activeElement ? document.activeElement.tagName + "#" + document.activeElement.id : "none",
  // v86 exposes a keyboard adapter when wired
  kbd: !!(window.emulator && window.emulator.keyboard_adapter),
}));
console.log("after: ", JSON.stringify(post));
console.log(post.keys > 0 ? "=> DOM saw the keys" : "=> DOM never saw the keys");
await browser.close();
