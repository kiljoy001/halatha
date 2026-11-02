// End-to-end check for the 9front tool set staged into the Rio guest.
// node dev/test-aspera-tools.mjs [url]
// Requires the repository root on a static server (default :7072).
import { chromium } from 'playwright';

const url = process.argv[2] ||
  'http://127.0.0.1:7072/examples/basic-v64-aspera.html?test';
const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1200, height: 950 } });
const errors = [];
page.on('pageerror', error => errors.push(error.message));

const EXTENDED = ['bind', 'unmount', 'sum', 'crc32', 'read', 'pwd', 'touch',
  'ns', 'strings', 'factor', 'primes', 'dc', 'lnfs', 'md5sum', 'mount', 'srv',
  'grep', 'sed', 'diff', 'chgrp', 'p', 'fmt', 'split', 'look', 'ascii',
  'unicode', 'col', 'pr', 'ps', 'time', 'xd', 'mtime'];
const CORE = ['ed', 'sam', 'mk', 'yacc', 'patch', 'gzip', 'gunzip', 'zip',
  'unzip', 'iostats', 'swap', 'srvfs', 'ramfs', 'mntgen', 'awk', 'bzip2',
  'bunzip2', 'plumb', 'plumber', 'tpmfs', 'tpmtest'];

try {
  await page.goto(url);
  await page.waitForFunction(
    () => /Rio ready|Boot failed/.test(document.querySelector('#status')?.textContent || ''),
    null, { timeout: 120000 });
  if (await page.locator('#status').textContent() !== 'Rio ready')
    throw Error('Rio did not report readiness');
  await page.screenshot({ path: '/tmp/halatha-aspera-tools.png' });

  // Focus the Rio window so the kernel keyboard sees the events.
  const box = await page.locator('#vga canvas').boundingBox();
  await page.mouse.click(box.x + box.width * 0.7, box.y + box.height * 0.3);
  // The first painted frame precedes the rc prompt by a moment.
  await page.waitForTimeout(3000);
  await page.keyboard.press('Enter');

  const shift = async key => {
    await page.keyboard.down('Shift');
    await page.keyboard.press(key);
    await page.keyboard.up('Shift');
  };
  const shifted = {
    '!': 'Digit1', '@': 'Digit2', '#': 'Digit3', '$': 'Digit4', '%': 'Digit5',
    '^': 'Digit6', '&': 'Digit7', '*': 'Digit8', '(': 'Digit9', ')': 'Digit0',
    '_': 'Minus', '+': 'Equal', '{': 'BracketLeft', '}': 'BracketRight',
    '|': 'Backslash', ':': 'Semicolon', '"': 'Quote', "'": 'Quote',
    '<': 'Comma', '>': 'Period', '?': 'Slash', '~': 'Backquote',
  };
  // Playwright's type() does not set shiftKey; Rio reads scancodes, so
  // every shifted character must be pressed explicitly.
  const typeLine = async text => {
    for (const ch of text) {
      if (ch === '\t') {
        await page.keyboard.press('Tab');
      } else if (/[A-Z]/.test(ch)) {
        await shift('Key' + ch);
      } else if (shifted[ch]) {
        await shift(shifted[ch]);
      } else {
        await page.keyboard.type(ch, { delay: 20 });
      }
    }
  };
  // Playwright's type() does not set shiftKey; Rio reads scancodes.
  const run = async (command, out) => {
    await typeLine(`${command} > /lib/aspera/${out}`);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(1200);
  };
  const read = async name => page.evaluate(file => {
    const node = window.aspera.fs.lookup('lib/aspera/' + file);
    return node ? new TextDecoder().decode(node.data) : null;
  }, name);

  await run('ls /bin', 'tool-list');
  const listing = await read('tool-list');
  if (!listing) throw Error('ls /bin produced no output');
  for (const name of [...EXTENDED, ...CORE])
    if (!listing.includes(`/bin/${name}\n`))
      throw Error(`guest /bin is missing ${name}`);

  await run('pwd', 'tool-pwd');
  if ((await read('tool-pwd')).trim() !== '/')
    throw Error('pwd did not report /');

  await run('factor 1234', 'tool-factor');
  const factor = (await read('tool-factor')).trim().split(/\s+/).join(' ');
  if (factor !== '1234 2 617')
    throw Error(`factor output was ${JSON.stringify(factor)}`);

  await run('md5sum /lib/aspera/note.txt', 'tool-md5');
  if (!/^[0-9a-f]{32}\t\/lib\/aspera\/note\.txt/.test(await read('tool-md5')))
    throw Error('md5sum did not print a digest');

  await run('strings /bin/echo', 'tool-strings');
  if (!(await read('tool-strings')).includes('echo: write error'))
    throw Error('strings did not find the expected literal');

  await run('ns', 'tool-ns');
  const ns = await read('tool-ns');
  if (!ns.includes('mount') && !ns.includes('bind'))
    throw Error('ns did not print the namespace');

  // grep, sed and diff against a file created through the same shell.
  await run('echo hello', 't1');
  await run('sed s/hello/world/ /lib/aspera/t1', 't2');
  if ((await read('t2')).trim() !== 'world')
    throw Error(`sed output was ${JSON.stringify(await read('t2'))}`);
  await run('grep world /lib/aspera/t2', 't3');
  if ((await read('t3')).trim() !== 'world')
    throw Error(`grep output was ${JSON.stringify(await read('t3'))}`);
  await run('diff /lib/aspera/t1 /lib/aspera/t2', 't4');
  if (!(await read('t4')).includes('1c1'))
    throw Error(`diff output was ${JSON.stringify(await read('t4'))}`);
  await run('xd /lib/aspera/t1', 't5');
  if (!(await read('t5')).includes('68656c6c'))
    throw Error(`xd output was ${JSON.stringify(await read('t5'))}`);

  // Monocypher known-answer vectors through the guest.
  await run('/bin/mctest', 'mc');
  const mc = await read('mc');
  if (!mc.includes('MONOCYPHER PASS'))
    throw Error(`mctest output was ${JSON.stringify(mc)}`);

  // libtab: a config row with hashed and signed cells.
  const exec = async text => {
    await typeLine(text);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(1500);
  };
  const pipe = async text => exec(text);
  await run('tab /lib/aspera/cfg.tab init config key value secret:HASHED:blake2b sig:SIGNED sealed:ENCRYPTED', 'tab-init');
  await run('tab /lib/aspera/cfg.tab set key host value example.org', 'tab-set');
  await run('tab /lib/aspera/cfg.tab get key host value', 'tab-get');
  if ((await read('tab-get')).trim() !== 'example.org')
    throw Error(`tab get output was ${JSON.stringify(await read('tab-get'))}`);
  await pipe('echo hunter2 | tab /lib/aspera/cfg.tab hash key host secret');
  await exec('tab keygen /lib/aspera/tab-sk /lib/aspera/tab-pk');
  await pipe('echo hello | tab /lib/aspera/cfg.tab sign key host sig /lib/aspera/tab-sk');
  await run('tab /lib/aspera/cfg.tab verify key host sig /lib/aspera/tab-pk', 'tab-verify');
  if ((await read('tab-verify')) !== 'hello')
    throw Error(`tab verify output was ${JSON.stringify(await read('tab-verify'))}`);
  await exec('tab keygen /lib/aspera/tab-key');
  await pipe('echo topsecret | tab /lib/aspera/cfg.tab encrypt key host sealed /lib/aspera/tab-key');
  await run('tab /lib/aspera/cfg.tab decrypt key host sealed /lib/aspera/tab-key', 'tab-decrypt');
  if ((await read('tab-decrypt')) !== 'topsecret')
    throw Error(`tab decrypt output was ${JSON.stringify(await read('tab-decrypt'))}`);
  await run('tab /lib/aspera/cfg.tab dump', 'tab-dump');
  const dump = await read('tab-dump');
  if (!dump.includes('secret=hashed:') || !dump.includes('sig=signed:')
      || !dump.includes('sealed=encrypted:'))
    throw Error(`tab dump was ${JSON.stringify(dump)}`);

  // Core tools: compression, ed, mk, yacc and ramfs.
  // gzip writes <name>.gz beside the operand in the current directory.
  await exec('cd /lib/aspera');
  await exec('echo gzhello > gztest');
  await exec('gzip gztest');
  await run('ls -l gztest.gz', 'gzls');
  if (!(await read('gzls')).includes('gztest.gz'))
    throw Error(`gzip did not create gztest.gz: ${JSON.stringify(await read('gzls'))}`);
  await exec('gunzip gztest.gz');
  await run('cat gztest', 'gzcat');
  if ((await read('gzcat')).trim() !== 'gzhello')
    throw Error(`gunzip output was ${JSON.stringify(await read('gzcat'))}`);

  await exec('echo hello > edtest');
  await exec('ed edtest');
  await exec('a');
  await exec('hello2');
  await exec('.');
  await exec('w');
  await exec('q');
  await run('cat /lib/aspera/edtest', 'edout');
  if (!(await read('edout')).includes('hello2'))
    throw Error(`ed output was ${JSON.stringify(await read('edout'))}`);

  await exec('mkdir ram');
  await exec('ramfs -m ram &');
  await exec('echo ramok > ram/x');
  await run('cat ram/x', 'ramcat');
  if ((await read('ramcat')).trim() !== 'ramok')
    throw Error(`ramfs output was ${JSON.stringify(await read('ramcat'))}`);

  await exec('echo %token A > g.y');
  await exec('echo %% >> g.y');
  await exec("echo 's: A;' >> g.y");
  await exec('yacc -d g.y');
  await run('ls -l y.tab.c', 'yaccout');
  if (!(await read('yaccout')).includes('y.tab.c'))
    throw Error('yacc did not write y.tab.c');

  // Inject the mkfile through the host 9P tree; typing a literal tab
  // through the emulated keyboard is unreliable.
  await page.evaluate(() => {
    window.aspera.fs.addFile('lib/aspera/mkfile', 'all:\n\techo hi\n');
  });
  await run('mk', 'mklog');
  if (!(await read('mklog')).includes('hi'))
    throw Error(`mk output was ${JSON.stringify(await read('mklog'))}`);

  await page.evaluate(() => {
    window.aspera.fs.addFile('lib/aspera/p.awk', '{ print $2 }\n');
  });
  await exec('echo one two > awkfile');
  await run('awk -f p.awk awkfile', 'awkout');
  if ((await read('awkout')).trim() !== 'two')
    throw Error(`awk output was ${JSON.stringify(await read('awkout'))}`);

  await exec('echo hello > bztest');
  await exec('bzip2 bztest');
  await run('ls -l bztest.bz2', 'bzls');
  if (!(await read('bzls')).includes('bztest.bz2'))
    throw Error(`bzip2 did not create bztest.bz2: ${JSON.stringify(await read('bzls'))}`);
  await exec('bunzip2 bztest.bz2');
  await run('cat bztest', 'bzcat');
  if ((await read('bzcat')).trim() !== 'hello')
    throw Error(`bunzip2 output was ${JSON.stringify(await read('bzcat'))}`);

  await exec('mkdir /lib/aspera/tpm');
  await exec('tpmfs -m /lib/aspera/tpm -t mock &');
  await page.waitForTimeout(1000);
  await run('tpmtest -t mock /lib/aspera/tpm', 'tpmtest');
  if (!(await read('tpmtest')).includes('TPM2 PASS'))
    throw Error(`tpmtest output was ${JSON.stringify(await read('tpmtest'))}`);
  await run('xd /lib/aspera/tpm/pcr/0', 'tpm-pcr');
  if (!(await read('tpm-pcr')).includes('42424242'))
    throw Error(`tpm pcr output was ${JSON.stringify(await read('tpm-pcr'))}`);

  if (errors.length) throw Error(errors.join('\n'));
  await page.screenshot({ path: '/tmp/halatha-aspera-tools.png' });
  console.log(`ASPERA-TOOLS PASS: ${EXTENDED.length + CORE.length} tools; grep/sed/diff, xd, Monocypher, libtab and core editors/build/compression/services ran`);
} catch (error) {
  console.log(await page.evaluate(() => ({
    status: document.querySelector('#status')?.textContent,
    serial: document.querySelector('#serial')?.textContent.slice(-2000),
  })));
  await page.screenshot({ path: '/tmp/halatha-aspera-tools-failed.png' });
  throw error;
} finally {
  await browser.close();
}
