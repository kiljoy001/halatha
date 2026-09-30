// Release smoke test: the page boots 9front from the release's own files.
// node dev/test-release-e2e.mjs [url] [--timeout=seconds] [--ignore-https-errors]
// Passes when every same-origin request succeeds, the page throws nothing,
// the emulator loads, and 9front's own boot prompt reaches the VGA text
// screen, which proves the served disk image really boots. Read-only: the
// test never writes to the server.
// Requires Playwright and its Chromium browser.
import { chromium } from "playwright";

const args = process.argv.slice(2);
const url = args.find(arg => !arg.startsWith("--")) || "https://halatha.rentonsoftworks.com/";
const timeout = Number(args.find(arg => arg.startsWith("--timeout="))?.slice(10) || 240) * 1000;
const origin = new URL(url).origin;

// 9front asks for its root before anything else; any of these on the text
// screen means the kernel loaded from the served image and is running.
const bootPrompt = /bootargs|root is from|user\[[a-z]+\]|term%/;

const browser = await chromium.launch({ args: ["--no-sandbox"] });
const context = await browser.newContext({
    viewport: { width: 1280, height: 800 },
    ignoreHTTPSErrors: args.includes("--ignore-https-errors"),
});
const page = await context.newPage();
const failures = [];
const pageErrors = [];
let bytes = 0;
page.on("pageerror", e => pageErrors.push(e.message));
page.on("requestfailed", req => {
    if (req.url().startsWith(origin)) failures.push(`${req.failure()?.errorText} ${req.url()}`);
});
page.on("response", res => {
    if (!res.url().startsWith(origin)) return;
    if (res.status() >= 400) failures.push(`${res.status()} ${res.url()}`);
    const len = Number(res.headers()["content-length"] || 0);
    if (len) bytes += len;
});

const started = Date.now();
const elapsed = () => ((Date.now() - started) / 1000).toFixed(1) + "s";
let verdict = "FAIL";
let screen = [];
try {
    await page.goto(url, { waitUntil: "load" });
    await page.waitForFunction(
        () => /9front( state)? loaded/.test(document.getElementById("status")?.textContent || ""),
        null, { timeout });
    console.log(`emulator loaded at ${elapsed()}`);
    const deadline = started + timeout;
    while (Date.now() < deadline) {
        screen = await page.evaluate(() => window.emulator?.screen_adapter?.get_text_screen?.() || []);
        if (bootPrompt.test(screen.join("\n"))) {
            verdict = "PASS";
            break;
        }
        await page.waitForTimeout(1000);
    }
    if (verdict === "PASS") console.log(`9front boot prompt at ${elapsed()}`);
} catch (e) {
    console.log("error:", e.message.split("\n")[0]);
}
if (failures.length) verdict = "FAIL";
if (pageErrors.length) verdict = "FAIL";

console.log(`status: ${await page.evaluate(() => document.getElementById("status")?.textContent).catch(() => "?")}`);
console.log(`same-origin bytes (content-length): ${(bytes / 1048576).toFixed(1)} MiB`);
for (const f of failures) console.log("request failed:", f);
for (const e of pageErrors) console.log("page error:", e);
console.log("screen:\n" + screen.map(l => l.trimEnd()).filter(Boolean).slice(-12).join("\n"));
console.log(`RELEASE-E2E ${verdict} ${url} (${elapsed()})`);
await browser.close();
process.exit(verdict === "PASS" ? 0 : 1);
