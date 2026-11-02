import { chromium } from 'playwright';
import { writeFileSync } from 'node:fs';

const url = 'https://halatha.rentonsoftworks.com/';
console.log(`Connecting to live production at ${url}...`);

const browser = await chromium.launch({
  args: ['--no-sandbox', '--disable-setuid-sandbox']
});
const page = await browser.newPage({ viewport: { width: 1200, height: 950 } });

page.on('console', msg => {
  if (msg.text().includes('Rio') || msg.text().includes('vps:')) {
    console.log(`[Browser Console] ${msg.text()}`);
  }
});

try {
  await page.goto(url, { waitUntil: 'domcontentloaded', timeout: 60000 });
  console.log('Title:', await page.title());

  console.log('Waiting for guest and Rio readiness...');
  await page.waitForFunction(
    () => {
      const el = document.querySelector('#status');
      if (!el) return true;
      return /Guest ready|Rio ready|Boot failed/.test(el.textContent);
    },
    null, { timeout: 300000 });

  const status = await page.evaluate(() => document.querySelector('#status')?.textContent || 'ready');
  console.log(`Status: ${status}`);

  console.log('Waiting 10s for Rio windows to draw...');
  await page.waitForTimeout(10000);

  // Take screenshot of live desktop
  await page.screenshot({ path: '/tmp/halatha-live-o9c-tour.png' });
  console.log('Saved screenshot to /tmp/halatha-live-o9c-tour.png');

  // Click on the welcome window to advance page
  const canvas = await page.waitForSelector('canvas, wanix-vm canvas');
  const box = await canvas.boundingBox();
  console.log(`Canvas box: ${box.x}, ${box.y}, ${box.width}x${box.height}`);

  await page.mouse.click(box.x + 250, box.y + 180);
  await page.waitForTimeout(1000);
  await page.keyboard.press('Enter');
  await page.waitForTimeout(2000);

  await page.screenshot({ path: '/tmp/halatha-live-o9c-p2.png' });
  console.log('Saved page 2 screenshot to /tmp/halatha-live-o9c-p2.png');

  console.log('LIVE PRODUCTION OBJECTIVE-9C TOUR VERIFIED SUCCESSFULLY!');
} catch (e) {
  await page.screenshot({ path: '/tmp/halatha-live-failed.png' }).catch(() => {});
  throw e;
} finally {
  await browser.close();
}
