// Boot a browser guest and collect read-only startup and Yggdrasil diagnostics.
// Usage: node dev/diagnose-vm-ygg.mjs [page URL] [wait seconds] [peer IPv6] [HTTP probe URL]
import { chromium } from "playwright";
import { readFile } from "node:fs/promises";

const url = process.argv[2] || "https://halatha.rentonsoftworks.com/";
const waitSeconds = Number(process.argv[3] || 130);
const peer = process.argv[4] || "";
const probeUrl = process.argv[5] || "";
if (Boolean(peer) !== Boolean(probeUrl) ||
    (peer && !/^[0-9a-fA-F:]+$/.test(peer)) ||
    (probeUrl && !/^https?:\/\/\[[0-9a-fA-F:]+\]:\d+\/[-A-Za-z0-9./_]+$/.test(probeUrl))) {
    throw new Error("provide both a peer IPv6 address and a simple HTTP probe URL");
}
const browser = await chromium.launch({ args: ["--no-sandbox"] });
const page = await (await browser.newContext()).newPage();
const pageErrors = [];
let interceptedHook = false;
page.on("pageerror", error => pageErrors.push(error.message));
page.route("**/extras/9front/hostbin.rc", async route => {
    await route.fulfill({
        response: await route.fetch(),
        body: await readFile(new URL("../extras/9front/hostbin.rc", import.meta.url), "utf8"),
    });
});

page.route("**/extras/9front/halatha.local", async route => {
    interceptedHook = true;
    const response = await route.fetch();
    let body = await readFile(new URL("../extras/9front/halatha.local", import.meta.url), "utf8");
    body = body.replace("\n", "\necho hook-start >/n/host/boot/ygg-stage\n");
    body = body.replace("\t/rc/bin/hostbin >>$LOG >[2=1]",
        "\techo before-hostbin >>/n/host/boot/ygg-stage\n" +
        "\tcat /net/ipselftab >/n/host/boot/ipself-before\n" +
        "\t/rc/bin/hostbin >>$LOG >[2=1]\n" +
        "\techo after-hostbin >>/n/host/boot/ygg-stage\n" +
        "\tcat /net/ipselftab >/n/host/boot/ipself-after\n" +
        "\tcat /tmp/hostbin.log >/n/host/boot/hostbin-debug");
    body = body.replace("if(test -x /rc/bin/ygg-up){",
        "echo before-ygg-block >>/n/host/boot/ygg-stage\n" +
        "if(test -x /rc/bin/ygg-up){");
    body = body.replace("\tylog 'halatha.local: started ygg-up'",
        "\tylog 'halatha.local: started ygg-up'\n" +
        "\techo spawned-ygg-up >>/n/host/boot/ygg-stage\n" +
        "\t{ sleep 30; cat /tmp/ygg-start.log >/n/host/boot/ygg-start-debug; cat /tmp/yggdrasil.log >/n/host/boot/ygg-daemon-debug } &");
    if (peer) {
        body = body.replace("\t\t\t\techo refresh >/net/cs >[2]/dev/null",
            "\t\t\t\techo refresh >/net/cs >[2]/dev/null\n" +
            `\t\t\t\t{ echo 'ping peer'; ip/ping -6 -n 3 ${peer}; echo 'tcp fetch'; hget '${probeUrl}' } >/n/host/boot/ygg-data-plane >[2=1] &`);
    }
    await route.fulfill({ response, body });
});

try {
    await page.goto(url, { waitUntil: "domcontentloaded", timeout: 60000 });
    await page.waitForTimeout(waitSeconds * 1000);
    const result = await page.evaluate(async () => {
        const root = window.hostRoot;
        if (!root) return { error: "browser host namespace unavailable" };
        const decoder = new TextDecoder();
        const read = async path => {
            try { return decoder.decode(await root.readFile(path)); }
            catch (error) { return `unavailable: ${error.message}`; }
        };
        return {
            status: document.querySelector("#status")?.textContent,
            stage: await read("boot/ygg-stage"),
            hostbinDebug: await read("boot/hostbin-debug"),
            ipselfBefore: await read("boot/ipself-before"),
            ipselfAfter: await read("boot/ipself-after"),
            yggStartDebug: await read("boot/ygg-start-debug"),
            yggDaemonDebug: (await read("boot/ygg-daemon-debug")).slice(-6000),
            health: await read("boot/ygg-health"),
            dataPlane: await read("boot/ygg-data-plane"),
            hookTop: (await read("boot/halatha.local")).slice(0, 320),
            host9p: await read("boot/host9p"),
            hostbinOverride: (await read("boot/hostbin.rc")).includes("requesting guest DHCP address"),
            guestLog: await read("boot/guest.log"),
            statusFile: await read("boot/status"),
            serial: (await read("boot/serial.log")).slice(-12000),
            bootProbe: (await read("boot/probe.log")).slice(-1200),
            bootEntries: await root.readDir("boot"),
        };
    });
    console.log(JSON.stringify({ ...result, interceptedHook, pageErrors }, null, 2));
} finally {
    await browser.close();
}
