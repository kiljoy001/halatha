import { chromium } from 'playwright';

const url = process.argv[2] || 'https://halatha.rentonsoftworks.com/';
console.log(`Connecting to live VPS at ${url}...`);

const browser = await chromium.launch({
  args: ['--no-sandbox', '--disable-setuid-sandbox']
});
const page = await browser.newPage({ viewport: { width: 1200, height: 950 } });

const errors = [];
page.on('pageerror', error => errors.push(error.message));
page.on('console', msg => {
  console.log(`[Browser Console ${msg.type()}] ${msg.text()}`);
});

try {
  await page.goto(url, { waitUntil: 'domcontentloaded', timeout: 60000 });

  console.log('Waiting for VM boot and Rio readiness...');
  await page.waitForFunction(
    () => {
      const el = document.querySelector('#status');
      if (!el) return true;
      return /Guest ready|Rio ready|Boot failed/.test(el.textContent);
    },
    null, { timeout: 300000 });

  const status = await page.evaluate(() => document.querySelector('#status')?.textContent || 'ready (status removed)');
  console.log(`VM Status: ${status}`);
  if (/Boot failed/.test(status)) {
    throw new Error(`VM failed to boot: ${status}`);
  }

  // Click canvas to focus guest
  const canvas = await page.waitForSelector('canvas, wanix-vm canvas', { timeout: 10000 });
  const box = await canvas.boundingBox();
  console.log(`Canvas found at (${box.x}, ${box.y}, ${box.width}x${box.height})`);
  await page.mouse.click(box.x + box.width * 0.5, box.y + box.height * 0.3);
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

  const runCommand = async (cmd, delay = 2000) => {
    console.log(`> Executing: ${cmd}`);
    await typeLine(cmd);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(delay);
  };

  const readFile = async path => page.evaluate(async file => {
    const vm = document.querySelector('#harvey');
    const root = vm && vm.task && vm.task.taskRoot;
    if (!root) return null;
    try {
      const data = await root.readFile('ram/tmp/' + file);
      return new TextDecoder().decode(data);
    } catch (e) {
      return null;
    }
  }, path);

  const writeFile = async (path, content) => page.evaluate(async ({ file, text }) => {
    const vm = document.querySelector('#harvey');
    const root = vm && vm.task && vm.task.taskRoot;
    if (!root) return false;
    try {
      await root.writeFile('ram/tmp/' + file, new TextEncoder().encode(text));
      return true;
    } catch (e) {
      return false;
    }
  }, { file: path, text: content });

  // Run Gate 1: hello9 (Plan 9 C binary built with UCC)
  await runCommand('hello9 > /tmp/out-hello9', 3000);
  const outHello9 = await readFile('out-hello9');
  console.log(`\n=== GATE 1: hello9 output ===\n${outHello9 || '(no output captured via readFile)'}`);

  // Run Gate 2: posixdemo (APEX POSIX binary built with UCC)
  await runCommand('posixdemo > /tmp/out-posixdemo', 3000);
  const outPosix = await readFile('out-posixdemo');
  console.log(`\n=== GATE 2: posixdemo output ===\n${outPosix || '(no output captured via readFile)'}`);

  // Run Gate 3: mmaptest (Kernel syscall test)
  await runCommand('mmaptest > /tmp/out-mmaptest', 3000);
  const outMmap = await readFile('out-mmaptest');
  console.log(`\n=== GATE 3: mmaptest output ===\n${outMmap || '(no output captured via readFile)'}`);

  // Run Gate 4: in-guest UCC compilation
  const wrote = await writeFile('g.c', '#include <u.h>\n#include <libc.h>\nvoid main(void){\n    print("in-guest-ucc-success\\n");\n    exits(nil);\n}\n');
  console.log(`Wrote /tmp/g.c via 9P host bridge: ${wrote}`);
  await runCommand('ucc /tmp/g.c -o /tmp/g > /tmp/out-ucc >[2=1]', 8000);
  const outUcc = await readFile('out-ucc');
  console.log(`\n=== GATE 4: in-guest ucc compilation log ===\n${outUcc || '(no output / compilation succeeded silently)'}`);

  await runCommand('/tmp/g > /tmp/out-g >[2=1]', 3000);
  const outG = await readFile('out-g');
  console.log(`\n=== GATE 4: in-guest compiled binary execution ===\n${outG || '(no output captured via readFile)'}`);

  await page.screenshot({ path: '/tmp/vps-ucc-gates.png' });
  console.log('Saved screenshot to /tmp/vps-ucc-gates.png');

} catch (err) {
  console.error('Test run error:', err);
  const debug = await page.evaluate(() => ({
    status: document.querySelector('#status')?.textContent,
    body: document.body.innerText.slice(0, 500)
  })).catch(() => ({}));
  console.log('Debug info on failure:', debug);
  await page.screenshot({ path: '/tmp/vps-ucc-gates-failed.png' }).catch(() => {});
  throw err;
} finally {
  await browser.close();
}
