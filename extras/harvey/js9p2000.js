// Minimal read-only 9P2000 server for v64's raw virtio-9p handler.
// Harvey's #9 client speaks 9P2000; v64's built-in basefs server is 9P2000.L.
// This server is intentionally small: enough to mount a diagnostic root and
// let Harvey's built-in init walk, stat and read files.

const Tversion = 100, Rversion = 101;
const Tauth = 102, Rerror = 107;
const Tattach = 104, Rattach = 105;
const Tflush = 108, Rflush = 109;
const Twalk = 110, Rwalk = 111;
const Topen = 112, Ropen = 113;
const Tcreate = 114, Rcreate = 115;
const Tread = 116, Rread = 117;
const Twrite = 118, Rwrite = 119;
const Tclunk = 120, Rclunk = 121;
const Tremove = 122, Rremove = 123;
const Tstat = 124, Rstat = 125;
const Twstat = 126, Rwstat = 127;

const QTDIR = 0x80, QTFILE = 0;
const DMDIR = 0x80000000;
const MAXMSIZE = 65560;

function file(name, text) {
  return { name, dir: false, data: new TextEncoder().encode(text) };
}
function dir(name, children = {}) {
  return { name, dir: true, children };
}

function makeRoot() {
  return dir("/", {
    "README.md": file("README.md", "Harvey on Halatha/v64\n"),
    amd64: dir("amd64", { bin: dir("bin") }),
    bin: dir("bin"),
    boot: dir("boot"),
    dev: dir("dev"),
    env: dir("env"),
    net: dir("net"),
    n: dir("n", { host: dir("host") }),
    proc: dir("proc"),
    srv: dir("srv"),
  });
}

class NineP2000 {
  constructor(root = makeRoot()) {
    this.root = root;
    this.fids = new Map();
    this.nextPath = 1;
    this.assign(root);
  }

  assign(n) {
    n.path = this.nextPath++;
    if (n.dir) for (const c of Object.values(n.children)) this.assign(c);
  }

  addFile(path, data) {
    const parts = path.split("/").filter(Boolean);
    let n = this.root;
    for (let i = 0; i < parts.length - 1; i++) {
      if (!n.children[parts[i]]) n.children[parts[i]] = dir(parts[i]);
      n = n.children[parts[i]];
    }
    const name = parts[parts.length - 1];
    n.children[name] = { name, dir: false, data: new Uint8Array(data) };
    this.assign(n.children[name]);
  }

  qid(n) {
    return { type: n.dir ? QTDIR : QTFILE, vers: 0, path: n.path };
  }

  stat(n) {
    const name = new TextEncoder().encode(n.name === "/" ? "." : n.name);
    const enc = (s) => {
      const b = new TextEncoder().encode(s);
      return u16(b.length) + b;
    };
    const st = cat(u16(0), u32(0), u8(n.dir ? QTDIR : QTFILE), u32(0), u64(n.path),
      u32(n.dir ? DMDIR | 0o555 : 0o444), u32(0), u32(0),
      u64(n.dir ? 0 : n.data.length), enc(n.name === "/" ? "." : n.name),
      enc("harvey"), enc("harvey"), enc("harvey"));
    return cat(u16(st.length), st);
  }

