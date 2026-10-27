import { chromium } from 'playwright';
import { writeFileSync } from 'node:fs';

const url = process.argv[2] || 'http://127.0.0.1:7072/examples/basic-v64-aspera.html?test';
const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1100, height: 1000 } });
const errors = [];
page.on('pageerror', e => errors.push(e.message));
const pause = ms => page.waitForTimeout(ms);
const grab = async name => {
  const data = await page.locator('#vga canvas').evaluate(c => c.toDataURL());
  writeFileSync(`/tmp/rio-titlebars-${name}.png`, Buffer.from(data.split(',')[1], 'base64'));
};
const text = async value => {
  for (const ch of value) {
    if (ch === '>') await page.keyboard.press('Shift+.');
    else await page.keyboard.press(ch);
    await pause(45);
  }
};
const command = async value => {
  await page.keyboard.press('Control+u');
  await text(value);
  await page.keyboard.press('Enter');
  await pause(1800);
};
const hostText = path => page.evaluate(path => {
  const data = window.aspera.fs.lookup(path)?.data;
  return data ? new TextDecoder().decode(data) : null;
}, path);
const waitFile = (path, content) => page.waitForFunction(([path, content]) => {
  const data = window.aspera.fs.lookup(path)?.data;
  return data && new TextDecoder().decode(data) === content;
}, [path, content], { timeout: 30000 });
const move = (dx, dy) => page.evaluate(([dx, dy]) => {
  document.querySelector('#vga canvas').dispatchEvent(new MouseEvent('mousemove', { movementX: dx, movementY: dy, bubbles: true }));
}, [dx, dy]);
const point = async (x, y) => {
  for (let i = 0; i < 20; i++) await move(-127, -127);
  await pause(300);
  while (x > 0) { const d = Math.min(x, 100); await move(d, 0); x -= d; await pause(30); }
  while (y > 0) { const d = Math.min(y, 100); await move(0, d); y -= d; await pause(30); }
  await pause(300);
};
const buttons = left => page.evaluate(left => window.emulator.bus.send('mouse-click', [left, false, false]), left);
const windowLeft = () => page.evaluate(() => {
  const c = document.querySelector('#vga canvas');
  const d = c.getContext('2d').getImageData(0, 180, 1024, 20).data;
  for (let x = 0; x < 1024; x++) {
    let white = 0;
    for (let y = 0; y < 20; y++) { const p = (y * 1024 + x) * 4; if (d[p] > 200 && d[p + 1] > 200 && d[p + 2] > 200) white++; }
    if (white > 15) return x;
  }
  return -1;
});

