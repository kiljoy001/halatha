// Actual Chromium -> browser namespace -> WebSocket -> TCP -> 9P file server.
// Run the local server as documented in dev/NETWORK-9P.md before this test.
import { chromium } from "playwright";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createHash } from "node:crypto";

const url = process.argv[2] || "http://127.0.0.1:7654/examples/network-9p.html";
const readPath = process.argv.find(arg => arg.startsWith("--read="))?.slice(7);
const expectedHash = process.argv.find(arg => arg.startsWith("--sha256="))?.slice(9);
const expectedText = process.argv.find(arg => arg.startsWith("--expect="))?.slice(9);
const expectFailure = process.argv.includes("--expect-failure");
if ((expectedHash || expectedText) && !readPath) throw new Error("--read is required for content assertions");
const pairingFile = process.argv.find(arg => arg.startsWith("--pairing-file="))?.slice(15);
const pairingToken = pairingFile ? (await readFile(pairingFile, "utf8")).trim() : process.env.HALATHA_TEST_MOUNT_PAIRING;
if (!pairingToken) throw new Error("Supply --pairing-file=<private code file>");
const browser = await chromium.launch({ args: ["--no-sandbox"] });
try {
    const page = await browser.newPage();
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    await page.goto(url, { waitUntil: "domcontentloaded", timeout: 30000 });
    await page.locator("#pairing").fill(pairingToken);
    await page.locator("#mount").click();
    await page.waitForFunction(() => window.networkMount && window.networkMount.state !== "connecting", { timeout: 45000 });
    const result = await page.evaluate(() => window.networkMount);
    if (expectFailure) {
        assert.equal(result.state, "failed", "unavailable server must fail instead of appearing mounted");
        console.log("PASS unavailable server reports failure:", result.error);
    } else {
        assert.equal(result.state, "mounted", JSON.stringify({ result, errors }));
        assert.ok(Array.isArray(result.entries), "listing must come through the browser filesystem API");
        assert.deepEqual(errors, [], "browser runtime errors");
        const stat = await page.evaluate(() => window.networkRoot.stat("network"));
        console.log("PASS real browser mounted network/:", JSON.stringify({ entries: result.entries, stat }));
        if (readPath) {
            assert.ok(!readPath.startsWith("/") && !readPath.split("/").includes(".."), "--read must be relative to the remote root");
            const bytes = await page.evaluate(async path => {
                const file = await window.networkRoot.readFile(`network/${path}`);
                return Array.from(file);
            }, readPath);
            const buffer = Buffer.from(bytes);
            const hash = createHash("sha256").update(buffer).digest("hex");
            if (expectedHash) assert.equal(hash, expectedHash, "remote content hash");
            if (expectedText) assert.ok(buffer.toString("utf8").includes(expectedText), "remote content must contain expected text");
            console.log("PASS browser read remote file:", JSON.stringify({ path: readPath, bytes: buffer.length, sha256: hash }));
        }
    }
} finally {
    await browser.close();
}
