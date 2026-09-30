// Screenshot the guest at intervals so we can see what state it reaches.
import { chromium } from "playwright";
const url = process.argv[2] || "https://halatha.rentonsoftworks.com/";
const out = process.argv[3] || "/tmp/shot";
const marks = (process.argv[4] || "30,60,90,120").split(",").map(Number);

const browser = await chromium.launch({ args: ["--no-sandbox"] });
const page = await (await browser.newContext({viewport:{width:1280,height:800}})).newPage();
await page.goto(url, { waitUntil: "domcontentloaded", timeout: 60000 });

let prev = 0;
for (const m of marks) {
  await page.waitForTimeout((m - prev) * 1000);
  prev = m;
  await page.screenshot({ path: `${out}-${m}s.png` });
  const st = await page.evaluate(() => {
    const s = document.querySelector("#status");
    return s ? s.textContent.trim() : "(no #status)";
  });
  console.log(`${m}s  status="${st}"  -> ${out}-${m}s.png`);
}
await browser.close();
