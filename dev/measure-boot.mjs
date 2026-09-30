import { firefox } from 'playwright';

async function main() {
    const browser = await firefox.launch({ headless: true });
    const page = await browser.newPage();
    const start = Date.now();
    let lastScreenText = '';

    console.log('[Test] Navigating with choice=none (letting PLAN9.INI menudefault handle it)...');
    await page.goto('https://halatha.rentonsoftworks.com/?choice=none', { timeout: 60000 });

    for (let i = 0; i < 45; i++) {
        await page.waitForTimeout(1000);
        const data = await page.evaluate(() => {
            const div = document.querySelector('#screen_container > div');
            const status = document.getElementById('status')?.textContent;
            return {
                screenText: div ? div.innerText : '',
                status: status
            };
        });
        const elapsed = Date.now() - start;
        if (data.screenText && data.screenText !== lastScreenText) {
            lastScreenText = data.screenText;
            console.log(`\n[+${elapsed}ms] SCREEN:\n${data.screenText.trim()}\n`);
        } else {
            console.log(`[+${elapsed}ms] Status: "${data.status}"`);
        }
        if (data.screenText.includes('rio') || data.screenText.includes('init: starting /bin/rc') || data.status?.includes('desktop')) {
            console.log(`[Test] Boot completed in ${elapsed}ms!`);
            break;
        }
    }

    await browser.close();
}

main().catch(console.error);
