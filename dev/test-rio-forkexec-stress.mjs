// Repeat-until-fail stress harness for the intermittent Harvey rc
// fork/exec crash (fault at libc abort, suspected pool/wait race).
//
// A single short fork/exec loop proves nothing about a race: it can
// pass by luck. This harness instead runs a matrix of loop variants
// (plain exec, exec with host-file output, pipelines, background+wait,
// plus a no-fork builtin control) over several fresh boots, counts
// completed rounds, and reports a failure rate. Any guest fault,
// POOLABORT, missing completion marker, or dead shell is recorded with
// boot/variant/round context and a screenshot in /tmp.
//
// Usage:
//   node dev/test-rio-forkexec-stress.mjs
//   STRESS_BOOTS=10 STRESS_ROUNDS=8 STRESS_COUNT=25 node dev/test-rio-forkexec-stress.mjs
//   STRESS_VARIANTS=exec-null,pipe node dev/test-rio-forkexec-stress.mjs
//
// Exit code is 1 if the crash (or any guest fault) was observed, 0 if
// every boot completed every round. "No failure observed" is reported
// with the sample size; it is not proof the race is gone.
import { chromium } from 'playwright';
import fs from 'node:fs';

const url = process.argv[2] || 'http://127.0.0.1:7072/examples/basic-v64-aspera.html?test';
const BOOTS = Number(process.env.STRESS_BOOTS || 3);
const ROUNDS = Number(process.env.STRESS_ROUNDS || 6);
const COUNT = Number(process.env.STRESS_COUNT || 25);
const LIST = Array.from({ length: COUNT }, (_, i) => i + 1).join(' ');

const rcmainExpected = fs.readFileSync(new URL('../local/aspera/rootfs/rc/lib/rcmain', import.meta.url), 'utf8');
// Workloads are typed as one-liners, NOT read from a script file:
// script files come over the same host 9P, and a truncated 9P read
// (the suspected cause of the boot-time "rc: line 4: token EOF" error
// in rcmain, whose line 3 opens a quote closed on line 4) would
// otherwise be mistaken for a fork/exec failure. The trailing marker
// echo is appended per round by runRound().
const VARIANTS = {
  // Control: loop + builtin only, no fork/exec. A failure here points
  // away from fork/exec (parser/loop/heap in rc itself).
  builtin: `for(i in ${LIST}) echo stress > /dev/null`,
  // Read-integrity control: copy rcmain back to the host every round;
  // the host byte-compares it. Detects truncated 9P reads directly.
  readback: `cat /rc/lib/rcmain > /n/host/clip/rcmain-copy`,
  // Plain repeated fork/exec + wait, output discarded.
  'exec-null': `for(i in ${LIST}) ls /bin > /dev/null`,
  // Same, but every child writes across virtio-9p to the host.
  'exec-host': `for(i in ${LIST}) ls /bin > /n/host/clip/big`,
  // Two processes per iteration: fork/exec on both sides of a pipe.
  pipe: `for(i in ${LIST}) ls /bin | cat > /dev/null`,
  // Many children alive at once, reaped by a single wait.
  bgwait: `for(i in ${LIST}){ls /bin > /dev/null &}; wait`,
};
const variantNames = (process.env.STRESS_VARIANTS || Object.keys(VARIANTS).join(','))
  .split(',').map(s => s.trim()).filter(s => VARIANTS[s]);
if (!variantNames.length) { console.error('no valid STRESS_VARIANTS'); process.exit(2); }

const browser = await chromium.launch({ args: ['--no-sandbox'] });
const failures = [];
let bootsDone = 0, roundsDone = 0, execsDone = 0;

const SHIFTED = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ>!@#$%^&*()_+{}|:"?~';
async function typeCmd(page, cmd) {
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
}
const hostRead = (page, path) => page.evaluate(p => {
  try { return new TextDecoder().decode(window.aspera.fs.lookup(p)?.data); } catch { return ''; }
}, path);
const faultCount = text => (text.match(/read fault fail|panic:|suicide/g) || []).length;

async function waitMarker(page, marker, timeout = 90000) {
  try {
    await page.waitForFunction(m => {
      try { return new TextDecoder().decode(window.aspera.fs.lookup('clip/stress')?.data).includes(m); }
      catch { return false; }
    }, marker, { timeout });
    return true;
  } catch { return false; }
}

