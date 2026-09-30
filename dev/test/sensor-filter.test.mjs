// The poller-suppression logic, extracted and exercised. One test session
// logged 300,619 events of which 150,231 were /usb/devices; this keeps the
// first few so the behaviour stays visible, then stops recording them.
import test from "node:test";
import assert from "node:assert/strict";

const POLLED_PATHS = new Set(["/usb/devices"]);
const POLLED_KEEP = 5;

function makeRecorder() {
  const seen = {}, recorded = [];
  return {
    recorded,
    walk(path) {
      if (POLLED_PATHS.has(path)) {
        const n = (seen[path] || 0) + 1;
        seen[path] = n;
        if (n > POLLED_KEEP) return;
      }
      recorded.push(path);
    },
  };
}

test("polled path is capped at POLLED_KEEP", () => {
  const r = makeRecorder();
  for (let i = 0; i < 1000; i++) r.walk("/usb/devices");
  assert.equal(r.recorded.length, POLLED_KEEP,
    "the poller should stop being recorded after the first few");
});

test("real visitor activity is never suppressed", () => {
  const r = makeRecorder();
  for (let i = 0; i < 1000; i++) {
    r.walk("/usb/devices");
    r.walk("/adm/keys");
    r.walk("/n/host/clip/text");
  }
  const keys = r.recorded.filter((p) => p === "/adm/keys").length;
  const clip = r.recorded.filter((p) => p === "/n/host/clip/text").length;
  assert.equal(keys, 1000, "credential access must always be recorded");
  assert.equal(clip, 1000, "clipboard access must always be recorded");
});

test("the poller stays visible rather than vanishing", () => {
  const r = makeRecorder();
  for (let i = 0; i < 50; i++) r.walk("/usb/devices");
  assert.ok(r.recorded.includes("/usb/devices"),
    "suppressing entirely would hide that polling happens at all");
});

test("a 300k-event session collapses to something readable", () => {
  const r = makeRecorder();
  // replay the observed mix: half poller, half everything else
  for (let i = 0; i < 150231; i++) r.walk("/usb/devices");
  for (let i = 0; i < 20; i++) r.walk("/n/host/boot/guest.log");
  assert.equal(r.recorded.length, POLLED_KEEP + 20);
});
