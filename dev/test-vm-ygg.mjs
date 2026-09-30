// Live browser/guest boot probe. Reads only the guest's RAM-backed health
// reports; it never submits or approves a key.
import { chromium } from "playwright";

const seconds = Number(process.argv[2] || 150);
const url = process.argv[3] || "https://halatha.rentonsoftworks.com/";
const browser = await chromium.launch({ args: ["--no-sandbox"] });
try {
  const page = await (await browser.newContext()).newPage();
  page.on("console", (message) => {
    if (/seed |ensureTree:|namespace services:/.test(message.text()))
      console.log(`browser ${message.type()}: ${message.text()}`);
  });
  page.on("pageerror", (error) => console.log(`browser pageerror: ${error.message}`));
  await page.goto(url, {
    waitUntil: "domcontentloaded", timeout: 60000,
  });
  await page.waitForTimeout(seconds * 1000);
  const result = await page.evaluate(async () => {
    const root = window.hostRoot;
    const decoder = new TextDecoder();
    const read = async (path) => {
      try { return decoder.decode(await root.readFile(path)); }
      catch (error) { return `unavailable: ${error.message}`; }
    };
    return {
      status: document.querySelector("#status")?.textContent,
      guestBoot: await read("boot/guest.log"),
      health: await read("boot/health"),
      ygg: await read("boot/ygg-health"),
      keyrequest: await read("boot/keyrequest-status"),
      bin: await read("bin/STATUS"),
      seedReady: await read("boot/seed-ready"),
      launcherSource: (await read("boot/keyrequest.rc")).slice(0, 120),
      clientSource: (await read("boot/keyrequest-client.o9")).slice(0, 120),
      bootReadme: (await read("boot/README")).slice(0, 120),
      bootHook: (await read("boot/halatha.local")).slice(0, 120),
      bootEntries: await root.readDir("boot"),
    };
  });
  const lines = result.ygg.split("\n");
  result.ygg = lines.filter((line) =>
    /^(OK|FAIL|Guest Yggdrasil|srv:|ygg-up:|ygg-peers:|\d{4}\/\d\d\/\d\d)/.test(line) ||
    /23\.184\.48\.86|Routing table size|Public key:|context deadline|handshake|error/i.test(line)
  ).slice(0, 80).join("\n");
  console.log(JSON.stringify(result, null, 2));
} finally {
  await browser.close();
}
