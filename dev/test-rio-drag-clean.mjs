// Drag-artifact regression test.
//
// A software cursor composited into the framebuffer must be hidden
// before any draw that touches screen memory (kernel hwdraw hook ->
// swcursoravoid), redrawn only by the mouse process under drawlock,
// and composited exactly.  Otherwise window drags overdraw the
// cursor and its saved-background restore paints stale 16x16
// patches over freshly drawn content, leaving artifacts along the
// drag path (and baked into Rio's Refbackup outline images).
//
// Proof: park the cursor, snapshot the frame, drag a window out and
// back to its exact origin, park the cursor identically, snapshot
// again.  The two frames must match (tiny tolerance for noise).
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
const busButtons = (l, m, r) => page.evaluate(([l, m, r]) => window.emulator.bus.send('mouse-click', [l, m, r]), [l, m, r]);

await page.goto(url);
await page.waitForFunction(() => document.querySelector('#serial').textContent.includes('softscreen: Bochs LFB'), null, { timeout: 180000 });
let s;
const t0 = Date.now();
while (Date.now() - t0 < 120000) {
  s = await stats();
  if (s.white > 60000 && s.dark > 30) break;
  await page.waitForTimeout(1000);
}
if (!(s.white > 60000 && s.dark > 30)) throw Error(`Rio window not drawn: ${JSON.stringify(s)}`);
console.log('boot: Rio window up', JSON.stringify(s));

// Baseline frame, cursor parked top-left.
await gotoGuest(10, 10);
await page.waitForTimeout(800);
const before = await frame();

// Drag the initial rc window (520,40)-(980,320) by its top border:
// out (+90,+60 in steps) and back by the exact inverse.
await gotoGuest(700, 42);
await busButtons(false, false, true);
await page.waitForTimeout(300);
for (let i = 0; i < 6; i++) { await gmove(15, 10); await page.waitForTimeout(120); }
for (let i = 0; i < 6; i++) { await gmove(-15, -10); await page.waitForTimeout(120); }
await busButtons(false, false, false);
await page.waitForTimeout(1500);

// Identical parking, then compare.
await gotoGuest(10, 10);
await page.waitForTimeout(800);
const after = await frame();

if (before.length !== after.length) throw Error('frame size changed');
let diff = 0, noise = 0, maxDelta = 0, minx = 1e9, miny = 1e9, maxx = -1, maxy = -1;
for (let p = 0; p < before.length / 4; p++) {
  const i = p * 4;
  const d = Math.max(Math.abs(before[i] - after[i]), Math.abs(before[i + 1] - after[i + 1]), Math.abs(before[i + 2] - after[i + 2]));
  if (d > 16) {
    diff++; if (d > maxDelta) maxDelta = d;
    const x = p % 1024, y = Math.floor(p / 1024);
    if (x < minx) minx = x; if (x > maxx) maxx = x; if (y < miny) miny = y; if (y > maxy) maxy = y;
  } else if (d > 0) noise++;
}
console.log(`drag-clean: artifact pixels=${diff} maxDelta=${maxDelta} bbox=(${minx},${miny})-(${maxx},${maxy}) sub-visible noise=${noise}`);
// Only visible deltas count: cursor droppings / outline residue are
// full-strength (delta up to 255) 16x16-ish patches.  A window fully
// redrawn after a move may differ in sub-visible channel noise.
if (diff > 400) throw Error(`window drag left artifacts: ${diff} differing pixels`);
console.log('RIO-DRAG-CLEAN PASS');
await browser.close();

