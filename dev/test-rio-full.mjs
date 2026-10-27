// Full automated Rio regression harness (genuine plan9front Rio in Harvey).
// Boots the guest once and exercises Rio end-to-end with no human input:
// keyboard + keymap, wctl window management, mouse buttons/menus/drag,
// scrolling, snarf/clipboard, Notes, focus and close/hide semantics.
// Every check is recorded; failures do not abort the run. Exit code is
// non-zero if any check fails. Screenshots land in /tmp/rio-full-*.png.
import { chromium } from 'playwright';
import fs from 'node:fs';

const url = process.argv[2] || 'http://127.0.0.1:7072/examples/basic-v64-aspera.html?test';
const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1100, height: 1000 } });
const results = [];
let serialText = '';

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
const regionHash = (x, y, w, h) => page.evaluate(([x, y, w, h]) => {
  const c = document.querySelector('#vga canvas');
  const d = c.getContext('2d').getImageData(x, y, w, h).data;
  let hh = 0;
  for (let i = 0; i < d.length; i += 16) hh = (hh * 31 + d[i]) >>> 0;
  return hh;
}, [x, y, w, h]);
const grab = async name => {
  const s = await page.evaluate(() => document.querySelector('#vga canvas').toDataURL('image/png'));
  fs.writeFileSync(name, Buffer.from(s.split(',')[1], 'base64'));
};
const seen = () => page.evaluate(() => {
  try { return new TextDecoder().decode(window.aspera.fs.lookup('clip/seen')?.data); } catch { return ''; }
});
const waitSeen = async (marker, timeout = 45000) => {
  await page.waitForFunction(m => {
    try { return new TextDecoder().decode(window.aspera.fs.lookup('clip/seen')?.data).includes(m); } catch { return false; }
  }, marker, { timeout });
};
const SHIFTED = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ>!@#$%^&*()_+{}|:"?~';
const typeCmd = async cmd => {
  // A stray rune can sit on the input line after window switches
  // (observed as a garbled argv[0]); clear the line like a user would.
  await page.keyboard.press('Control+u');
  await page.waitForTimeout(150);
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
  await page.waitForTimeout(3000);
};
const execProof = async (cmd, marker) => { await typeCmd(cmd); await waitSeen(marker); };
const typeText = async txt => {
  for (const ch of txt) {
    if (SHIFTED.includes(ch)) {
      await page.keyboard.down('Shift');
      await page.keyboard.press(ch === '>' ? '.' : ch.toLowerCase());
      await page.keyboard.up('Shift');
    } else {
      await page.keyboard.press(ch);
    }
    await page.waitForTimeout(40);
  }
};
const gmove = (dx, dy) => page.evaluate(([dx, dy]) => {
  document.querySelector('#vga canvas').dispatchEvent(new MouseEvent('mousemove', { movementX: dx, movementY: dy, bubbles: true }));
}, [dx, dy]);
const gdown = (button = 0) => page.evaluate(b => {
  document.querySelector('#vga canvas').dispatchEvent(new MouseEvent('mousedown', { button: b, bubbles: true }));
}, button);
const gup = (button = 0) => page.evaluate(b => {
  document.querySelector('#vga canvas').dispatchEvent(new MouseEvent('mouseup', { button: b, bubbles: true }));
}, button);
const gotoGuest = async (gx, gy) => {
  // Canvas movementY+ moves the guest pointer down (verified on screen).
  for (let i = 0; i < 20; i++) await gmove(-127, -127);
  await page.waitForTimeout(250);
  let x = 0, y = 0;
  while (x < gx) { const d = Math.min(127, gx - x); await gmove(d, 0); x += d; }
  while (y < gy) { const d = Math.min(127, gy - y); await gmove(0, d); y += d; }
  await page.waitForTimeout(250);
};
const busButtons = (l, m, r) => page.evaluate(([l, m, r]) => window.emulator.bus.send('mouse-click', [l, m, r]), [l, m, r]);
const busClick = async (left, mid, right, holdMs = 1200) => {
  await page.evaluate(([l, m, r]) => window.emulator.bus.send('mouse-click', [l, m, r]), [left, mid, right]);
  await page.waitForTimeout(holdMs);
  const during = await regionHash(0, 0, 1024, 768);
  await page.evaluate(() => window.emulator.bus.send('mouse-click', [false, false, false]));
  await page.waitForTimeout(600);
  return during;
};

