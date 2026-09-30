// Boot the guest, wait for rio, then type a clipboard write and watch the
// 9P conversation for the resulting Twalk/Topen/Twrite/Tclunk.
//
//   node dev/clip-probe.mjs [url] [bootSeconds]
import { chromium } from "playwright";

const url = process.argv[2] || "https://halatha.rentonsoftworks.com/?debug9p=1";
const bootWait = Number(process.argv[3] || 90);

const T = {104:"Tattach",110:"Twalk",112:"Topen",114:"Tcreate",116:"Tread",
           118:"Twrite",120:"Tclunk",124:"Tstat",126:"Twstat",100:"Tversion",108:"Tflush"};

const frames = [];
let marking = false;

const browser = await chromium.launch({ args: ["--no-sandbox"] });
const page = await (await browser.newContext()).newPage();

page.on("console", (m) => {
  const t = m.text();
  let g;
  if ((g = t.match(/9P (->|<-) host \((\d+)B type (\d+) tag (\d+)\)/))) {
    const [, dir, bytes, type, tag] = g;
    const rec = { dir, type: +type, name: T[+type] || `T?${type}`, tag: +tag, t: Date.now() };
    frames.push(rec);
    if (marking) {
      console.log(`    ${dir === "->" ? "REQ" : "REP"} ${rec.name} tag=${rec.tag} ${bytes}B`);
    }
  }
});

console.log(`loading ${url}`);
await page.goto(url, { waitUntil: "domcontentloaded", timeout: 60000 });
console.log(`booting for ${bootWait}s...`);
await page.waitForTimeout(bootWait * 1000);
console.log(`boot frames: ${frames.length}`);

const canvas = await page.$("#screen_container canvas, canvas");
if (!canvas) { console.log("NO CANVAS"); await browser.close(); process.exit(1); }
await canvas.click();
await page.waitForTimeout(1000);

console.log("\n=== typing the clipboard write ===");
marking = true;
const before = frames.length;
await page.keyboard.type("echo halatha-probe > /n/host/clip/text", { delay: 60 });
await page.keyboard.press("Enter");
await page.waitForTimeout(15000);
marking = false;

const after = frames.slice(before);
console.log(`\nframes during the write: ${after.length}`);
const reqs = after.filter(f => f.dir === "->");
const reps = after.filter(f => f.dir === "<-");
console.log(`  requests=${reqs.length} replies=${reps.length}`);
console.log(`  request types: ${[...new Set(reqs.map(r=>r.name))].join(", ") || "(none)"}`);

// unanswered?
const pend = new Map();
for (const f of after) {
  if (f.dir === "->") pend.set(f.tag, (pend.get(f.tag)||0)+1);
  else pend.set(f.tag, (pend.get(f.tag)||0)-1);
}
const stuck = [...pend].filter(([,v]) => v > 0);
console.log(stuck.length ? `  UNANSWERED tags: ${JSON.stringify(stuck)}` : "  all answered");

// did the host file change?
try {
  const val = await page.evaluate(async () => {
    if (!window.hostRoot) return "(no hostRoot)";
    const b = await window.hostRoot.readFile("clip/text");
    return new TextDecoder().decode(b);
  });
  console.log(`\nclip/text now: ${JSON.stringify(val)}`);
} catch (e) { console.log(`\nclip/text read failed: ${e.message}`); }

await browser.close();