  handle(req, reply) {
    const d = new DataView(req.buffer, req.byteOffset, req.byteLength);
    const size = d.getUint32(0, true);
    if (size > req.byteLength || size < 7) return;
    const type = d.getUint8(4);
    const tag = d.getUint16(5, true);
    let p = 7;
    const str = () => { const n = d.getUint16(p, true); p += 2; const s = new TextDecoder().decode(req.subarray(p, p + n)); p += n; return s; };
    const u16 = (v) => { const b = new Uint8Array(2); new DataView(b.buffer).setUint16(0, v, true); return b; };
    const u32 = (v) => { const b = new Uint8Array(4); new DataView(b.buffer).setUint32(0, v, true); return b; };
    const u64 = (v) => { const b = new Uint8Array(8); new DataView(b.buffer).setBigUint64(0, BigInt(v), true); return b; };
    const err = (msg) => this.send(reply, Rerror, tag, enc(msg));
    const enc = (s) => { const b = new TextEncoder().encode(s); return cat(u16(b.length), b); };

    if (type === Tversion) {
      const msize = d.getUint32(p, true); p += 4;
      const version = str();
      this.fids.clear();
      this.send(reply, Rversion, tag, cat(u32(Math.min(MAXMSIZE, msize || MAXMSIZE)),
        enc("9P2000")));
      return;
    }
    if (type === Tauth) { err("authentication not required"); return; }
    if (type === Tattach) {
      const fid = d.getUint32(p, true); p += 4;
      p += 4; // afid
      str(); str(); // uname, aname
      this.fids.set(fid, { node: this.root, open: false });
      this.send(reply, Rattach, tag, this.qidBytes(this.qid(this.root)));
      return;
    }
    if (type === Tflush) { this.send(reply, Rflush, tag); return; }
    if (type === Twalk) {
      const fid = d.getUint32(p, true); p += 4;
      const newfid = d.getUint32(p, true); p += 4;
      const nwname = d.getUint16(p, true); p += 2;
      let node = this.fids.get(fid)?.node;
      if (!node) { err("unknown fid"); return; }
      const qids = [];
      for (let i = 0; i < nwname; i++) {
        const name = str();
        if (!node.dir) { err("not a directory"); return; }
        if (name === "..") node = this.parent(node) || node;
        else {
          const next = node.children[name];
          if (!next) { err("no such file"); return; }
          node = next;
        }
        qids.push(this.qid(node));
      }
      this.fids.set(newfid, { node, open: false });
      this.send(reply, Rwalk, tag, cat(u16(qids.length), ...qids.map((q) => this.qidBytes(q))));
      return;
    }
    if (type === Topen) {
      const fid = d.getUint32(p, true); p += 4;
      const mode = d.getUint8(p);
      const f = this.fids.get(fid);
      if (!f) { err("unknown fid"); return; }
      // OREAD=0 and OEXEC=3 both only read; OWRITE/ORDWR need a writable root.
      const access = mode & 3;
      if (!f.node.dir && access !== 0 && access !== 3) { err("read only"); return; }
      f.open = true;
      this.send(reply, Ropen, tag, cat(this.qidBytes(this.qid(f.node)), u32(0)));
      return;
    }
    if (type === Tread) {
      const fid = d.getUint32(p, true); p += 4;
      const off = Number(d.getBigUint64(p, true)); p += 8;
      const count = d.getUint32(p, true); p += 4;
      const f = this.fids.get(fid);
      if (!f || !f.open) { err("not open"); return; }
      if (!f.node.dir) {
        const data = f.node.data.subarray(off, Math.min(f.node.data.length, off + count));
        this.send(reply, Rread, tag, cat(u32(data.length), data));
        return;
      }
      // Directory read is not needed by the current boot path; return EOF.
      this.send(reply, Rread, tag, u32(0));
      return;
    }
    if (type === Tclunk) {
      const fid = d.getUint32(p, true); p += 4;
      this.fids.delete(fid);
      this.send(reply, Rclunk, tag);
      return;
    }
    if (type === Tstat) {
      const fid = d.getUint32(p, true); p += 4;
      const f = this.fids.get(fid);
      if (!f) { err("unknown fid"); return; }
      this.send(reply, Rstat, tag, this.stat(f.node));
      return;
    }
    if (type === Twrite || type === Tcreate || type === Tremove || type === Twstat) {
      err("read-only filesystem");
      return;
    }
    err("unsupported 9P opcode " + type);
  }

  parent(node) {
    const walk = (n) => {
      if (n === node) return null;
      if (n.dir) {
        for (const c of Object.values(n.children)) {
          if (c === node) return n;
          const r = walk(c);
          if (r) return r;
        }
      }
      return null;
    };
    return walk(this.root);
  }

  qidBytes(q) { return cat(u8(q.type), u32(q.vers), u64(q.path)); }

  send(reply, type, tag, payload = new Uint8Array(0)) {
    if (!(payload instanceof Uint8Array)) payload = new Uint8Array(payload);
    const body = new Uint8Array(7 + payload.length);
    const d = new DataView(body.buffer);
    d.setUint32(0, body.length, true);
    d.setUint8(4, type);
    d.setUint16(5, tag, true);
    body.set(payload, 7);
    reply(body);
  }
}

function cat(...parts) {
  const list = parts.flat();
  let n = 0;
  for (const p of list) n += p.length;
  const out = new Uint8Array(n);
  let o = 0;
  for (const p of list) { out.set(p, o); o += p.length; }
  return out;
}
function u8(v) { return new Uint8Array([v]); }
function u16(v) { const b = new Uint8Array(2); new DataView(b.buffer).setUint16(0, v, true); return b; }
function u32(v) { const b = new Uint8Array(4); new DataView(b.buffer).setUint32(0, v, true); return b; }
function u64(v) { const b = new Uint8Array(8); new DataView(b.buffer).setBigUint64(0, BigInt(v), true); return b; }

export { NineP2000, makeRoot };
