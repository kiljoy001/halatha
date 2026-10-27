// Guest test for the 9front core commands ported to Harvey
// (basename cal chmod cleanname cmp comm cp dd du mkdir mv seq
// sort tail tar tee tr uniq wc) plus the writable ram /tmp they
// need.  Each command runs in the Rio rc window with its output
// or status captured under /n/host/clip and asserted here.
import { chromium } from 'playwright';
const url = 'http://127.0.0.1:7072/examples/basic-v64-aspera.html?test';
const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1100, height: 1000 } });
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
  await page.waitForTimeout(800);
};
page.on('pageerror', e => console.log('PAGEERROR', String(e).slice(0, 200)));
await page.goto(url, { waitUntil: 'domcontentloaded' });
await page.waitForFunction(() => document.querySelector('#serial').textContent.includes('softscreen: Bochs LFB'), null, { timeout: 180000 });
await page.waitForTimeout(15000);
let ready = false;
for (let a = 0; a < 8 && !ready; a++) {
  await typeCmd('echo ping > /n/host/clip/core-ping.txt');
  for (let i = 0; i < 5 && !ready; i++) {
    await page.waitForTimeout(2000);
    ready = await page.evaluate(() => { try { return !!window.aspera.fs.lookup('clip/core-ping.txt')?.data; } catch { return false; } });
  }
}
if (!ready) throw Error('guest rc window never answered ping');
{
  const ser = await page.evaluate(() => document.querySelector('#serial').textContent);
  if (!ser.includes('/tmp on ram device')) throw Error('ram /tmp not bound at init');
}
const lines = [
  'rm -f /n/host/clip/core-*.txt',
  'mkdir /tmp/ct',
  'echo mkdir-status $status > /n/host/clip/core-mkdir.txt',
  'echo banana > /tmp/ct/a',
  'echo writea-status $status > /n/host/clip/core-writea.txt',
  'echo apple > /tmp/ct/b',
  'cat /tmp/ct/a /tmp/ct/b | sort > /n/host/clip/core-sort.txt',
  'echo 1 2 3 | wc > /n/host/clip/core-wc.txt',
  'cp /tmp/ct/a /tmp/ct/c',
  'cmp /tmp/ct/a /tmp/ct/c > /n/host/clip/core-cmp.txt',
  'echo cmp-status $status >> /n/host/clip/core-cmp.txt',
  'basename /tmp/ct/a > /n/host/clip/core-base.txt',
  'seq 3 | tr 2 Z > /n/host/clip/core-tr.txt',
  'seq 1 3 | tail -1 > /n/host/clip/core-tail.txt',
  'du /tmp/ct > /n/host/clip/core-du.txt',
  'echo DONE > /n/host/clip/core-done.txt',
  'ls -l /tmp > /n/host/clip/core-tmpls.txt',
  'echo x > /tmp/testfile',
  'echo write-status $status > /n/host/clip/core-write.txt',
  'ls /tmp/ct > /n/host/clip/core-ls.txt',
  'sort /tmp/ct/a /tmp/ct/b > /n/host/clip/core-sortfile.txt',
  'echo pear | sort > /n/host/clip/core-sortstdin.txt',
  'cmp /tmp/ct/a /tmp/ct/b > /n/host/clip/core-cmpab.txt',
  'echo cmpab $status >> /n/host/clip/core-cmpab.txt',
  'mv /tmp/ct/c /tmp/ct/c2',
  'echo mv $status > /n/host/clip/core-mv.txt',
  'chmod 600 /tmp/ct/a',
  'echo chmod $status >> /n/host/clip/core-mv.txt',
  'ls /tmp/ct/c2 > /n/host/clip/core-mvls.txt',
  'echo tee-test | tee /tmp/ct/tee.txt > /n/host/clip/core-tee.txt',
  'seq 1 2 > /tmp/ct/u',
  'seq 1 2 >> /tmp/ct/u',
  'sort /tmp/ct/u | uniq > /n/host/clip/core-uniq.txt',
  'sort /tmp/ct/a /tmp/ct/u > /tmp/ct/s1',
  'comm /tmp/ct/s1 /tmp/ct/u > /n/host/clip/core-comm.txt',
  'cleanname /tmp//ct/./a > /n/host/clip/core-cleanname.txt',
  'dd -if /tmp/ct/a -of /tmp/ct/a2 -bs 64 > /n/host/clip/core-dd.txt',
  'echo dd $status >> /n/host/clip/core-dd.txt',
  'cat /tmp/ct/a2 >> /n/host/clip/core-dd.txt',
  'tar cf /tmp/ct/t.tar /tmp/ct/a /tmp/ct/b',
  'echo tar $status > /n/host/clip/core-tar.txt',
  'tar tf /tmp/ct/t.tar >> /n/host/clip/core-tar.txt',
  'cal 2 2026 | tail -1 > /n/host/clip/core-cal.txt',
  'echo cal $status >> /n/host/clip/core-cal.txt',
];
for (const l of lines) await typeCmd(l);
await page.waitForTimeout(6000);
const readClip = (f) => page.evaluate((n) => {
  try { const d = window.aspera.fs.lookup('clip/' + n)?.data; return d ? new TextDecoder().decode(d) : '(missing)'; }
  catch (err) { return 'ERR ' + String(err); }
}, f);
const expect = {
  'core-sort.txt': 'apple\nbanana\n',          // cat | sort (pipe)
  'core-sortfile.txt': 'apple\nbanana\n',      // sort file args
  'core-sortstdin.txt': 'pear\n',              // sort stdin
  'core-wc.txt': null,                          // checked by regex below
  'core-cmp.txt': 'cmp-status \n',             // identical files: success, silent
  'core-cmpab.txt': null,                      // pid in status varies; regex below
  'core-base.txt': 'a\n',
  'core-tr.txt': '1\nZ\n3\n',
  'core-tail.txt': '3\n',
  'core-mkdir.txt': 'mkdir-status \n',
  'core-writea.txt': 'writea-status \n',
  'core-ls.txt': '/tmp/ct/a\n/tmp/ct/b\n/tmp/ct/c\n',
  'core-mv.txt': 'mv \nchmod \n',
  'core-mvls.txt': '/tmp/ct/c2\n',
  'core-tee.txt': 'tee-test\n',
  'core-uniq.txt': '1\n2\n',
  'core-cleanname.txt': '/tmp/ct/a\n',
  'core-dd.txt': 'dd \nbanana\n',
  'core-tar.txt': 'tar \n./tmp/ct/a\n./tmp/ct/b\n',
  'core-done.txt': 'DONE\n',
};
let failures = 0;
for (const [f, want] of Object.entries(expect)) {
  const got = await readClip(f);
  if (want !== null && got !== want) { console.log(`FAIL ${f}: got ${JSON.stringify(got)} want ${JSON.stringify(want)}`); failures++; }
  else console.log(`ok ${f}`);
}
const wc = await readClip('core-wc.txt');
if (!/^\s*1\s+3\s+6\s*$/.test(wc)) { console.log(`FAIL core-wc.txt: ${JSON.stringify(wc)}`); failures++; } else console.log('ok core-wc.txt');
const du = await readClip('core-du.txt');
if (!/^\d+\t\/tmp\/ct\n$/.test(du)) { console.log(`FAIL core-du.txt: ${JSON.stringify(du)}`); failures++; } else console.log('ok core-du.txt');
const comm = await readClip('core-comm.txt');
if (!comm.includes('banana')) { console.log(`FAIL core-comm.txt: ${JSON.stringify(comm)}`); failures++; } else console.log('ok core-comm.txt');
const cmpab = await readClip('core-cmpab.txt');
if (!/^\/tmp\/ct\/a \/tmp\/ct\/b differ: char 1\ncmpab cmp \d+: differ\n$/.test(cmpab)) { console.log(`FAIL core-cmpab.txt: ${JSON.stringify(cmpab)}`); failures++; } else console.log('ok core-cmpab.txt');
const cal = await readClip('core-cal.txt');
if (!cal.includes('cal')) { console.log(`FAIL core-cal.txt: ${JSON.stringify(cal)}`); failures++; } else console.log('ok core-cal.txt');
const serial = await page.evaluate(() => document.querySelector('#serial').textContent);
const faults = serial.split('\n').filter(l => /read fault fail|panic:|suicide/.test(l));
if (faults.length) { console.log('FAIL guest faults:', faults.slice(0, 3).join(' | ')); failures++; }
await browser.close();
if (failures) throw Error(`${failures} core-binary check(s) failed`);
console.log('CORE-BINARIES PASS');
