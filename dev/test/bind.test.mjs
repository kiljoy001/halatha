// Unit tests for the pure helpers in elements/bind.js.
// Run: node --test dev/test/
//
// resolveWebSocketURL is not exported (bind.js is a browser module that
// registers a custom element on import), so the function is extracted from
// source and evaluated in isolation. That keeps the test dependency-free
// and avoids needing a DOM.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const src = readFileSync(new URL("../../elements/bind.js", import.meta.url), "utf8");

function extract(name) {
  const start = src.indexOf(`function ${name}(`);
  assert.notEqual(start, -1, `${name} not found in bind.js`);
  let depth = 0, i = src.indexOf("{", start);
  const open = i;
  for (; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}") { depth--; if (depth === 0) break; }
  }
  return src.slice(start, i + 1);
}

const resolveWebSocketURL = new Function(
  "document",
  `${extract("resolveWebSocketURL")}; return resolveWebSocketURL;`
)({ baseURI: "https://halatha.rentonsoftworks.com/examples/page.html" });

test("http and https are upgraded to ws and wss", () => {
  assert.equal(
    resolveWebSocketURL("https://example.com/sock"),
    "wss://example.com/sock"
  );
  assert.equal(
    resolveWebSocketURL("http://example.com/sock"),
    "ws://example.com/sock"
  );
});

test("relative paths resolve against the page and inherit wss", () => {
  // The deployed page is https, so a relative import must become wss —
  // a ws:// import from an https page is blocked as mixed content.
  assert.equal(
    resolveWebSocketURL("/.well-known/wisp"),
    "wss://halatha.rentonsoftworks.com/.well-known/wisp"
  );
  assert.equal(
    resolveWebSocketURL("sock"),
    "wss://halatha.rentonsoftworks.com/examples/sock"
  );
});

test("explicit ws and wss URLs pass through unchanged", () => {
  assert.equal(resolveWebSocketURL("wss://a.example/x"), "wss://a.example/x");
  assert.equal(resolveWebSocketURL("ws://a.example/x"), "ws://a.example/x");
});

test("query strings and ports survive the rewrite", () => {
  assert.equal(
    resolveWebSocketURL("https://a.example:8443/x?token=abc"),
    "wss://a.example:8443/x?token=abc"
  );
});

test("bind.js ends with a newline", () => {
  // A missing trailing newline broke this file before; keep it pinned.
  assert.ok(src.endsWith("\n"), "elements/bind.js must end with a newline");
});

test("websocket import rejects on error", () => {
  // A failed WebSocket used to leave the import promise pending forever,
  // hanging boot. The handler must exist.
  assert.match(src, /ws\.onerror\s*=/, "bind.js has no ws.onerror handler");
  assert.match(src, /reject\(/, "bind.js never rejects the import promise");
});
