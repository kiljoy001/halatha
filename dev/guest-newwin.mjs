// rio's default screen has stats/faces windows, not a shell. Open a NEW rio
// window (button-3 menu -> New, then drag a rectangle) and run a command in it.
import { chromium } from "playwright";
const cmd = process.argv[2] || "ls /n/host/bin";
const boot = Number(process.argv[3] || 110);
const SP = "/tmp/halatha-keyreg";

const browser = await chromium.launch({ args: ["--no-sandbox"] });
const page = await (await browser.newContext({viewport:{width:1280,height:800}})).newPage();
let frames = 0;
page.on("console", (m) => { if (/9P -> host/.test(m.text())) frames++; });
await page.goto("https://halatha.rentonsoftworks.com/?debug9p=1", { waitUntil:"domcontentloaded", timeout:60000 });
console.log(`booting ${boot}s...`);
await page.waitForTimeout(boot*1000);
console.log(`boot frames: ${frames}`);

// button-3 press on empty desktop -> rio menu; "New" is the first entry.
await page.mouse.move(600, 400);
await page.mouse.down({ button: "right" });
await page.waitForTimeout(400);
await page.screenshot({ path: `${SP}/menu.png` });
// keep held, move onto "New" (first item, just below the press point)
await page.mouse.move(608, 410);
await page.waitForTimeout(200);
await page.mouse.up({ button: "right" });
await page.waitForTimeout(600);

// now drag out the window rectangle with button-1
await page.mouse.move(420, 200);
await page.mouse.down({ button: "left" });
await page.mouse.move(1100, 650, { steps: 12 });
await page.mouse.up({ button: "left" });
await page.waitForTimeout(2500);
await page.screenshot({ path: `${SP}/newwin.png` });

await page.mouse.click(700, 400);
await page.waitForTimeout(500);
const before = frames;
await page.keyboard.type(cmd, { delay: 70 });
await page.keyboard.press("Enter");
await page.waitForTimeout(9000);
console.log(`9P frames from command: ${frames - before}`);
await page.screenshot({ path: `${SP}/newwin-after.png` });
console.log(`shots: ${SP}/menu.png ${SP}/newwin.png ${SP}/newwin-after.png`);
await browser.close();
