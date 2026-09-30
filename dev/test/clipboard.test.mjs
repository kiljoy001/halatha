import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const source = readFileSync(new URL("../../examples/basic-v86-9legacy-direct.html", import.meta.url), "utf8");
const service = source.slice(source.indexOf("async function startNamespaceServices("),
    source.indexOf("// Browser File System Access API bridges."));

async function harness(initial = "") {
    const files = new Map([["clip/text", initial], ["fetch/ctl", ""]]);
    const events = new Map();
    const screenEvents = new Map();
    const writes = [];
    let tick, allowWrite = false, osClipboard = "old OS text";
    const window = {
        addEventListener: (name, fn) => events.set(name, fn),
    };
    const document = {
        getElementById: () => ({ addEventListener: (name, fn) => screenEvents.set(name, fn) }),
        addEventListener() {},
    };
    const bridge = new Function("window", "document", "navigator", "readText", "writeText",
        "maybeWriteClipboard", "showClipToast", "setInterval", "exposeDownloads", "runFetch", `
        let publishHostClipboard;
        ${service}
        return { start: startNamespaceServices, paste: text => publishHostClipboard(text) };
    `)(window, document, { clipboard: { readText: async () => osClipboard } },
        async (_, path) => files.get(path),
        async (_, path, text) => files.set(path, text),
        async text => {
            writes.push(text);
            if (allowWrite) osClipboard = text;
            return allowWrite;
        }, () => {}, fn => { tick = fn; }, async () => {}, async () => {});
    await bridge.start({});
    return {
        files, events, screenEvents, writes, bridge,
        tick: () => tick(),
        allow: () => { allowWrite = true; },
    };
}

test("failed gesture retry retains a guest selection instead of importing stale OS text", async () => {
    const h = await harness();
    h.files.set("clip/text", "guest selection");
    await h.tick(); // automatic write denied
    await h.screenEvents.get("pointerdown")(); // gesture write also denied
    await h.events.get("pointerdown")(); // same gesture's host read
    assert.equal(h.files.get("clip/text"), "guest selection");
    h.allow();
    await h.screenEvents.get("pointerdown")();
    assert.deepEqual(h.writes, ["guest selection", "guest selection", "guest selection"]);
});

test("an empty guest clipboard survives denied writes and retries", async () => {
    const h = await harness();
    await h.bridge.paste("previous text");
    h.files.set("clip/text", "");
    await h.tick();
    await h.screenEvents.get("mouseup")();
    h.allow();
    await h.screenEvents.get("mouseup")();
    assert.deepEqual(h.writes, ["", "", ""]);
});

test("explicit host paste cancels an older pending guest export", async () => {
    const h = await harness();
    h.files.set("clip/text", "old guest selection");
    await h.tick();
    await h.bridge.paste("new host text");
    h.allow();
    await h.screenEvents.get("mouseup")();
    await h.tick();
    assert.equal(h.files.get("clip/text"), "new host text");
    assert.deepEqual(h.writes, ["old guest selection"]);
});

test("a guest change not yet polled blocks stale host import", async () => {
    const h = await harness();
    h.files.set("clip/text", "new guest text");
    await h.events.get("focus")();
    assert.equal(h.files.get("clip/text"), "new guest text");
});

test("clearing the initial guest clipboard exports an empty string", async () => {
    const h = await harness("initial text");
    h.files.set("clip/text", "");
    await h.tick();
    assert.deepEqual(h.writes, [""]);
});
