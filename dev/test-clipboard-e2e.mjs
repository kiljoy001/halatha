// Real browser -> 9P -> Rio snarf -> browser regression test.
// node dev/test-clipboard-e2e.mjs [url] [--local] [--stall-disk-once]
// --local substitutes the checkout's page, v86 loader, and boot scripts in
// this browser only. The deployed server and its disk images are never modified.
// --stall-disk-once holds the first disk range long enough to verify that
// v86 retries before the guest's ATA read times out.
// Requires Playwright and its Chromium browser.
import { chromium } from "playwright";
import { readFile } from "node:fs/promises";
import assert from "node:assert/strict";

const local = process.argv.includes("--local");
const stallDiskOnce = process.argv.includes("--stall-disk-once");
const pageFile = process.argv.find(arg => arg.startsWith("--page="))?.slice(7);
const wasmFile = process.argv.find(arg => arg.startsWith("--wasm="))?.slice(7);
const url = process.argv.slice(2).find(arg => !arg.startsWith("--")) ||
    "https://halatha.rentonsoftworks.com/";
const browser = await chromium.launch({ args: ["--no-sandbox"] });
const context = await browser.newContext({
    permissions: ["clipboard-read", "clipboard-write"],
    viewport: { width: 1280, height: 800 },
});
const page = await context.newPage();
page.on("pageerror", e => console.error("Page error:", e.message));
const browserErrors = [];
const diskTimeouts = [];
page.on("console", message => {
    if (message.type() === "error") browserErrors.push(message.text());
    if (message.text().includes("timed out; retrying")) diskTimeouts.push(message.text());
});
let stalledRange = "", retriedRange = false;
if (stallDiskOnce) {
    await page.route("**/local/9front-halatha-v9p*.img", async route => {
        const range = route.request().headers().range;
        if (range && !stalledRange) {
            stalledRange = range;
            await new Promise(resolve => setTimeout(resolve, 15000));
            await route.continue().catch(() => {}); // timed out request was canceled
            return;
        }
        if (range === stalledRange) retriedRange = true;
        await route.continue();
    });
}

// This independent test process gets a real Rio window namespace, just as
// an application does. It never invokes clip to read or change snarf.
const guestProbe = `
{
    rfork ne
    mkdir -p /mnt/cliptest
    attached=()
    while(~ $#attached 0){
        for(s in /srv/rio.*){
            if(~ $#attached 0){
                if(mount $s /mnt/cliptest 'new -hide -dx 200 -dy 150' >[2]/dev/null)
                    attached=1
            }
        }
        if(~ $#attached 0)
            sleep 1
    }
    bind -b /mnt/cliptest /dev
    echo ready >/n/host/clip/test-ready
    while(sleep 1){
        if(test -e /n/host/clip/test-command-ready){
            rm /n/host/clip/test-command-ready
            rc /n/host/clip/test-command >/n/host/clip/test-output >[2=1]
            echo done >/n/host/clip/test-done
        }
        { echo BEGIN; cat /dev/snarf; echo END } >/n/host/clip/test-observed
    }
} &
`;

await page.route("**/extras/9front/halatha.local", async route => {
    const response = await route.fetch();
    const body = local
        ? await readFile(new URL("../extras/9front/halatha.local", import.meta.url), "utf8")
        : await response.text();
    // Start the Rio probe before optional hostbin setup, which may wait for
    // the remote service when this page is served from the VPS.
    await route.fulfill({ response, body: body.replace("\n", "\n" + guestProbe) });
});
if (local) {
    await page.route("**/v86/lib/libv86.mjs*", async route => {
        await route.fulfill({ contentType: "text/javascript", body: await readFile(
            new URL("../v86/lib/libv86.mjs", import.meta.url), "utf8") });
    });
    await page.route("**/extras/9front/clip.rc", async route => {
        let body = await readFile(new URL("../extras/9front/clip.rc", import.meta.url), "utf8");
        if (process.argv.includes("--trace")) body = body.replace("rc $0 run", "rc -x $0 run");
        await route.fulfill({ body });
    });
    await page.route(url, async route => {
        const response = await route.fetch();
        await route.fulfill({ response, body: await readFile(
            pageFile || new URL("../examples/basic-v86-9front-direct.html", import.meta.url), "utf8") });
    });
}
if (wasmFile) {
    await page.route("**/dist/wanix*.wasm", route => {
        console.log("Using rebuilt runtime for", route.request().url());
        return route.fulfill({ path: wasmFile, contentType: "application/wasm" });
    });
}

async function read(path) {
    return page.evaluate(async path => {
        try {
            return await Promise.race([
                window.hostRoot.readFile(path).then(bytes => new TextDecoder().decode(bytes)),
                new Promise(resolve => setTimeout(() => resolve(null), 3000)),
            ]);
        } catch { return null; }
    }, path);
}
async function write(path, text) {
    await page.evaluate(async ({ path, text }) => {
        await window.hostRoot.writeFile(path, new TextEncoder().encode(text));
    }, { path, text });
}
async function waitFor(label, check, timeout = 20000) {
    const deadline = Date.now() + timeout;
    while (Date.now() < deadline) {
        if (await check()) return;
        await page.waitForTimeout(250);
    }
    throw new Error("Timed out: " + label);
}
async function guest(command) {
    await write("clip/test-done", "");
    await write("clip/test-command", command + "\n");
    await write("clip/test-command-ready", "run");
    await waitFor("guest command: " + command, async () => await read("clip/test-done") === "done\n");
    const output = await read("clip/test-output");
    assert.equal(output, "", "guest command failed: " + output);
}
async function clipboard() {
    return page.evaluate(() => navigator.clipboard.readText());
}
async function observed(text) {
    return await read("clip/test-observed") === "BEGIN\n" + text + "END\n";
}