async function test(name, fn) {
  try {
    await fn();
    results.push(['PASS', name]);
    console.log('PASS', name);
  } catch (e) {
    results.push(['FAIL', `${name}: ${e.message.slice(0, 160)}`]);
    console.log('FAIL', name, '-', e.message.slice(0, 160));
    await grab(`/tmp/rio-full-fail-${results.length}.png`);
  }
}

// ---- boot ----
await page.goto(url);
await page.waitForFunction(() => document.querySelector('#serial').textContent.includes('softscreen: Bochs LFB'), null, { timeout: 120000 });
{
  const t0 = Date.now();
  let s = await stats();
  while (Date.now() - t0 < 120000 && !(s.white > 60000 && s.dark > 30)) { await page.waitForTimeout(1000); s = await stats(); }
  if (!(s.white > 60000)) throw Error(`Rio did not draw: ${JSON.stringify(s)}`);
}
console.log('boot: Rio window drawn');

await test('no guest faults in serial or abort.log', async () => {
  serialText = await page.evaluate(() => document.querySelector('#serial').textContent);
  if (!serialText.includes('/n/host')) throw Error('/n/host not established');
  if (/read fault fail|panic:/.test(serialText)) throw Error('fault lines in serial');
  const abort = await page.evaluate(() => { try { return new TextDecoder().decode(window.aspera.fs.lookup('clip/abort.log')?.data); } catch { return '?'; } });
  if (/POOLABORT/.test(abort)) throw Error('POOLABORT in abort.log');
});

await test('rc echo executes and writes /n/host', async () => {
  await execProof('echo t01-echo > /n/host/clip/seen', 't01-echo');
});

await test('keyboard Ctrl-A (bol) via keymap', async () => {
  // Type an invalid command suffix, jump to line start, prepend "echo ",
  // jump to line end, then execute.
  await page.keyboard.press('Control+u');
  await page.waitForTimeout(200);
  await typeText('t02-ctrl-a > /n/host/clip/seen');
  await page.keyboard.press('Control+a');
  await page.waitForTimeout(400);
  await typeText('echo ');
  await page.keyboard.press('Control+e');
  await page.waitForTimeout(400);
  await page.keyboard.press('Enter');
  await waitSeen('t02-ctrl-a');
});

await test('keyboard Ctrl-U (dellinel) via keymap', async () => {
  await typeText('echo should-not-run > /n/host/clip/seen');
  await page.keyboard.press('Control+u');
  await page.waitForTimeout(400);
  await typeCmd('echo t03-ctrl-u > /n/host/clip/seen');
  await waitSeen('t03-ctrl-u');
  if ((await seen()).includes('should-not-run')) throw Error('pre-Ctrl-U line executed');
});

await test('clipboard guest snarf -> browser', async () => {
  await typeCmd('echo t04-snarf > /dev/snarf');
  await page.waitForFunction(() => localStorage.getItem('halatha.clip.text.v1')?.includes('t04-snarf'), null, { timeout: 30000 });
});

await test('clipboard browser -> guest snarf', async () => {
  await page.evaluate(() => window.aspera.setHostClip('t05-hostmark'));
  await page.waitForTimeout(3500);
  await execProof('cat /dev/snarf > /n/host/clip/seen', 't05-hostmark');
});

await test('notes persist to localStorage', async () => {
  await typeCmd('echo t06-note > /n/host/lib/aspera/note.txt');
  await page.waitForFunction(() => localStorage.getItem('halatha.aspera.note.v1')?.includes('t06-note'), null, { timeout: 30000 });
});

