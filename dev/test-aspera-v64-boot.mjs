import { chromium } from 'playwright';
const url = process.argv[2] || 'http://127.0.0.1:7072/examples/basic-v64-aspera.html?test';
const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1100, height: 1000 } });
const errors = [];
page.on('pageerror', error => errors.push(error.message));
page.on('console', msg => { if (msg.type() === 'error') console.log('console:', msg.text()); });
try {
  await page.goto(url);
  const start = Date.now();
  while (Date.now() - start < 120000) {
    const state = await page.evaluate(() => ({ text: document.querySelector('#serial').textContent, frames: window.aspera?.display.frames || 0 }));
    if (state.frames && state.text.includes('ASPERA-LIVE READY')) break;
    if (/panic:|ASPERA-LIVE FAIL|suicide:/.test(state.text)) throw Error(state.text.slice(-5000));
    await page.waitForTimeout(1000);
  }
  const state = await page.evaluate(() => ({ text: document.querySelector('#serial').textContent, frames: window.aspera?.display.frames || 0 }));
  console.log(state.text.slice(-5000));
  if (!state.frames || !state.text.includes('ASPERA-LIVE READY')) throw Error('Desktop did not publish its initial frame');
  await page.screenshot({ path: '/tmp/halatha-aspera.png' });
  await page.locator('#desktop').focus();
  await page.keyboard.type('echo VMTERMOK', { delay: 100 });
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => document.querySelector('#serial').textContent.includes('ASPERA-TERM-COMMAND PASS'), null, { timeout: 60000 });
  await page.keyboard.type('cat /mnt/shelf/view/protocol > /lib/aspera/shelf-probe', { delay: 40 });
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => new TextDecoder().decode(window.aspera.fs.lookup('lib/aspera/shelf-probe')?.data) === 'o9shelf 1\n', null, { timeout: 60000 });
  console.log('Shelf protocol read through guest mount: PASS');
  const clickDesktop = async (x, y) => {
    const rect = await page.locator('#desktop').boundingBox();
    const before = await page.evaluate(() => window.aspera.display.frames);
    await page.mouse.click(rect.x + x * rect.width / 1024, rect.y + y * rect.height / 768);
    await page.waitForFunction(n => window.aspera.display.input.length === 0 && window.aspera.display.frames > n, before, { timeout: 60000 });
  };
  // These menu actions travel through ShelfViews, then focus the Notes window.
  await clickDesktop(40, 16);
  await clickDesktop(40, 50);
  await page.keyboard.type('HALATHA-NOTE-OK', { delay: 100 });
  await page.keyboard.press('Control+s');
  await page.waitForFunction(() => localStorage.getItem('halatha.aspera.note.v1') === 'HALATHA-NOTE-OK', null, { timeout: 60000 });
  const note = await page.evaluate(() => new TextDecoder().decode(window.aspera.fs.lookup('lib/aspera/note.txt').data));
  if (note !== 'HALATHA-NOTE-OK') throw Error('Guest Notes file did not match saved browser storage');
  console.log('Shelf menu, Notes keyboard input and Ctrl+S persistence: PASS');
  await page.screenshot({ path: '/tmp/halatha-aspera.png' });
  if (errors.length) throw Error(errors.join('\n'));
  console.log('ASPERA-V64-BOOT PASS: frame, rc command, shelf mount/menu, Notes save');
} catch (error) {
  await page.screenshot({ path: '/tmp/halatha-aspera-failed.png' });
  console.log(await page.evaluate(() => ({ status: document.querySelector('#status')?.textContent, serial: document.querySelector('#serial')?.textContent.slice(-5000), frames: window.aspera?.display.frames, input: window.aspera?.display.input.length, probe: new TextDecoder().decode(window.aspera?.fs.lookup('lib/aspera/shelf-probe')?.data), errors: window.aspera?.fs.errors })));
  throw error;
} finally { await browser.close(); }
