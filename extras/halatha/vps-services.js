// Browser services for a VPS session namespace.
//
// The session root has a host/ tree (clip, downloads, fetch, net, uploads,
// usb, app). The guest mounts it at /n/host and drives it with files. The
// pickers and network calls run here, on the page main thread.
//
//   host/uploads/ctl     write "open" -> file picker -> host/uploads/{in,name}
//   host/downloads/ctl   write "save NAME" -> save picker for host/downloads/NAME
//   host/fetch/ctl       write a URL -> response in host/fetch/body
//   host/clip/text       clipboard text (polled both ways by the page)
//   host/usb/...         WebUSB bridge (see startUSB)

const enc = new TextEncoder();
const dec = new TextDecoder();

async function readText(root, path) {
  return dec.decode(await root.readFile(path));
}

async function writeText(root, path, text) {
  await root.writeFile(path, enc.encode(text));
}

async function ensureFile(root, path, text = "") {
  // Probe by reading: stat on a missing file can hang on this namespace
  // (which stalls the whole poller), while a failed open returns at once.
  try {
    await readText(root, path);
  } catch (_) {
    await writeText(root, path, text);
  }
}

export function startVPServices(root, opts = {}) {
  const status = opts.status || (() => {});
  let openBusy = false;
  let saveBusy = false;
  let lastFetch = "";
  let lastClip = "";

  const tick = async () => {
    // Open: browser picker -> guest uploads/in
    try {
      await ensureFile(root, "host/uploads/ctl");
      const ctl = (await readText(root, "host/uploads/ctl")).trim();
      if (ctl === "open" && !openBusy) {
        openBusy = true;
        await writeText(root, "host/uploads/ctl", "opening");
        try {
          const file = await pickFile();
          const buf = new Uint8Array(await file.arrayBuffer());
          await writeText(root, "host/uploads/name", file.name || "unnamed");
          await root.writeFile("host/uploads/in", buf);
          if (file.name) await root.writeFile("host/uploads/" + file.name, buf);
          await writeText(root, "host/uploads/ctl",
            `done ${file.name || "unnamed"} ${buf.length}`);
          status(`imported ${file.name || "unnamed"}`);
        } catch (err) {
          await writeText(root, "host/uploads/ctl", `error ${err.name || err.message || err}`);
        } finally {
          openBusy = false;
        }
      }
    } catch (_) {}

    // Save: guest downloads/NAME -> browser picker (or download)
    try {
      await ensureFile(root, "host/downloads/ctl");
      const ctl = (await readText(root, "host/downloads/ctl")).trim();
      if (ctl.startsWith("save ") && !saveBusy) {
        const name = ctl.slice(5).trim();
        if (name) {
          saveBusy = true;
          await writeText(root, "host/downloads/ctl", "saving");
          try {
            const data = await root.readFile("host/downloads/" + name);
            await saveFile(name, data);
            await writeText(root, "host/downloads/ctl", `saved ${name}`);
            status(`saved ${name}`);
          } catch (err) {
            await writeText(root, "host/downloads/ctl", `error ${err.name || err.message || err}`);
          } finally {
            saveBusy = false;
          }
        }
      }
    } catch (_) {}

    // Fetch: guest writes a URL, page fetches and publishes the body
    try {
      await ensureFile(root, "host/fetch/ctl");
      const url = (await readText(root, "host/fetch/ctl")).trim();
      if (url && url !== lastFetch) {
        lastFetch = url;
        try {
          const res = await fetch(url);
          const body = await res.text();
          await writeText(root, "host/fetch/body", [
            `HTTP ${res.status} ${res.statusText}`,
            `URL ${res.url}`,
            "",
            body.slice(0, 4 << 20),
          ].join("\n"));
        } catch (err) {
          await writeText(root, "host/fetch/body", `fetch failed: ${err.message || err}\n`);
        }
      }
    } catch (_) {}

    // Clipboard: guest -> browser
    try {
      await ensureFile(root, "host/clip/text");
      const text = await readText(root, "host/clip/text");
      if (text !== lastClip) {
        lastClip = text;
        if (text && navigator.clipboard?.writeText) {
          navigator.clipboard.writeText(text).catch(() => {});
        }
      }
    } catch (_) {}
  };

  const usbRoot = {
    readFile: (p) => root.readFile("host/" + p),
    writeFile: (p, d) => root.writeFile("host/" + p, d),
    makeDirAll: (p) => root.makeDirAll("host/" + p),
    removeAll: (p) => root.removeAll("host/" + p),
    remove: (p) => root.remove("host/" + p),
    stat: (p) => root.stat("host/" + p),
  };
  startUSB(usbRoot, status).catch((err) => status("usb: " + (err.message || err)));

  const timer = setInterval(tick, 1000);
  tick();
  return { stop() { clearInterval(timer); }, tick };
}

        async function startUSB(root, say) {
            if (!("usb" in navigator)) {
                await writeText(root, "usb/ctl", "error WebUSB unavailable (Chrome/Edge + secure context required)");
                return;
            }
            const devs = new Map();   // id -> { device, claimed:Set, lastReq }
            let nextId = 0;
            let ctlBusy = false;

            async function refreshDevices() {
                const lines = [];
                for (const [id, d] of devs) {
                    const mfr = d.device.manufacturerName || "";
                    const prod = d.device.productName || "";
                    lines.push(`${id} 0x${toHex(d.device.vendorId, 4)}:0x${toHex(d.device.productId, 4)} cfg${d.device.configuration ? d.device.configuration.configurationValue : 0} ${mfr} ${prod}`);
                }
                await writeText(root, "usb/devices", lines.join("\n") + (lines.length ? "\n" : ""));
            }

            async function openDevice(filters) {
                const device = await navigator.usb.requestDevice({ filters });
                if (!device.opened) { await device.open(); }
                // Select a configuration if none is active; claimInterface
                // fails on an unconfigured device. Pick the first config.
                if (!device.configuration && device.configurations.length) {
                    try { await device.selectConfiguration(
                        device.configurations[0].configurationValue); }
                    catch (_) {}  // some devices reject SET_CONFIGURATION; ignore
                }
                const id = String(nextId++);
                devs.set(id, { device, claimed: new Set() });
                await root.makeDirAll(`usb/${id}`);
                await writeText(root, `usb/${id}/ctl`, "");
                await writeText(root, `usb/${id}/status`, `open`);
                await writeText(root, `usb/${id}/req`, "");
                await writeText(root, `usb/${id}/resp`, `ok 0`);
                await writeInfo(root, id, devs.get(id));
                await refreshDevices();
                await writeText(root, "usb/ctl", `ready ${id}`);
                say(`usb: opened device ${id} (0x${toHex(device.vendorId,4)}:0x${toHex(device.productId,4)})`);
            }

            async function writeInfo(root, id, d) {
                const dev = d.device;
                const lines = [`id ${id}`, `vid 0x${toHex(dev.vendorId,4)}`, `pid 0x${toHex(dev.productId,4)}`, `opened ${dev.opened}`];
                const cfg = dev.configuration;
                lines.push(`config ${cfg ? cfg.configurationValue : 0}`);
                if (cfg) for (const iface of cfg.interfaces) {
                    lines.push(` iface ${iface.interfaceNumber} alt ${iface.alternates.length}`);
                    for (const alt of iface.alternates) {
                        lines.push(`  alt ${alt.alternateSetting} class 0x${toHex(alt.interfaceClass,2)} sub 0x${toHex(alt.interfaceSubclass,2)} proto 0x${toHex(alt.interfaceProtocol,2)} ${alt.interfaceName||""}`);
                        for (const ep of alt.endpoints) {
                            lines.push(`   ep 0x${toHex(ep.endpointNumber,2)} ${ep.direction} ${ep.type} maxpkt ${ep.packetSize}`);
                        }
                    }
                }
                await writeText(root, `usb/${id}/info`, lines.join("\n") + "\n");
            }

            async function closeDevice(id) {
                const d = devs.get(id);
                if (!d) { return; }
                try {
                    for (const iface of d.claimed) { await d.device.releaseInterface(iface); }
                    await d.device.close();
                } catch (_) {}
                devs.delete(id);
                try { await root.removeAll(`usb/${id}`); } catch (_) {}
                await refreshDevices();
                await writeText(root, "usb/ctl", `closed ${id}`);
            }

            // Parse "vid 0x2341 pid 0x0043" / "class 0x0A" / "" into WebUSB filters.
            function parseFilters(rest) {
                const filters = [];
                const tok = rest.trim().split(/\s+/).filter(Boolean);
                for (let i = 0; i + 1 < tok.length; i += 2) {
                    if (tok[i] === "vid" && tok[i+1]) { const f = { vendorId: parseInt(tok[i+1], 0) }; i++; if (tok[i+1] === "pid") { f.productId = parseInt(tok[i+2], 0); i++; } filters.push(f); }
                    else if (tok[i] === "class") { filters.push({ classCode: parseInt(tok[i+1], 0) }); }
                }
                return filters;
            }

            // WebUSB setup.requestType/recipient take string enums, not the
            // numeric bmRequestType fields, so map them here.
            const REQTYPE = ["standard", "class", "vendor"];
            const RECIP = ["device", "interface", "endpoint", "other"];
            function dvBytes(dv) {
                if (!dv) return new Uint8Array(0);
                return new Uint8Array(dv.buffer, dv.byteOffset, dv.byteLength);
            }
            async function runControl(d, bmReqType, bReq, wVal, wIdx, dataOrLen) {
                const setup = {
                    requestType: REQTYPE[(bmReqType >> 5) & 3],
                    recipient: RECIP[bmReqType & 3],
                    index: wIdx, request: bReq, value: wVal,
                };
                // SET/OUT direction: dataOrLen is a Uint8Array payload.
                // GET/IN direction: dataOrLen is a number (length to read).
                if (typeof dataOrLen === "number") {
                    const r = await d.device.controlTransferIn(setup, dataOrLen);
                    return { status: r.status, data: dvBytes(r.data) };
                } else {
                    const r = await d.device.controlTransferOut(setup, dataOrLen);
                    return { status: r.status, data: new Uint8Array(0) };
                }
            }

            function parseReq(line) {
                // "control <bm> <breq> <wval> <widx> <wlen> <hex>"  (hex optional -> IN)
                // "in <ep> <len>"   "out <ep> <hex>"
                const t = line.trim().split(/\s+/);
                if (t[0] === "control") {
                    const bm = parseInt(t[1], 0), bReq = parseInt(t[2], 0), wVal = parseInt(t[3], 0), wIdx = parseInt(t[4], 0), wLen = parseInt(t[5], 0);
                    const dirIn = ((bm >> 7) & 1) === 1;   // bit7 = direction (1 = IN)
                    if (dirIn) return { kind: "control-in", bm, bReq, wVal, wIdx, wLen };
                    const data = t[6] ? hexToBytes(t[6]) : new Uint8Array(0);
                    return { kind: "control-out", bm, bReq, wVal, wIdx, wLen, data };
                }
                if (t[0] === "in")  return { kind: "in",  ep: parseInt(t[1], 0) & 0xff, len: parseInt(t[2], 0) || 64 };
                if (t[0] === "out") return { kind: "out", ep: parseInt(t[1], 0) & 0xff, data: hexToBytes(t[2] || "") };
                return null;
            }

            async function runReq(id, d, line) {
                const r = parseReq(line);
                if (!r) { await writeText(root, `usb/${id}/resp`, "err bad request"); return; }
                try {
                    let out = "";
                    if (r.kind === "control-in") {
                        const res = await runControl(d, r.bm, r.bReq, r.wVal, r.wIdx, r.wLen);
                        out = `ok ${res.data.length} ${bytesToHex(res.data)} ${res.status}`;
                    } else if (r.kind === "control-out") {
                        const res = await runControl(d, r.bm, r.bReq, r.wVal, r.wIdx, r.data);
                        out = `ok 0 ${res.status}`;
                    } else if (r.kind === "in") {
                        const res = await d.device.transferIn(r.ep, r.len);
                        const data = dvBytes(res.data);
                        out = `ok ${data.length} ${bytesToHex(data)} ${res.status}`;
                    } else if (r.kind === "out") {
                        const res = await d.device.transferOut(r.ep, r.data);
                        out = `ok ${r.data.length} ${res.status}`;
                    }
                    await writeText(root, `usb/${id}/resp`, out);
                } catch (e) {
                    await writeText(root, `usb/${id}/resp`, `err ${e.name || e.message || e}`);
                }
            }

            async function handleDevCtl(id, d, cmd) {
                const t = cmd.trim().split(/\s+/);
                const sp = `usb/${id}/status`;
                try {
                    if (t[0] === "claim") { await d.device.claimInterface(parseInt(t[1], 0)); d.claimed.add(parseInt(t[1], 0)); await writeText(root, sp, `claimed ${t[1]}`); }
                    else if (t[0] === "release") { await d.device.releaseInterface(parseInt(t[1], 0)); d.claimed.delete(parseInt(t[1], 0)); await writeText(root, sp, `released ${t[1]}`); }
                    else if (t[0] === "close") { await closeDevice(id); return; }
                    else if (t[0] === "control") {
                        const bm = parseInt(t[1],0), bReq = parseInt(t[2],0), wVal = parseInt(t[3],0), wIdx = parseInt(t[4],0);
                        const dirIn = ((bm >> 7) & 1) === 1;
                        if (dirIn) { const res = await runControl(d, bm, bReq, wVal, wIdx, parseInt(t[5],0)||0); await writeText(root, sp, `ok ${res.data.length} ${bytesToHex(res.data)}`); }
                        else { const res = await runControl(d, bm, bReq, wVal, wIdx, hexToBytes(t[6]||"")); await writeText(root, sp, `ok ${res.status}`); }
                    }
                    else if (t[0] === "set") {
                        // set <iface> <baud> <8|7><n|o|e><1|2>  -> CDC SET_LINE_CODING
                        const iface = parseInt(t[1],0), baud = parseInt(t[2],0);
                        const fmt = t[3] || "8n1";
                        const dbits = parseInt(fmt[0]||"8",10);
                        const par = ({n:0,o:1,e:2,m:3}[fmt[1]||"n"]) ?? 0;
                        const stop = (fmt[2]==="2") ? 2 : 0;
                        const lc = new Uint8Array(7);
                        new DataView(lc.buffer).setUint32(0, baud, true); lc[4]=stop; lc[5]=par; lc[6]=dbits;
                        // bmRequestType 0x21 (host->device, class, interface)
                        const res = await runControl(d, 0x21, 0x20, 0, iface, lc);
                        await writeText(root, sp, `set ${baud} ${fmt} ${res.status}`);
                    }
                    else { await writeText(root, sp, `err unknown: ${cmd}`); }
                } catch (e) {
                    await writeText(root, sp, `err ${e.name || e.message || e}`);
                }
            }

            // Main poll loop: usb/ctl (open/close/reset) + per-device ctl/req.
            setInterval(async () => {
                if (ctlBusy) return; ctlBusy = true;
                try {
                    let ctl; try { ctl = (await readText(root, "usb/ctl")).trim(); } catch { ctl = ""; }
                    if (ctl.startsWith("open")) {
                        await writeText(root, "usb/ctl", "opening");
                        try { await openDevice(parseFilters(ctl.slice(4))); }
                        catch (e) { await writeText(root, "usb/ctl", `error ${e.name || e.message || e}`); }
                    } else if (ctl.startsWith("close ")) {
                        await closeDevice(ctl.slice(6).trim());
                    } else if (ctl.startsWith("reset ")) {
                        const id = ctl.slice(6).trim(); const d = devs.get(id);
                        if (d) { try { await d.device.reset(); await writeText(root, "usb/ctl", `reset ${id}`); } catch (e) { await writeText(root, "usb/ctl", `error ${e.message||e}`); } }
                    }
                    for (const [id, d] of devs) {
                        try {
                            // Edge-triggered: a command in usb/<id>/ctl is
                            // handled once, then cleared so it doesn't re-fire.
                            // Results land in usb/<id>/status. Same for req/resp.
                            const dc = (await readText(root, `usb/${id}/ctl`)).trim();
                            if (dc) { await handleDevCtl(id, d, dc); if (devs.has(id)) await writeText(root, `usb/${id}/ctl`, ""); }
                            const req = (await readText(root, `usb/${id}/req`)).trim();
                            if (req) { await runReq(id, d, req); if (devs.has(id)) await writeText(root, `usb/${id}/req`, ""); }
                        } catch (e) { /* device dir may be transient */ }
                    }
                } finally { ctlBusy = false; }
            }, 1000);

            await writeText(root, "usb/ctl", "ready");
            say("usb bridge ready");
        }


