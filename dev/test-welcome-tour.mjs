import { chromium } from 'playwright';
import { writeFileSync } from 'node:fs';

const url = process.argv[2] || 'http://127.0.0.1:7072/examples/basic-v64-aspera.html?test';
console.log(`Testing welcome tour at ${url}...`);

const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1200, height: 950 } });
const errors = [];
page.on('pageerror', error => errors.push(error.message));
page.on('console', msg => {
  if (msg.type() === 'error') console.log('[Console Error]', msg.text());
});

try {
  await page.goto(url);
  console.log('Waiting for Rio readiness...');
  await page.waitForFunction(
    () => /Rio ready|Boot failed/.test(document.querySelector('#status')?.textContent || ''),
    null, { timeout: 120000 });

  const status = await page.locator('#status').textContent();
  console.log(`Status: ${status}`);
  if (status !== 'Rio ready') {
    const serial = await page.locator('#serial').textContent();
    console.error('Boot log:\n', serial.slice(-3000));
    throw new Error(`Rio boot failed: ${status}`);
  }

  // Allow Rio to execute rio-start.rc (welcome + rc + snarfbridge)
  console.log('Rio ready! Waiting 8 seconds for welcome window to open and paint...');
  await page.waitForTimeout(8000);

  // Take screenshot of desktop with the Objective-9c welcome window
  await page.screenshot({ path: '/tmp/halatha-welcome-tour.png' });
  console.log('Saved screenshot to /tmp/halatha-welcome-tour.png');

  // Let's inspect serial output
  const serialText = await page.locator('#serial').textContent();
  console.log('Last serial output lines:');
  console.log(serialText.slice(-1000));

  // Let's test interaction: click into the welcome window (around x=250, y=150)
  // or press Enter to advance to page 2 of the tour!
  console.log('Clicking inside the Welcome window to advance page...');
  const canvas = await page.locator('#vga canvas');
  const box = await canvas.boundingBox();
  // Welcome window is at (40, 40) to (500, 320). Click around (250, 180)
  await page.mouse.click(box.x + 250, box.y + 180);
  await page.waitForTimeout(1000);
  await page.keyboard.press('Enter');
  await page.waitForTimeout(2000);

  await page.screenshot({ path: '/tmp/halatha-welcome-tour-p2.png' });
  console.log('Saved page 2 screenshot to /tmp/halatha-welcome-tour-p2.png');

  if (errors.length) {
    console.error('Page errors encountered:', errors);
    throw new Error('Page errors during test');
  }

  console.log('WELCOME TOUR TEST PASSED!');
} catch (e) {
  await page.screenshot({ path: '/tmp/halatha-welcome-failed.png' }).catch(() => {});
  throw e;
} finally {
  await browser.close();
}