// First window (from start.rc): guest rect 520,40-980,320, centre ~ (750,180).
// NOTE: reading /dev/wctl blocks (it is an event stream); use $winid.
let win1Id = '';
const readWinId = async () => {
  const prev = await page.evaluate(() => {
    try { return new TextDecoder().decode(window.aspera.fs.lookup('clip/winid')?.data); } catch { return ''; }
  });
  await typeCmd('cat /dev/winid > /n/host/clip/winid');
  await page.waitForFunction(p => {
    try {
      const t = new TextDecoder().decode(window.aspera.fs.lookup('clip/winid')?.data);
      return t !== p && /\d/.test(t);
    } catch { return false; }
  }, prev, { timeout: 45000 });
  const t = await page.evaluate(() => new TextDecoder().decode(window.aspera.fs.lookup('clip/winid')?.data));
  return t.match(/(\d+)/)?.[1];
};
await test('read first window id via /dev/winid', async () => {
  win1Id = await readWinId();
  if (!win1Id) throw Error(`no winid in: ${await seen()}`);
});

await test('wctl creates second window and it executes', async () => {
  const before = await stats();
  await typeCmd("echo 'new -r 60 380 560 700' > /dev/wctl");
  await page.waitForTimeout(2500);
  const after = await stats();
  if (after.white - before.white < 20000) throw Error(`white ${before.white} -> ${after.white}`);
  await execProof('echo t08-second > /n/host/clip/seen', 't08-second');
  await grab('/tmp/rio-full-windows.png');
});

let win2Id = '';
await test('second window has distinct id', async () => {
  win2Id = await readWinId();
  if (!win2Id || win2Id === win1Id) throw Error(`winid ${win2Id} vs ${win1Id}`);
});

await test('wctl move relocates window (pixel check)', async () => {
  await typeCmd("echo 'move -r 120 400 620 720' > /dev/wctl");
  await execProof('echo t09-moved > /n/host/clip/seen', 't09-moved');
  await page.waitForTimeout(1500);
  // Window interior should now be white at (200, 500); before the move the
  // window was at y 380..700 x 60..560, so also verify old spot (580, 390)
  // is no longer white interior (it is outside both windows now).
  const probe = await page.evaluate(() => {
    const c = document.querySelector('#vga canvas');
    const px = (x, y) => { const d = c.getContext('2d').getImageData(x, y, 1, 1).data; return [d[0], d[1], d[2]]; };
    return { inside: px(300, 550), outside: px(580, 390) };
  });
  const isWhite = p => p[0] > 200 && p[1] > 200 && p[2] > 200;
  if (!isWhite(probe.inside)) throw Error(`moved window interior not white: ${probe.inside}`);
  if (isWhite(probe.outside)) throw Error('old window position still white');
});

await test('wctl resize enlarges window (pixel check)', async () => {
  await typeCmd("echo 'resize -r 120 400 900 740' > /dev/wctl");
  await execProof('echo t10-resized > /n/host/clip/seen', 't10-resized');
  await page.waitForTimeout(1500);
  const probe = await page.evaluate(() => {
    const c = document.querySelector('#vga canvas');
    const d = c.getContext('2d').getImageData(850, 700, 1, 1).data;
    return [d[0], d[1], d[2]];
  });
  if (!(probe[0] > 200 && probe[1] > 200)) throw Error(`resized window does not cover (850,700): ${probe}`);
});

await test('scrolling with PageDown/PageUp and Home/End', async () => {
  // One fork, many lines: repeated fork/exec in a tight loop trips a
  // pre-existing Harvey rc crash (see the fork-loop test at the end),
  // which would mask the Rio scrolling behaviour under test here.
  await execProof('ls /bin /bin /bin /bin /bin /bin /bin /bin; echo t11-scroll > /n/host/clip/seen', 't11-scroll');
  await page.waitForTimeout(1500);
  const top = await regionHash(60, 380, 840, 340);
  // Window starts at the bottom (scrolling), so PageUp must move first.
  await page.keyboard.press('PageUp');
  await page.waitForTimeout(1200);
  const up = await regionHash(60, 380, 840, 340);
  if (up === top) throw Error('PageUp had no effect');
  await page.keyboard.press('PageDown');
  await page.waitForTimeout(1200);
  const down = await regionHash(60, 380, 840, 340);
  if (down === up) throw Error('PageDown had no effect');
  await page.keyboard.press('Home');
  await page.waitForTimeout(1200);
  const home = await regionHash(60, 380, 840, 340);
  await page.keyboard.press('End');
  await page.waitForTimeout(1200);
  const end = await regionHash(60, 380, 840, 340);
  if (end === home) throw Error('End had no effect');
});

