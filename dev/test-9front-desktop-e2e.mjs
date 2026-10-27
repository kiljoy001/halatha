// Boot the real 9front image and verify browser timezone, Rio startup apps,
// and the preinstalled Objective-9C compiler with an in-guest build.
import assert from "node:assert/strict";
import { chromium } from "playwright";
import { plan9Timezone } from "../extras/9front/browser-timezone.mjs";

const url = process.argv[2] || "http://127.0.0.1:7070/examples/basic-v86-9front-direct.html";
const zone = process.argv[3] || "Asia/Kathmandu";
const browser = await chromium.launch({ args: ["--no-sandbox"] });
const context = await browser.newContext({ timezoneId: zone });
const page = await context.newPage();
const pageErrors = [];
page.on("pageerror", error => pageErrors.push(error.message));
await page.route("**/extras/9front/halatha.local", async route => {
    const response = await route.fetch();
    const body = await response.text();
    const probe = `
{
    while(sleep 1){
        if(test -r /n/host/boot/time-ping){
            token=\`{cat /n/host/boot/time-ping}
            echo $token \`{date -n} >/n/host/boot/time-pong
            exit
        }
    }
} >/dev/null >[2=1] &
{
    apps=()
    for(i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37 38 39 40 41 42 43 44 45 46 47 48 49 50 51 52 53 54 55 56 57 58 59 60){
        if(~ $#apps 0){
            if(ps | grep -s catclock && ps | grep -s bar)
                apps=1
            if(~ $#apps 0)
                sleep 1
        }
    }
    echo TIMEZONE
    cat /dev/sysname
    echo
    cat /n/host/boot/timezone-name
    sed -n '1p' /env/timezone
    date
    echo STARTUP
    tail -8 /usr/glenda/bin/rc/riostart
    ps | grep catclock
    ps | grep bar
    ps | grep fgui
    echo O9BUILD
    echo 'main { print("o9c ready\\n"); }' >/tmp/halatha-o9-hello.o9
    o9build /tmp/halatha-o9-hello.o9 /tmp/halatha-o9-hello
    if(test -x /tmp/halatha-o9-hello)
        /tmp/halatha-o9-hello
    echo GUEST-EPOCH \`{date -n}
    echo PROBE-DONE
} >/n/host/boot/desktop-probe >[2=1] &
`;
    await route.fulfill({ response, body: body.replace("\n", "\n" + probe) });
});

try {
    await page.goto(url, { waitUntil: "domcontentloaded", timeout: 60000 });
    const deadline = Date.now() + 180000;
    let output = "";
    while (Date.now() < deadline) {
        output = await page.evaluate(async () => {
            try {
                const root = document.getElementById("host")?._kernel?.root;
                return root ? new TextDecoder().decode(await root.readFile("boot/desktop-probe")) : "";
            } catch { return ""; }
        });
        if (output.includes("PROBE-DONE")) break;
        await page.waitForTimeout(500);
    }
    assert.match(output, /PROBE-DONE/, `guest probe timed out:\n${output}`);
    const guestEpoch = Number(output.match(/GUEST-EPOCH (\d+)/)?.[1]);
    assert.ok(Number.isFinite(guestEpoch));
    const token = String(Date.now());
    const pingStarted = Date.now();
    await page.evaluate(async value => {
        const root = document.getElementById("host")._kernel.root;
        await root.writeFile("boot/time-ping", new TextEncoder().encode(`${value}\n`));
    }, token);
    let pong = "";
    const pingDeadline = Date.now() + 30000;
    while (Date.now() < pingDeadline) {
        pong = await page.evaluate(async () => {
            try {
                return new TextDecoder().decode(await document.getElementById("host")._kernel.root.readFile("boot/time-pong"));
            } catch { return ""; }
        });
        if (pong.startsWith(`${token} `)) break;
        await page.waitForTimeout(250);
    }
    const pingEnded = Date.now();
    const pingEpoch = Number(pong.match(new RegExp(`^${token} (\\d+)`))?.[1]);
    assert.ok(Number.isFinite(pingEpoch) && pingEpoch >= pingStarted / 1000 - 2 &&
        pingEpoch <= pingEnded / 1000 + 2,
        `guest clock outside browser request/response interval: ${pong}, ${pingStarted}-${pingEnded}`);
    const browserZone = new Intl.DateTimeFormat("en-US", { timeZone: zone })
        .resolvedOptions().timeZone;
    assert.ok(output.includes(browserZone),
        `missing browser timezone ${zone}:\n${output}`);
    assert.match(output, /TIMEZONE\nhalatha\n/);
    assert.ok(output.includes(plan9Timezone(zone).text.split("\n")[0]),
        `guest did not adopt browser timezone:\n${output}`);
    assert.match(output, /halatha desktop startup/);
    assert.match(output, /catclock/);
    assert.match(output, /\bbar\b/);
    assert.match(output, /\bfgui\b/);
    assert.match(output, /o9c ready/);
    assert.doesNotMatch(output, /directory entry not found/);
    assert.deepEqual(pageErrors, []);
    console.log("9FRONT-DESKTOP PASS", zone, "catclock, bar, fgui, o9c build; clock ping", ((pingEnded - pingStarted) / 1000).toFixed(1), "s");
} catch (error) {
    console.error("Desktop probe:", await page.evaluate(async () => {
        try { return new TextDecoder().decode(await document.getElementById("host")._kernel.root.readFile("boot/desktop-probe")); }
        catch { return ""; }
    }));
    console.error("Page errors:", pageErrors);
    throw error;
} finally {
    await browser.close();
}
