// Drive the live Halatha page in Chromium and capture the 9P conversation.
//
//   node dev/boot-probe.mjs [url] [seconds]
//
// Boots with ?debug9p=1 so every frame is logged as
// "9P -> host (NB type T tag N)" / "9P <- host (...)", then reports which
// requests never got a reply. That is the difference between "the write never
// left the guest" and "the reply never came back".
import { chromium } from "playwright";

const url = process.argv[2] || "https://halatha.rentonsoftworks.com/?debug9p=1";
const seconds = Number(process.argv[3] || 120);

const out = [];
const reqs = new Map();   // tag -> [{type, bytes, t}]
const reps = new Map();   // tag -> count
let tty = [];

const browser = await chromium.launch({
  args: ["--enable-features=SharedArrayBuffer", "--no-sandbox"],
});
const ctx = await browser.newContext();
const page = await ctx.newPage();

page.on("console", (m) => {
  const t = m.text();
  out.push(t);
  let g;
  if ((g = t.match(/9P -> host \((\d+)B type (\d+) tag (\d+)\)/))) {
    const [, bytes, type, tag] = g;
    if (!reqs.has(tag)) reqs.set(tag, []);
    reqs.get(tag).push({ type: +type, bytes: +bytes, t: Date.now() });
  } else if ((g = t.match(/9P <- host \((\d+)B type (\d+) tag (\d+)\)/))) {
    const [, , , tag] = g;
    reps.set(tag, (reps.get(tag) || 0) + 1);
  }
});
page.on("pageerror", (e) => out.push(`PAGEERROR: ${e.message}`));

console.log(`loading ${url}`);
await page.goto(url, { waitUntil: "domcontentloaded", timeout: 60000 });

const start = Date.now();
while ((Date.now() - start) / 1000 < seconds) {
  await page.waitForTimeout(5000);
  const el = Date.now() - start;
  const nreq = [...reqs.values()].reduce((a, b) => a + b.length, 0);
  const nrep = [...reps.values()].reduce((a, b) => a + b, 0);
  console.log(`  ${(el / 1000).toFixed(0)}s  9P req=${nreq} rep=${nrep}`);
}

// Pull the guest console text off the page if it exposed it.
try {
  tty = await page.evaluate(() => {
    const e = document.querySelector("#boot-console, #console, pre");
    return e ? e.innerText.split("\n").slice(-40) : [];
  });
} catch {}

console.log("\n=== 9P TAG LEDGER (requests without a matching reply) ===");
let unanswered = 0;
for (const [tag, list] of reqs) {
  const got = reps.get(tag) || 0;
  if (got < list.length) {
    unanswered += list.length - got;
    console.log(`  tag ${tag}: sent ${list.length} types=[${list.map((r) => r.type).join(",")}] replies ${got}  <-- ${list.length - got} UNANSWERED`);
  }
}
if (!unanswered) console.log("  (none - every request got a reply)");

console.log("\n=== tag reuse (collision risk in the page relay) ===");
let collisions = 0;
for (const [tag, list] of reqs) {
  if (list.length > 1) {
    collisions++;
    if (collisions <= 10) console.log(`  tag ${tag} reused ${list.length}x types=[${list.map((r) => r.type).join(",")}]`);
  }
}
console.log(`  tags reused: ${collisions}`);

if (tty.length) {
  console.log("\n=== guest console tail ===");
  console.log(tty.join("\n"));
}

const errs = out.filter((l) => /error|fail|PAGEERROR|REJECT/i.test(l));
if (errs.length) {
  console.log("\n=== errors ===");
  console.log(errs.slice(0, 25).join("\n"));
}

await browser.close();