async function boot(page, bootIdx) {
  await page.goto(url);
  await page.waitForFunction(() => document.querySelector('#serial').textContent.includes('softscreen: Bochs LFB'), null, { timeout: 120000 });
  const stats = () => page.evaluate(() => {
    const c = document.querySelector('#vga canvas');
    const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data;
    let white = 0;
    for (let i = 0; i < d.length; i += 4) if (d[i] > 200 && d[i + 1] > 200 && d[i + 2] > 200) white++;
    return white;
  });
  const t0 = Date.now();
  let white = 0;
  while (Date.now() - t0 < 120000 && white < 60000) { await page.waitForTimeout(1000); white = await stats(); }
  if (white < 60000) throw Error('Rio window did not draw');
  const settle = Number(process.env.STRESS_SETTLE_MS || 0);
  if (settle) { console.log(`boot ${bootIdx}: settling ${settle}ms`); await page.waitForTimeout(settle); }
  await page.evaluate(() => {
    if (!window.aspera.fs.lookup('clip/stress'))
      window.aspera.fs.addFile('clip/stress', '', { writable: true, mode: 0o600 });
  });
  console.log(`boot ${bootIdx}: Rio up`);
}

for (let b = 1; b <= BOOTS; b++) {
  const page = await browser.newPage({ viewport: { width: 1100, height: 1000 } });
  let bootFailed = false;
  try {
    await boot(page, b);
  } catch (e) {
    failures.push({ boot: b, variant: '(boot)', round: 0, kind: 'boot-failure', detail: e.message });
    console.log(`boot ${b}: FAILED to boot - ${e.message}`);
    await page.close();
    continue;
  }
  let baseFaults = faultCount(await page.evaluate(() => document.querySelector('#serial').textContent));

  outer:
  for (const variant of variantNames) {
    for (let r = 1; r <= ROUNDS; r++) {
      const marker = `stress-b${b}-${variant}-r${r}-ok`;
      await typeCmd(page, `${VARIANTS[variant]}; echo ${marker} > /n/host/clip/stress`);
      let ok = await waitMarker(page, marker);
      if (ok && variant === 'readback') {
        const copy = await hostRead(page, 'clip/rcmain-copy');
        if (copy !== rcmainExpected) {
          failures.push({ boot: b, variant, round: r, kind: 'read-truncated',
            detail: `rcmain copy ${copy.length} bytes, expected ${rcmainExpected.length}` });
          console.log(`boot ${b}: FAILURE variant=readback round=${r} kind=read-truncated ` +
            `(copy ${copy.length}/${rcmainExpected.length} bytes)`);
          bootFailed = true;
          break outer;
        }
      }
      const serial = await page.evaluate(() => document.querySelector('#serial').textContent);
      const faults = faultCount(serial);
      const abort = await hostRead(page, 'clip/abort.log');
      if (!ok || faults > baseFaults || /POOLABORT/.test(abort)) {
        // Distinguish a dead shell (race crash) from a script error:
        // probe with a trivial command on a separate marker file.
        let kind = 'guest-fault';
        if (ok && faults <= baseFaults) kind = 'pool-abort';
        if (!ok) {
          await typeCmd(page, 'echo probe-alive > /n/host/clip/stress');
          kind = await waitMarker(page, 'probe-alive', 20000) ? 'marker-missing-shell-alive' : 'shell-dead-after-loop';
        }
        const shot = `/tmp/rio-stress-fail-b${b}-${variant}-r${r}.png`;
        const data = await page.evaluate(() => document.querySelector('#vga canvas').toDataURL('image/png'));
        fs.writeFileSync(shot, Buffer.from(data.split(',')[1], 'base64'));
        failures.push({ boot: b, variant, round: r, kind, screenshot: shot,
          detail: abort ? `abort.log: ${JSON.stringify(abort.slice(0, 200))}` : '',
          serialTail: serial.slice(-600) });
        console.log(`boot ${b}: FAILURE variant=${variant} round=${r} kind=${kind} (screenshot ${shot})`);
        bootFailed = true;
        break outer;
      }
      baseFaults = faults;
      roundsDone++;
      execsDone += (variant === 'builtin' || variant === 'readback') ? 0 : COUNT * (variant === 'pipe' ? 2 : 1);
      console.log(`boot ${b}: ${variant} round ${r}/${ROUNDS} ok (${execsDone} execs so far)`);
    }
  }
  if (!bootFailed) bootsDone++;
  await page.close();
}

await browser.close();
console.log(`\nRIO-FORKEXEC-STRESS SUMMARY: ${bootsDone}/${BOOTS} boots clean, ` +
  `${roundsDone} rounds, ~${execsDone} fork/exec iterations, ${failures.length} failure(s)`);
for (const f of failures) {
  console.log(`FAIL boot=${f.boot} variant=${f.variant} round=${f.round} kind=${f.kind}${f.screenshot ? ' shot=' + f.screenshot : ''}`);
  if (f.detail) console.log(`  detail: ${f.detail}`);
  if (f.serialTail) console.log(`  serial tail: ${f.serialTail.replace(/\s*\n\s*/g, ' | ').slice(-400)}`);
}
if (failures.length) {
  const kinds = [...new Set(failures.map(f => f.kind))].join(', ');
  console.log(`FAILURE OBSERVED under stress (kinds: ${kinds})`);
  process.exit(1);
}
console.log(`NO FAILURE OBSERVED in ${BOOTS} boots / ~${execsDone} execs (sample only; not proof the race is fixed)`);