await test('mouse wheel scrolls window', async () => {
  await execProof('echo ps2intellimouse > /dev/mousectl; echo t12-im > /n/host/clip/seen', 't12-im');
  const before = await regionHash(60, 380, 840, 340);
  await gotoGuest(400, 550);
  await page.evaluate(() => window.emulator.bus.send('mouse-wheel', [3, 0]));
  await page.waitForTimeout(1500);
  const after = await regionHash(60, 380, 840, 340);
  if (after === before) throw Error('wheel had no effect');
});

await test('button 2 menu opens on window (bus)', async () => {
  await gotoGuest(400, 550);
  const before = await regionHash(0, 0, 1024, 768);
  const during = await busClick(false, true, false, 1500);
  if (during === before) throw Error('no pixel change while button 2 held');
  await grab('/tmp/rio-full-menu2.png');
});

await test('button 3 menu opens on desktop (bus)', async () => {
  await gotoGuest(950, 740);
  const before = await regionHash(0, 0, 1024, 768);
  await busButtons(false, false, true);
  await page.waitForTimeout(1500);
  const during = await regionHash(0, 0, 1024, 768);
  if (during === before) throw Error('no pixel change while button 3 held');
  await grab('/tmp/rio-full-menu3.png');
  // Cancel without selecting: releasing over the first item would pick
  // "New" and leave Rio waiting for a window sweep. Move off the menu
  // first, then release.
  await gotoGuest(120, 120);
  await busButtons(false, false, false);
  await page.waitForTimeout(600);
});

await test('drag window by border moves it', async () => {
  // Second window top border is near y=400, x 120..900 after resize.
  await gotoGuest(400, 402);
  const before = await page.evaluate(() => {
    const c = document.querySelector('#vga canvas');
    const d = c.getContext('2d').getImageData(850, 430, 1, 1).data;
    return [d[0], d[1], d[2]];
  });
  // Rio moves a window by dragging its border with button 3 (drag());
  // buttons 1/2 on the border start a resize sweep instead. Canvas
  // mousedown events do not reach v86, so drive buttons via the bus.
  await busButtons(false, false, true);
  await page.waitForTimeout(300);
  for (let i = 0; i < 4; i++) { await gmove(-30, 0); await page.waitForTimeout(120); }
  await busButtons(false, false, false);
  await page.waitForTimeout(2000);
  await grab('/tmp/rio-full-drag.png');
  const edge = await page.evaluate(() => {
    const c = document.querySelector('#vga canvas');
    const ctx = c.getContext('2d');
    for (let x = 0; x < 1024; x++) {
      const d = ctx.getImageData(x, 550, 1, 1).data;
      if (d[0] > 200 && d[1] > 200 && d[2] > 200) return x;
    }
    return -1;
  });
  console.log('  drag: leftmost white at y=550:', edge, 'pixel before:', before);
  if (edge < 0 || edge > 115) throw Error(`window did not move left, edge at ${edge}`);
});