try {
    console.log("Booting", url, local ? "with local clipboard changes" : "as deployed");
    await page.goto(url, { waitUntil: "domcontentloaded", timeout: 60000 });
    await waitFor("Rio test window", async () => await read("clip/test-ready") === "ready\n", 150000);
    console.log("Real Rio snarf endpoint ready");
    const retries = Array.from({ length: 30 }, (_, i) => i + 1).join(" ");
    await guest(`{ staged=(); for(i in ${retries}) { if(~ $#staged 0) { if(cmp -s /n/host/boot/ygg-up.rc /rc/bin/ygg-up) staged=1; if(~ $#staged 0) sleep 1 } }; if(~ $#staged 0) echo ygg-up-stage-mismatch }`);
    console.log("PASS current ygg-up staged in guest");
    if (stallDiskOnce) {
        assert.ok(stalledRange && retriedRange && diskTimeouts.length,
            `disk range was not retried after timeout: ${JSON.stringify({ stalledRange, retriedRange, diskTimeouts })}`);
        console.log("PASS stalled disk range retried before Rio startup");
    }
    if (process.argv.includes("--compare-probe")) {
        await guest("{ echo shorter >/tmp/cmp-full; cp /dev/null /tmp/cmp-empty; for(c in /bin/cmp /n/host/bin/cmp){ echo $c; $c -s /tmp/cmp-empty /tmp/cmp-full; echo $status; $c -s /tmp/cmp-full /tmp/cmp-empty; echo $status }; echo SNAPSHOTS; cat /tmp/cmp-full; cat /tmp/cmp-empty } >/n/host/clip/test-compare");
        console.log("Compare probe:", await read("clip/test-compare"));
        process.exitCode = 0;
    }

    for (const text of ["host clipboard — café\nsecond line", "λ".repeat(12000), ""]) {
        await page.evaluate(text => navigator.clipboard.writeText(text), text);
        await page.locator("#btn-paste-guest").click();
        await waitFor("browser clipboard reaches Rio (" + text.length + " chars)",
            async () => await observed(text));
        console.log("PASS browser -> actual Rio snarf:", text.length, "chars");
    }

    for (const text of ["Rio snarf — 日本語\nsecond line", "界".repeat(9000), "shorter", ""]) {
        await write("clip/test-input", text);
        await guest("cat /n/host/clip/test-input >>/dev/snarf");
        await waitFor("Rio reaches host file", async () => await read("clip/text") === text);
        await page.locator("#btn-copy-guest").click();
        await waitFor("Rio reaches browser clipboard", async () => await clipboard() === text);
        console.log("PASS actual Rio snarf -> browser:", text.length, "chars");
    }

    await guest("rc /rc/bin/clip sync");
    await write("clip/test-input", "after duplicate sync");
    await guest("cat /n/host/clip/test-input >>/dev/snarf");
    await waitFor("sync remains functional", async () => await read("clip/text") === "after duplicate sync");
    console.log("PASS repeated sync startup");

    await guest("cat /tmp/clip-sync/pid >/n/host/clip/test-worker");
    const worker = (await read("clip/test-worker")).trim();
    assert.match(worker, /^\d+$/);
    await guest("echo kill >/proc/" + worker + "/ctl");
    await guest("rc /rc/bin/clip sync");
    await page.evaluate(() => navigator.clipboard.writeText("restart handshake"));
    await page.locator("#btn-paste-guest").click();
    await waitFor("restarted worker imports host text",
        async () => await observed("restart handshake"));
    await write("clip/test-input", "after worker restart");
    await guest("cat /n/host/clip/test-input >>/dev/snarf");
    await waitFor("restarted sync", async () => await read("clip/text") === "after worker restart");
    console.log("PASS sync restart after worker termination");
} catch (error) {
    console.error("Browser errors:", browserErrors.slice(-10));
    await page.screenshot({ path: "/tmp/halatha-clipboard-e2e-failure.png" }).catch(() => {});
    await guest("cat /tmp/clip.log >/n/host/clip/test-log").catch(() => {});
    await guest("{ ls -l /tmp/clip-sync/*; echo BEGIN; cat /dev/snarf; echo END } >/n/host/clip/test-diagnostic").catch(() => {});
    await guest("{ /n/host/bin/cmp /tmp/clip-sync/guest /tmp/clip-sync/last; echo $status; cat /dev/null >/n/host/clip/test-truncate; ls -l /n/host/clip/text } >/n/host/clip/test-compare").catch(() => {});
    console.error("Worker log:", (await read("clip/test-log"))?.slice(-6000));
    console.error("Guest diagnostic:", (await read("clip/test-diagnostic"))?.slice(0, 500));
    console.error("Compare diagnostic:", await read("clip/test-compare"));
    console.error("Observed:", JSON.stringify((await read("clip/test-observed"))?.slice(0, 120)));
    console.error("Host:", JSON.stringify((await read("clip/text"))?.slice(0, 120)));
    throw error;
} finally {
    await browser.close();
}