try {
  await page.goto(url);
  await page.waitForFunction(() => document.querySelector('#serial').textContent.includes('halatha: starting Rio'), null, { timeout: 120000 });
  await page.waitForFunction(() => {
    const c = document.querySelector('#vga canvas');
    if (c.width !== 1024) return false;
    const d = c.getContext('2d').getImageData(530, 100, 430, 200).data;
    let white = 0;
    for (let i = 0; i < d.length; i += 4) if (d[i] > 200 && d[i + 1] > 200 && d[i + 2] > 200) white++;
    const bar = c.getContext('2d').getImageData(530, 48, 430, 12).data;
    let tint = 0;
    for (let i = 0; i < bar.length; i += 4) if (bar[i + 2] > bar[i] + 4) tint++;
    return white > 60000 && tint > 1000;
  }, null, { timeout: 120000 });
  await pause(1200);
  await grab('boot');
  console.log('Rio window booted');
  await command('echo titlebar-shell > /n/host/clip/seen');
  await waitFile('clip/seen', 'titlebar-shell\n');
  console.log('Shell input with title bars: PASS');

  const beforeMove = await windowLeft();
  await point(700, 54);
  await buttons(true);
  await pause(300);
  for (let i = 0; i < 4; i++) { await move(-15, 15); await pause(150); }
  await buttons(false);
  await pause(1200);
  const afterMove = await windowLeft();
  await grab('dragged');
  if (beforeMove - afterMove < 30) throw Error(`Title drag did not move the window: ${beforeMove} -> ${afterMove}`);
  await command('echo dragged > /n/host/clip/seen');
  await waitFile('clip/seen', 'dragged\n');
  console.log('Title-bar drag and shell input after move: PASS');
  await command("echo 'resize -r 460 100 960 420' > /dev/wctl");
  await command('echo resized > /n/host/clip/seen');
  await waitFile('clip/seen', 'resized\n');
  const resized = await page.locator('#vga canvas').evaluate(c => Array.from(c.getContext('2d').getImageData(940, 400, 1, 1).data));
  if (resized[0] < 200 || resized[1] < 200 || resized[2] < 200) throw Error('Window content did not expand on resize');
  console.log('Title-bar window resize: PASS');

  await page.evaluate(() => window.aspera.fs.addFile('clip/title-source', 'original\n', { writable: true, mode: 0o600 }));
  await page.keyboard.press('Control+o');
  await pause(500);
  await page.keyboard.press('Control+a');
  await page.keyboard.press('Control+k');
  await text('/n/host/clip/title-source');
  await grab('path');
  await page.keyboard.press('Enter');
  await pause(1000);
  await grab('opened');
  await page.keyboard.press('Control+End');
  await text('edited');
  await page.keyboard.press('Enter');
  await pause(500);
  await grab('edited');
  await page.keyboard.press('Control+s');
  await waitFile('clip/title-source', 'original\nedited\n');
  console.log('Title path open, edit and Ctrl+S file save: PASS');
  // Cancelling a title edit must preserve the file and its editing mode.
  await page.keyboard.press('Control+o');
  await page.keyboard.press('Control+a');
  await page.keyboard.press('Control+k');
  await text('/cancel-this-path');
  await page.keyboard.press('Escape');
  await page.keyboard.press('Control+b');
  await text('cancelled');
  await page.keyboard.press('Enter');
  await page.keyboard.press('Control+s');
  await waitFile('clip/title-source', 'original\nedited\ncancelled\n');
  console.log('Escape cancels title editing and preserves file mode: PASS');

  // A failed save must not append an error/path to the document or clear dirty state.
  await page.evaluate(() => { window.aspera.fs.lookup('clip/title-source').writable = false; });
  await text('pending');
  await page.keyboard.press('Enter');
  await page.keyboard.press('Control+s');
  await pause(1000);
  if (await hostText('clip/title-source') !== 'original\nedited\ncancelled\n') throw Error('Failed save changed the host file');
  await page.evaluate(() => { window.aspera.fs.lookup('clip/title-source').writable = true; });
  await page.keyboard.press('Control+s');
  await waitFile('clip/title-source', 'original\nedited\ncancelled\npending\n');
  console.log('Failed save preserves edits for retry: PASS');
  await pause(500);
  await point(600, 114);
  await buttons(true); await pause(70); await buttons(false); await pause(100);
  await buttons(true); await pause(70); await buttons(false); await pause(500);
  await grab('doubleclick');
  await text('discard-title-edit');
  await page.keyboard.press('Escape');
  await page.keyboard.press('Control+s');
  await pause(700);
  await waitFile('clip/title-source', 'original\nedited\ncancelled\npending\n');
  console.log('Double-click edits the title without changing the document: PASS');
  await grab('saved');
  const serial = await page.locator('#serial').textContent();
  if (/read fault fail|POOLABORT|panic:/.test(serial)) throw Error('Guest fault in serial output');
  if (errors.length) throw Error(errors.join('\n'));
  console.log('RIO-TITLEBARS PASS');
} catch (e) {
  await grab('failed').catch(() => {});
  console.log('serial:', await page.locator('#serial').textContent());
  console.log('saved file:', await hostText('clip/title-source').catch(() => null));
  throw e;
} finally {
  await browser.close();
}
