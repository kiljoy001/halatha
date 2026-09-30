// Boot the guest, wait for rio, then type a command into the rio terminal
// and screenshot the result.
//   node dev/guest-exec.mjs "<command>" [bootSeconds]
import { chromium } from "playwright";

const cmd = process.argv[2] || "ls /n/host/boot";
const boot = Number(process.argv[3] || 100);
const SP = "/tmp/halatha-keyreg";

const browser = await chromium.launch({ args: ["--no-sandbox"] });
const page = await (await browser.newContext({viewport:{width:1280,height:800}})).newPage();
await page.goto("https://halatha.rentonsoftworks.com/?debug9p=1", { waitUntil: "domcontentloaded", timeout: 60000 });

let frames = 0;
page.on("console", (m) => { if (/9P -> host/.test(m.text())) frames++; });

console.log(`booting ${boot}s...`);
await page.waitForTimeout(boot * 1000);
console.log(`boot 9P frames: ${frames}`);

// Click inside the rio terminal window (the right-hand window in the shots)
await page.mouse.click(240, 60);
await page.waitForTimeout(800);

const before = frames;
await page.keyboard.type(cmd, { delay: 70 });
await page.keyboard.press("Enter");
await page.waitForTimeout(8000);
console.log(`9P frames caused by the command: ${frames - before}`);

await page.screenshot({ path: `${SP}/exec.png` });
console.log(`screenshot -> ${SP}/exec.png`);
await browser.close();
