// 9front over virtio-9p: the guest's #9 device must find v86's virtio-9p
// device and termrc.local must mount the host namespace through it.
// node dev/test-9front-virtio9p-e2e.mjs [url] [--timeout=seconds]
// Passes when the kernel prints its "#9/0: virtio9p" probe line and the
// guest writes boot/host9p into the host namespace, which it can only do
// through the #9 mount. Read-only: the test never writes to the server.
// Build the image with extras/9front/kernel/build-kernel.py and
// extras/9front/pipeline/mk9frontimg.py.
// Requires Playwright and its Chromium browser.
import { chromium } from "playwright";

const args = process.argv.slice(2);
const url = args.find(arg => !arg.startsWith("--")) ||
    "http://127.0.0.1:7070/examples/basic-v86-9front-direct.html";
const timeout = Number(args.find(arg => arg.startsWith("--timeout="))?.slice(10) || 200) * 1000;

const browser = await chromium.launch({ args: ["--no-sandbox"] });
const page = await (await browser.newContext()).newPage();
const pageErrors = [];
page.on("pageerror", e => pageErrors.push(e.message));
const started = Date.now();
const elapsed = () => ((Date.now() - started) / 1000).toFixed(0) + "s";

await page.goto(url, { waitUntil: "domcontentloaded" });
let probeLine = null, probeAt = null, marker = null;
while (Date.now() - started < timeout && !(probeLine && marker)) {
    const r = await page.evaluate(async () => {
        const screen = window.emulator?.screen_adapter?.get_text_screen?.() || [];
        let root = null, marker = null;
        try { root = document.getElementById("host")?._kernel?.root; } catch {} // wasm not loaded yet
        if (root) {
            try { marker = new TextDecoder().decode(await root.readFile("boot/host9p")); } catch {}
        }
        return { screen, marker };
    });
    if (!probeLine) {
        probeLine = r.screen.map(l => l.trim()).find(l => l.startsWith("#9/0: virtio9p")) ?? null;
        if (probeLine) probeAt = elapsed();
    }
    if (r.marker && !marker) marker = { text: r.marker.trim(), at: elapsed() };
    await page.waitForTimeout(1000);
}
await browser.close();

console.log(`kernel probe: ${probeLine ? `${probeLine} (${probeAt})` : "not seen"}`);
console.log(`guest marker: ${marker ? `"${marker.text}" (${marker.at})` : "not written"}`);
for (const e of pageErrors) console.log("page error:", e);
const pass = probeLine && marker && pageErrors.length === 0;
console.log(`9FRONT-VIRTIO9P ${pass ? "PASS" : "FAIL"} ${url} (${elapsed()})`);
process.exit(pass ? 0 : 1);
