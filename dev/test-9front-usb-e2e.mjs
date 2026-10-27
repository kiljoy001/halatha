// Browser service -> virtio-9p -> 9front usbns -> usbd, with a synthetic
// WebUSB device. No physical device or browser permission prompt is needed.
// node dev/test-9front-usb-e2e.mjs [page URL]
import assert from "node:assert/strict";
import { chromium } from "playwright";

const url = process.argv[2] ||
    "http://127.0.0.1:7070/examples/basic-v86-9front-direct.html";
const browser = await chromium.launch({ args: ["--no-sandbox"] });
const context = await browser.newContext();
await context.addInitScript(() => {
    const device = {
        vendorId: 0x1234,
        productId: 0x5678,
        manufacturerName: "Halatha",
        productName: "USB probe",
        opened: false,
        configuration: { configurationValue: 1, interfaces: [] },
        configurations: [],
        async open() { this.opened = true; },
        async close() { this.opened = false; },
        async controlTransferIn(setup, length) {
            window.usbRequests.push({ setup, length });
            const type = setup.value >> 8;
            const data = type === 1
                ? [18, 1, 0x10, 1, 0, 0, 0, 64, 0x34, 0x12, 0x78, 0x56, 0, 1, 0, 0, 0, 1]
                : type === 2
                    ? [9, 2, 9, 0, 0, 1, 0, 0x80, 50]
                    : [4, 3, 0x09, 0x04];
            const bytes = Uint8Array.from(data.slice(0, length));
            return { status: "ok", data: new DataView(bytes.buffer) };
        },
        async controlTransferOut(setup, data) {
            window.usbRequests.push({ setup, length: data.byteLength });
            return { status: "ok" };
        },
        async transferIn() { return { status: "ok", data: new DataView(new ArrayBuffer(0)) }; },
        async transferOut() { return { status: "ok", bytesWritten: 0 }; },
        async claimInterface() {},
        async releaseInterface() {},
        async reset() {},
    };
    window.usbRequests = [];
    Object.defineProperty(navigator, "usb", {
        configurable: true,
        value: { requestDevice: async () => device },
    });
});
const page = await context.newPage();
const errors = [];
page.on("pageerror", error => errors.push(error.message));
const browserMessages = [];
page.on("console", message => {
    if (["warning", "error"].includes(message.type())) browserMessages.push(message.text());
});
await page.route("**/extras/9front/halatha.local", async route => {
    const response = await route.fetch();
    const body = await response.text();
    // Run probes as soon as the current hook starts. The hostbin mount later
    // in the hook can legitimately take longer over the deployed WISP relay.
    const probe = `
{
    echo USB-DIAGNOSTIC
    ls /dev/usb
    cat /dev/usb/ctl
    ls /srv/usb
    ps | grep usbd
    ls -l /386/bin/halctl /386/bin/o9c /386/bin/yggdrasil /386/bin/yggdrasilctl
} >/n/host/boot/usb-diagnostic >[2=1] &
/386/bin/halctl status >/n/host/boot/halctl-status >[2=1] &
{
    echo TERMRC
    sed -n '90,115p' /rc/bin/termrc
    echo LOCAL
    cat /rc/bin/termrc.local
} >/n/host/boot/usb-termrc >[2=1] &
cat /usr/glenda/lib/profile >/n/host/boot/usb-profile &
{
    while(sleep 2)
        cat /dev/kmesg | tail -40 >/n/host/boot/usb-kernel-diag
} &
`;
    await route.fulfill({ response, body: body.replace("\n", "\n" + probe) });
});

async function state(detail = false) {
    return page.evaluate(async detail => {
        let root = null;
        try { root = document.getElementById("host")?._kernel?.root; }
        catch { /* Wasm has not finished loading yet. */ }
        let mounted = "", ctl = "", devices = "", diagnostic = "", req = "", resp = "", kernelDiag = "", halctlStatus = "", termrc = "", profile = "";
        let seedReady = "", hookBytes = 0, bootStatus = "", keyrequestStatus = "", guestLog = "", serial = "";
        if (root) {
            const read = async path => {
                try { return new TextDecoder().decode(await root.readFile(path)); }
                catch { return ""; }
            };
            mounted = await read("boot/host9p");
            ctl = await read("usb/ctl");
            devices = await read("usb/devices");
            diagnostic = await read("boot/usb-diagnostic");
            req = await read("usb/0/req");
            resp = await read("usb/0/resp");
            kernelDiag = await read("boot/usb-kernel-diag");
            halctlStatus = await read("boot/halctl-status");
            termrc = await read("boot/usb-termrc");
            profile = await read("boot/usb-profile");
            seedReady = await read("boot/seed-ready");
            hookBytes = (await read("boot/halatha.local")).length;
            bootStatus = await read("boot/status");
            keyrequestStatus = await read("boot/keyrequest-status");
            if (detail) {
                guestLog = (await read("boot/guest.log")).slice(-1000);
                serial = (await read("boot/serial.log")).slice(-1000);
            }
        }
        return { mounted, ctl, devices, diagnostic, req, resp, kernelDiag, halctlStatus, termrc, profile,
            seedReady, hookBytes, bootStatus, keyrequestStatus, guestLog, serial, requests: window.usbRequests || [] };
    }, detail);
}

async function waitFor(label, condition, timeout = 120000) {
    const deadline = Date.now() + timeout;
    while (Date.now() < deadline) {
        const value = await state();
        if (condition(value)) return value;
        await page.waitForTimeout(500);
    }
    throw new Error(`timed out: ${label}; state ${JSON.stringify(await state(true))}`);
}

try {
    await page.goto(url, { waitUntil: "domcontentloaded" });
    await waitFor("9front host mount", value => value.mounted.includes("mounted #9/0"));
    await waitFor("WebUSB service", value => value.ctl === "ready");
    await page.evaluate(async () => {
        const root = document.getElementById("host")._kernel.root;
        await root.writeFile("usb/ctl", new TextEncoder().encode("open"));
    });
    const opened = await waitFor("mock device open", value => value.ctl === "ready 0");
    assert.match(opened.devices, /0 0x1234:0x5678/);
    const transfer = await waitFor("9front USB descriptor transfer",
        value => value.requests.some(request => request.setup.request === 6), 45000);
    assert.equal(transfer.requests[0].setup.requestType, "standard");
    const guestTools = await waitFor("9front halctl status",
        value => value.halctlStatus.length > 0, 120000);
    assert.match(guestTools.halctlStatus, /halatha|host|status|ready/i);
    await page.waitForTimeout(20000);
    const settled = await state();
    assert.ok(settled.requests.some(request => request.setup.request === 9),
        `USB device was not configured: ${JSON.stringify(settled.requests)}`);
    assert.doesNotMatch(settled.termrc, /if not if\(/);
    assert.match(settled.profile, /halatha-bind/);
    assert.doesNotMatch(settled.kernelDiag, /if not.*does not follow/);
    assert.deepEqual(errors, []);
    console.log(`9FRONT-USB PASS ${settled.requests.length} guest control request(s)`);
} catch (error) {
    console.error("Page errors:", errors.slice(-10));
    console.error("Browser warnings/errors:", browserMessages.slice(-10));
    throw error;
} finally {
    await browser.close();
}
