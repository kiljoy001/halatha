import { chromium } from 'playwright';
import { writeFileSync } from 'node:fs';

const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1100, height: 1000 } });
const errors = [];
page.on('pageerror', error => errors.push(error.message));
const output = () => page.evaluate(() => {
  const data = window.aspera?.fs.lookup('clip/ttf-result')?.data;
  return data ? new TextDecoder().decode(data) : '';
});
try {
  await page.goto(process.argv[2] || 'http://127.0.0.1:7072/examples/basic-v64-aspera.html?test');
  await page.waitForFunction(() => document.querySelector('#serial').textContent.includes('halatha: starting Rio'), null, { timeout: 120000 });
  await page.waitForFunction(() => {
    const canvas = document.querySelector('#vga canvas');
    if (canvas.width !== 1024) return false;
    const pixels = canvas.getContext('2d').getImageData(530, 68, 430, 200).data;
    let ink = 0, white = 0;
    for (let i = 0; i < pixels.length; i += 4) {
      if (pixels[i] < 80 && pixels[i + 1] < 80 && pixels[i + 2] < 80) ink++;
      if (pixels[i] > 200 && pixels[i + 1] > 200 && pixels[i + 2] > 200) white++;
    }
    return white > 60000 && ink > 10;
  }, null, { timeout: 120000 });
  await page.waitForTimeout(2500);
  for (const ch of 'ttfcheck > /n/host/clip/ttf-result >[2=1]') {
    await page.keyboard.press(ch === '>' ? 'Shift+.' : ch);
    await page.waitForTimeout(65);
  }
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => {
    const data = window.aspera.fs.lookup('clip/ttf-result')?.data;
    return data && /TTF-CHECK (PASS|FAIL)/.test(new TextDecoder().decode(data));
  }, null, { timeout: 120000 });
  const result = await output();
  console.log(result);
  if (!result.includes('TTF-CHECK PASS') || result.includes('TTF-CHECK FAIL')) throw Error('Guest font check failed');
  const data = await page.locator('#vga canvas').evaluate(c => c.toDataURL());
  writeFileSync('/tmp/aspera-fonts.png', Buffer.from(data.split(',')[1], 'base64'));
  const serial = await page.locator('#serial').textContent();
  if (/read fault fail|POOLABORT|panic:/.test(serial)) throw Error('Guest fault');
  if (errors.length) throw Error(errors.join('\n'));
  console.log('ASPERA-FONTS PASS');
} catch (error) {
  await page.locator('#vga canvas').screenshot({ path: '/tmp/aspera-fonts-failed.png' }).catch(() => {});
  console.log('font result:', await output().catch(() => 'unavailable'));
  console.log('serial:', await page.locator('#serial').textContent({ timeout: 1000 }).catch(() => 'unavailable'));
  throw error;
} finally {
  await browser.close();
}
