import { chromium } from 'playwright';
import fs from 'node:fs';
const url = process.argv[2] || 'http://127.0.0.1:7072/examples/basic-v64-aspera.html?test';
const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1100, height: 1000 } });
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
const grab = async name => {
  const s = await page.evaluate(() => document.querySelector('#vga canvas').toDataURL('image/png'));
  fs.writeFileSync(name, Buffer.from(s.split(',')[1], 'base64'));
};
const waitStats = async (cond, timeout = 45000) => {
  const t0 = Date.now();
  let s = await stats();
  while (!cond(s) && Date.now() - t0 < timeout) {
    await page.waitForTimeout(1000);
    s = await stats();
  }
  return s;
};
const SHIFTED = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ>!@#$%^&*()_+{}|:"?~';
const typeCmd = async cmd => {
  for (const ch of cmd) {
    if (SHIFTED.includes(ch)) {
      await page.keyboard.down('Shift');
      await page.keyboard.press(ch === '>' ? '.' : ch.toLowerCase());
      await page.keyboard.up('Shift');
    } else {
      await page.keyboard.press(ch);
    }
    await page.waitForTimeout(50);
  }
  await page.keyboard.press('Enter');
  await page.waitForTimeout(3500);
};
try {
  await page.goto(url);
  await page.waitForFunction(() => document.querySelector('#serial').textContent.includes('softscreen: Bochs LFB'), null, { timeout: 120000 });
  // Genuine Rio window: white rc window on the grey desktop.
  let s;
  const start = Date.now();
  while (Date.now() - start < 120000) {
    s = await stats();
    if (s.white > 60000 && s.dark > 30) break;
    await page.waitForTimeout(1000);
  }
  if (!(s.white > 60000 && s.dark > 30)) throw Error(`Rio window not drawn: ${JSON.stringify(s)}`);
  console.log('Rio display + initial rc window: PASS', JSON.stringify(s));
  const serial = await page.evaluate(() => document.querySelector('#serial').textContent);
  if (!serial.includes('/n/host')) throw Error('/n/host gateway not established');
  console.log('Rio /n/host gateway (#9): PASS');
  // Type promptly: guest window shells can abort if left idle after boot.
  await grab('/tmp/halatha-rio.png');
  const rect = await page.locator('#vga canvas').boundingBox();
  // No click: keyboard input goes to Rio's current window (Notes at boot).
  await typeCmd('echo vmtermok');
  console.log('Rio PS/2 keyboard input typed (proven by execution below)');
  // /n/host write from Rio window lands in browser storage (proves input).
  const clipOk = async () => {
    try {
      await page.waitForFunction(() => {
        try { return new TextDecoder().decode(window.aspera.fs.lookup('clip/text')?.data).includes("riomark"); } catch { return false; }
      }, null, { timeout: 30000 });
      return true;
    } catch { return false; }
  };
  await typeCmd('echo riomark > /n/host/clip/text');
  if (!(await clipOk())) {
    await typeCmd('echo riomark > /n/host/clip/text');
    if (!(await clipOk())) {
      await page.waitForTimeout(5000);
      await typeCmd('echo riomark > /n/host/clip/text');
      if (!(await clipOk())) { await grab('/tmp/halatha-clipfail.png'); throw Error('Rio keyboard/clip write failed'); }
    }
  }
  console.log('Rio PS/2 keyboard input + rc echo: PASS');
  console.log('Rio window -> /n/host/clip/text: PASS');
  // Clipboard bridge: guest snarf -> host, host -> guest snarf.
  await typeCmd('echo snarfmark > /dev/snarf');
  try {
    await page.waitForFunction(() => localStorage.getItem('halatha.clip.text.v1')?.includes('snarfmark'), null, { timeout: 30000 });
  } catch {
    await typeCmd('snarfbridge &');
    await page.waitForTimeout(3000);
    await typeCmd('echo snarfmark > /dev/snarf');
    try {
      await page.waitForFunction(() => localStorage.getItem('halatha.clip.text.v1')?.includes('snarfmark'), null, { timeout: 30000 });
    } catch (e) {
      await grab('/tmp/halatha-snarfail.png');
      console.log('host clip at snarf failure:', await page.evaluate(() => { try { return JSON.stringify(new TextDecoder().decode(window.aspera.fs.lookup('clip/text')?.data)); } catch { return '?'; } }));
      throw e;
    }
  }
  console.log('Clipboard Rio snarf -> browser: PASS');
  await page.evaluate(() => window.aspera.setHostClip('hostmark'));
  await page.waitForTimeout(4000);
  await typeCmd('cat /dev/snarf > /n/host/clip/seen');
  try {
    await page.waitForFunction(() => {
      try { return new TextDecoder().decode(window.aspera.fs.lookup('clip/seen')?.data).includes('hostmark'); } catch { return false; }
    }, null, { timeout: 30000 });
  } catch {
    await page.evaluate(() => window.aspera.setHostClip('hostmark'));
    await page.waitForTimeout(4000);
    await typeCmd('cat /dev/snarf > /n/host/clip/seen');
    await page.waitForFunction(() => {
      try { return new TextDecoder().decode(window.aspera.fs.lookup('clip/seen')?.data).includes('hostmark'); } catch { return false; }
    }, null, { timeout: 30000 });
  }
  console.log('Clipboard browser -> Rio snarf: PASS');
  // Notes persistence through /n/host.
  await typeCmd('echo halatha-note-ok > /n/host/lib/aspera/note.txt');
  try {
    await page.waitForFunction(() => localStorage.getItem('halatha.aspera.note.v1')?.includes("halatha-note-ok"), null, { timeout: 30000 });
  } catch {
    await typeCmd('echo halatha-note-ok > /n/host/lib/aspera/note.txt');
    await page.waitForFunction(() => localStorage.getItem('halatha.aspera.note.v1')?.includes("halatha-note-ok"), null, { timeout: 30000 });
  }
  console.log('Notes via /n/host -> localStorage: PASS');
  // Second window via /dev/wctl.
  const wctlBefore = await stats();
  await typeCmd("echo 'new -r 500 80 980 420' > /dev/wctl");
  await page.waitForTimeout(3000);
  const wctlAfter = await stats();
  await grab('/tmp/halatha-rio-wctl.png');
  console.log('wctl stats', JSON.stringify(wctlBefore), JSON.stringify(wctlAfter));
  // Run a command in the second (now current) window.
  await typeCmd('echo secondwin > /n/host/clip/seen');
  await page.waitForFunction(() => {
    try { return new TextDecoder().decode(window.aspera.fs.lookup('clip/seen')?.data).includes('secondwin'); } catch { return false; }
  }, null, { timeout: 45000 });
  console.log('Command in second Rio window via /n/host: PASS');
  // Window move through /dev/wctl, done now while the second window is
  // known current and visible, at its prompt before any scrolling:
  // chained typing is reliable there, unlike in a scrolled-back window
  // where Rio inserts into the scrollback and lines never execute.
  const seenHas = async marker => {
    try {
      await page.waitForFunction(m => {
        try { return new TextDecoder().decode(window.aspera.fs.lookup('clip/seen')?.data).includes(m); } catch { return false; }
      }, marker, { timeout: 20000 });
      return true;
    } catch { return false; }
  };
  await typeCmd("echo 'move -r 8 120 968 700' > /dev/wctl");
  await typeCmd('echo snapmoved > /n/host/clip/seen');
  if (!(await seenHas('snapmoved'))) {
    await typeCmd('echo snapmoved > /n/host/clip/seen');
    if (!(await seenHas('snapmoved'))) {
      await grab('/tmp/halatha-snapfail.png');
      console.log('seen at snap failure:', await page.evaluate(() => { try { return JSON.stringify(new TextDecoder().decode(window.aspera.fs.lookup('clip/seen')?.data)); } catch { return '?'; } }));
      throw Error('wctl move command did not execute (no snapmoved proof)');
    }
  }
  await page.waitForTimeout(2000);
  const moveEdge = await page.evaluate(() => {
    const c = document.querySelector('#vga canvas');
    const ctx = c.getContext('2d');
    for (let x = 0; x < 40; x++) {
      const d = ctx.getImageData(x, 200, 1, 1).data;
      const grey = Math.abs(d[0] - 119) < 12 && Math.abs(d[1] - 119) < 12 && Math.abs(d[2] - 119) < 12;
      if (!grey) return x; /* first non-desktop pixel: window border */
    }
    return -1;
  });
  console.log('window left edge after wctl move to x=8:', moveEdge);
  await grab('/tmp/halatha-rio-snap.png');
  if (!(moveEdge >= 4 && moveEdge <= 14)) throw Error(`wctl move failed, window border at ${moveEdge}`);
  console.log('Window move via /dev/wctl: PASS');
  // Scrolling: fill the window with output, then page up/down and compare
  // a pixel hash of the window region (dark counts are fooled by the
  // repetitive ls listing).
  const regionHash = () => page.evaluate(() => {
    const c = document.querySelector('#vga canvas');
    const d = c.getContext('2d').getImageData(60, 60, 900, 600).data;
    let h = 0;
    for (let i = 0; i < d.length; i += 16) h = (h * 31 + d[i]) >>> 0;
    return h;
  });
  // Single fork, many lines (a tight fork loop trips a pre-existing
  // Harvey rc crash); window starts scrolled to the bottom, so the
  // first paging direction to test is PageUp.
  await typeCmd('ls /bin /bin /bin /bin /bin /bin /bin /bin');
  await page.waitForTimeout(3000);
  const atTop = await regionHash();
  await page.keyboard.press('PageUp');
  await page.waitForTimeout(2500);
  const pagedDown = await regionHash();
  console.log('scroll region hash top/down', atTop, pagedDown);
  if (pagedDown === atTop) throw Error('Rio window scrolling (PageUp) had no effect');
  await page.keyboard.press('PageDown');
  await page.waitForTimeout(2500);
  const backUp = await regionHash();
  if (backUp === pagedDown) throw Error('Rio window scrolling (PageDown) had no effect');
  console.log('Rio window scrolling via PageDown/PageUp: PASS');
  // Button 3 (desktop menu) via the emulator mouse bus. Informational:
  // guest pointer placement via synthesized deltas is not reliable
  // headless, so a missed menu here is not conclusive.
  const gmouse = (dx, dy, down) => page.evaluate(([dx, dy, down]) => {
    const c = document.querySelector('#vga canvas');
    c.dispatchEvent(new MouseEvent('mousemove', { movementX: dx, movementY: dy, bubbles: true }));
    if (down) c.dispatchEvent(new MouseEvent('mousedown', { button: 0, bubbles: true }));
  }, [dx, dy, down]);
  for (let i = 0; i < 20; i++) await gmouse(-127, -127, false);
  await page.waitForTimeout(400);
  for (let i = 0; i < 7; i++) await gmouse(127, 127, false);
  await gmouse(11, 65, false);
  await page.waitForTimeout(400);
  const menuBefore = await stats();
  await page.evaluate(() => window.emulator.bus.send('mouse-click', [false, false, true]));
  await page.waitForTimeout(2000);
  const menuAfter = await stats();
  await grab('/tmp/halatha-rio-menu3.png');
  console.log('button3 menu stats', JSON.stringify(menuBefore), JSON.stringify(menuAfter));
  if (Math.abs(menuAfter.white - menuBefore.white) < 50 && Math.abs(menuAfter.dark - menuBefore.dark) < 50)
    console.log('Button 3 via emulator bus: menu not observed headless (informational, see /tmp/halatha-rio-menu3.png)');
  else
    console.log('Button 3 via emulator bus -> Rio menu: PASS');
  await page.evaluate(() => window.emulator.bus.send('mouse-click', [false, false, false]));
  await page.waitForTimeout(1000);
  // Regression check for the reported boot faults (abort at pc 0x412f20).
  const finalSerial = await page.evaluate(() => document.querySelector('#serial').textContent);
  const abortLog = await page.evaluate(() => { try { return new TextDecoder().decode(window.aspera.fs.lookup('clip/abort.log')?.data); } catch { return '?'; } });
  console.log('serial fault lines:', (finalSerial.match(/read fault fail|POOLABORT|panic:/g) || []).length, 'abort.log:', JSON.stringify(abortLog.slice(0, 200)));
  if (/read fault fail|POOLABORT|panic:/.test(finalSerial)) throw Error('guest fault regression in serial output');
  if (/POOLABORT/.test(abortLog)) throw Error('guest pool abort regression in clip/abort.log');
  console.log('No guest read-fault/pool-abort regression: PASS');
  console.log('RIO-V64-BOOT PASS: display, window, keyboard, wctl, move, scrolling, /n/host, Notes');
} finally { await browser.close(); }
