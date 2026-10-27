// Verify the deployment supplied babyfs endpoint reaches 9front and the
// Objective-9C request client is ready inside the guest.
// node dev/test-9front-keyrequest-e2e.mjs [page URL]
import assert from "node:assert/strict";
import { chromium } from "playwright";

const url = process.argv[2] ||
    "http://127.0.0.1:7070/examples/basic-v86-9front-direct.html?size=2147483648";
const browser = await chromium.launch({ args: ["--no-sandbox"] });
const context = await browser.newContext();
const page = await context.newPage();
const errors = [];
page.on("pageerror", error => errors.push(error.message));
// The local smoke test supplies a harmless endpoint because a checkout has
// no deployment config. A live run must read the VPS's actual config.
if (["localhost", "127.0.0.1", "::1"].includes(new URL(url).hostname)) {
    await page.route("**/config/babyfs-endpoint", route =>
        route.fulfill({ status: 200, contentType: "text/plain", body: "tcp![::1]!17026\n" }));
}
async function state() {
    return page.evaluate(async () => {
        let root;
        try { root = document.getElementById("host")?._kernel?.root; }
        catch { return { mount: "", config: "", status: "", log: "" }; }
        if (!root) return { mount: "", config: "", status: "", log: "" };
        const read = async path => {
            try { return new TextDecoder().decode(await root.readFile(path)); }
            catch { return ""; }
        };
        return {
            mount: await read("boot/host9p"),
            config: await read("boot/babyfs-config-status"),
            status: await read("boot/keyrequest-status"),
            log: await read("boot/keyrequest-e2e-log"),
        };
    });
}

try {
    await page.goto(url, { waitUntil: "domcontentloaded" });
    const deadline = Date.now() + 120000;
    let result;
    do {
        result = await state();
        if (/^(OK|FAIL) keyrequest/.test(result.status)) break;
        await page.waitForTimeout(500);
    } while (Date.now() < deadline);
    assert.match(result.mount, /mounted #9\/0/);
    assert.equal(result.config, "ready\n");
    assert.match(result.status, /^OK keyrequest 386 (prebuilt|build)/,
        `guest status: ${result.status}\n${result.log}`);
    assert.deepEqual(errors, []);
    console.log(`9FRONT-KEYREQUEST PASS ${result.status.trim()}, deployment configured`);
} finally {
    if (errors.length) console.log(`page errors: ${errors.join(" | ")}`);
    await browser.close();
}
