// Cursor-cycle regression test.
//
// The kernel software cursor saves the 16x16 background, draws the
// glyph, and restores the background on every move.  That cycle must
// be pixel-exact: any error accumulates as visible residue in the
// regions the cursor crosses.  Cycle the cursor in a small square
// over bare desktop, park it identically, and compare frames.
import { chromium } from 'playwright';
const url = 'http://127.0.0.1:7072/examples/basic-v64-aspera.html?test';
const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1100, height: 1000 } });
page.on('pageerror', e => console.log('PAGEERROR', String(e).slice(0, 200)));
const frame = () => page.evaluate(() => {
  const c = document.querySelector('#vga canvas');
  return Array.from(c.getContext('2d').getImageData(0, 0, c.width, c.height).data);
});
const stats = () => page.evaluate(() => {
  const c = document.querySelector('#vga canvas');
  const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data;
  let white = 0, dark = 0;
  for (let i = 0; i < d.length; i += 4) {
    if (d[i] > 200 && d[i + 1] > 200 && d[i + 2] > 200) white++;
    if (d[i] < 60 && d[i + 1] < 60 && d[i + 2] < 60) dark++;
  }
  return { white, dark };
});
const gmove = (dx, dy) => page.evaluate(([dx, dy]) => {
  document.querySelector('#vga canvas').dispatchEvent(new MouseEvent('mousemove', { movementX: dx, movementY: dy, bubbles: true }));
}, [dx, dy]);
const gotoGuest = async (gx, gy) => {
  for (let i = 0; i < 20; i++) await gmove(-127, -127);
  await page.waitForTimeout(250);
  let x = 0, y = 0;
  while (x < gx) { const d = Math.min(127, gx - x); await gmove(d, 0); x += d; }
  while (y < gy) { const d = Math.min(127, gy - y); await gmove(0, d); y += d; }
  await page.waitForTimeout(250);
};
await page.goto(url);
await page.waitForFunction(() => document.querySelector('#serial').textContent.includes('softscreen: Bochs LFB'), null, { timeout: 180000 });
let s; const t0 = Date.now();
while (Date.now() - t0 < 120000) { s = await stats(); if (s.white > 60000 && s.dark > 30) break; await page.waitForTimeout(1000); }
if (!(s.white > 60000 && s.dark > 30)) throw Error(`Rio window not drawn: ${JSON.stringify(s)}`);
console.log('boot: Rio window up', JSON.stringify(s));
await gotoGuest(10, 10);
await page.waitForTimeout(800);
const before = await frame();
// Ten laps of a small square over bare desktop.
await gotoGuest(100, 500);
for (let lap = 0; lap < 10; lap++) {
  for (let i = 0; i < 4; i++) { await gmove(15, 0); await page.waitForTimeout(60); }
  for (let i = 0; i < 4; i++) { await gmove(0, 15); await page.waitForTimeout(60); }
  for (let i = 0; i < 4; i++) { await gmove(-15, 0); await page.waitForTimeout(60); }
  for (let i = 0; i < 4; i++) { await gmove(0, -15); await page.waitForTimeout(60); }
}
await gotoGuest(10, 10);
await page.waitForTimeout(800);
const after = await frame();
let diff = 0, noise = 0;
for (let p = 0; p < before.length / 4; p++) {
  const i = p * 4;
  const d = Math.max(Math.abs(before[i] - after[i]), Math.abs(before[i + 1] - after[i + 1]), Math.abs(before[i + 2] - after[i + 2]));
  if (d > 16) diff++; else if (d > 0) noise++;
}
console.log(`cursor-cycle: artifact pixels=${diff} sub-visible noise=${noise}`);
if (diff > 0) throw Error(`cursor cycling left residue: ${diff} pixels`);
console.log('RIO-CURSOR-CYCLE PASS');
await browser.close();