await test('click switches focus between windows', async () => {
  // Click first window (750,180), prove identity; click second (400,600).
  await gotoGuest(750, 180);
  await busButtons(true, false, false); await page.waitForTimeout(250); await busButtons(false, false, false);
  await page.waitForTimeout(600);
  await page.keyboard.press('End');
  await page.waitForTimeout(400);
  await page.keyboard.press('Control+u');
  await page.waitForTimeout(300);
  const a = await readWinId();
  if (a !== win1Id) throw Error(`focus did not return to window 1: ${a} vs ${win1Id}`);
  await gotoGuest(400, 600);
  await busButtons(true, false, false); await page.waitForTimeout(250); await busButtons(false, false, false);
  await page.waitForTimeout(600);
  await page.keyboard.press('End');
  await page.waitForTimeout(400);
  await page.keyboard.press('Control+u');
  await page.waitForTimeout(300);
  const b = await readWinId();
  if (!b || b === win1Id) throw Error('focus did not switch to window 2');
});

await test('hide then unhide window by id', async () => {
  const id = win2Id;
  const before = await stats();
  await typeCmd(`echo 'hide -id ${id}' > /dev/wctl`);
  await page.waitForTimeout(1500);
  const hidden = await stats();
  if (!(hidden.white < before.white * 0.75)) throw Error(`hide did not reduce white: ${before.white} -> ${hidden.white}`);
  // Hiding the current window leaves no window with keyboard focus
  // (Rio sets input=nil); click window 1 before typing again.
  await gotoGuest(750, 180);
  await busButtons(true, false, false); await page.waitForTimeout(250); await busButtons(false, false, false);
  await page.waitForTimeout(500);
  await execProof('echo t15-hidden > /n/host/clip/seen', 't15-hidden');
  await typeCmd(`echo 'unhide -id ${id}' > /dev/wctl`);
  await execProof('echo t15-unhidden > /n/host/clip/seen', 't15-unhidden');
  await page.waitForTimeout(1500);
  const restored = await stats();
  if (!(restored.white > hidden.white * 1.2)) throw Error(`unhide did not restore white: ${hidden.white} -> ${restored.white}`);
});

await test('delete second window, focus falls back', async () => {
  await gotoGuest(400, 600);
  await busButtons(true, false, false); await page.waitForTimeout(250); await busButtons(false, false, false);
  await page.waitForTimeout(500);
  const before = await stats();
  await typeCmd('echo delete > /dev/wctl');
  await page.waitForTimeout(2000);
  const after = await stats();
  if (!(after.white < before.white * 0.7)) throw Error(`delete did not remove window: ${before.white} -> ${after.white}`);
  // Deleting the current window also drops keyboard focus; click win1.
  await gotoGuest(750, 180);
  await busButtons(true, false, false); await page.waitForTimeout(250); await busButtons(false, false, false);
  await page.waitForTimeout(500);
  await page.keyboard.press('End');
  await page.waitForTimeout(400);
  await execProof('echo t17-alive > /n/host/clip/seen', 't17-alive');
});

await test('no guest faults at end of run', async () => {
  const s = await page.evaluate(() => document.querySelector('#serial').textContent);
  const lines = s.split('\n').filter(l => /read fault fail|panic:|suicide/.test(l));
  if (lines.length > 0) throw Error(`${lines.length} fault lines in serial: ${lines.slice(0, 4).join(' | ')}`);
});

await test('guest rc survives repeated fork/exec loop', async () => {
  // Bug watch: on Harvey (with or without Rio) a tight fork/exec loop
  // intermittently kills rc (fault at abort, pool corruption in the
  // wait/free path). Reproduces on the bare Harvey console. Kept as a
  // test so the fix is detected without human intervention.
  await execProof('for(i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20) ls /bin > /n/host/clip/big; echo t18-loop > /n/host/clip/seen', 't18-loop');
  const abort = await page.evaluate(() => { try { return new TextDecoder().decode(window.aspera.fs.lookup('clip/abort.log')?.data); } catch { return ''; } });
  if (/POOLABORT/.test(abort)) throw Error('POOLABORT in abort.log');
});

await browser.close();
const failed = results.filter(r => r[0] === 'FAIL');
console.log(`\nRIO-FULL SUMMARY: ${results.length - failed.length}/${results.length} passed`);
for (const [st, name] of results) console.log(st, name);
if (failed.length) process.exit(1);
console.log('RIO-FULL PASS');