function toHex(n, w) { return (n >>> 0).toString(16).padStart(w, "0"); }
function bytesToHex(u8) { return Array.from(u8).map(b => b.toString(16).padStart(2, "0")).join(""); }
function hexToBytes(h) {
  h = h.replace(/\s+/g, "");
  const out = new Uint8Array(h.length >> 1);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(h.substr(i * 2, 2), 16);
  return out;
}

async function pickFile() {
  if (typeof window.showOpenFilePicker === "function") {
    try {
      const [handle] = await window.showOpenFilePicker();
      return await handle.getFile();
    } catch (err) {
      if (err.name === "AbortError") throw err;
      // Fall through to the input fallback.
    }
  }
  return await new Promise((resolve, reject) => {
    const input = document.createElement("input");
    input.type = "file";
    input.style.display = "none";
    input.onchange = () => {
      const file = input.files && input.files[0];
      input.remove();
      file ? resolve(file) : reject(new Error("no file selected"));
    };
    document.body.appendChild(input);
    input.click();
  });
}

async function saveFile(name, data) {
  if (typeof window.showSaveFilePicker === "function") {
    try {
      const handle = await window.showSaveFilePicker({ suggestedName: name });
      const writable = await handle.createWritable();
      await writable.write(data);
      await writable.close();
      return;
    } catch (err) {
      if (err.name === "AbortError") throw err;
    }
  }
  const blob = new Blob([data], { type: "application/octet-stream" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.textContent = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 30000);
}
