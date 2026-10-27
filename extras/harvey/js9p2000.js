// 9P2000 root for Harvey's legacy virtio-9p device. Writable directories are
// opt-in; executable assets remain read-only. Device hooks carry Aspera I/O.
const encoder = new TextEncoder(), decoder = new TextDecoder();
const DMDIR = 0x80000000, MAXMSIZE = 65560, MAXFILE = 16 * 1024 * 1024;
function u8(v) { return Uint8Array.of(v); }
function number(size, v) {
  const b = new Uint8Array(size), d = new DataView(b.buffer);
  if (size === 8) d.setBigUint64(0, BigInt(v), true);
  else if (size === 4) d.setUint32(0, v, true);
  else d.setUint16(0, v, true);
  return b;
}
const u16 = v => number(2, v), u32 = v => number(4, v), u64 = v => number(8, v);
function cat(...parts) {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let pos = 0;
  for (const p of parts) { out.set(p, pos); pos += p.length; }
  return out;
}
const str = s => { const b = encoder.encode(s); return cat(u16(b.length), b); };
const bytes = value => typeof value === 'string' ? encoder.encode(value) : new Uint8Array(value);
const directory = (name, writable = false) => ({ name, dir: true, children: Object.create(null), writable });
function makeRoot() {
  const root = directory('/');
  for (const name of ['amd64', 'bin', 'boot', 'dev', 'env', 'fd', 'net', 'n', 'proc', 'srv', 'mnt'])
    root.children[name] = directory(name);
  root.children.amd64.children.bin = directory('bin');
  root.children.n.children.host = directory('host');
  return root;
}

class NineP2000 {
  constructor(root = makeRoot()) {
    this.root = root;
    this.fids = new Map();
    this.nextPath = 1;
    this.msize = MAXMSIZE;
    this.errors = [];
    this.assign(root);
  }
  assign(n, parent = null) {
    n.path = this.nextPath++;
    n.parent = parent;
    n.version = 0;
    n.mode ??= n.dir ? (n.writable ? 0o777 : 0o555) : 0o555;
    if (n.dir) for (const child of Object.values(n.children)) this.assign(child, n);
  }
  mkdir(path, writable = false) {
    let node = this.root;
    for (const name of path.split('/').filter(Boolean)) {
      if (!node.children[name]) {
        const child = directory(name, writable);
        this.assign(child, node);
        node.children[name] = child;
      }
      node = node.children[name];
      if (!node.dir) throw Error('not a directory');
    }
    if (writable) { node.writable = true; node.mode = 0o777; }
    return node;
  }
  lookup(path) {
    return path.split('/').filter(Boolean).reduce((node, name) => node?.children?.[name], this.root);
  }
  addFile(path, data, options = {}) {
    const parts = path.split('/').filter(Boolean), name = parts.pop();
    const parent = this.mkdir(parts.join('/'));
    const node = { name, dir: false, data: bytes(data), writable: false, ...options };
    this.assign(node, parent);
    parent.children[name] = node;
    return node;
  }
  qid(n) { return cat(u8(n.dir ? 0x80 : 0), u32(n.version), u64(n.path)); }
  stat(n) {
    const body = cat(u16(0), u32(0), this.qid(n), u32((n.dir ? DMDIR : 0) | n.mode),
      u32(0), u32(0), u64(n.dir ? 0 : n.data.length), str(n.name === '/' ? '.' : n.name),
      str('harvey'), str('harvey'), str('harvey'));
    return cat(u16(body.length), body);
  }
  send(reply, type, tag, payload = new Uint8Array()) {
    reply(cat(u32(7 + payload.length), u8(type), u16(tag), payload));
  }
  handle(req, reply) {
    let tag = 0xffff;
    try {
      const d = new DataView(req.buffer, req.byteOffset, req.byteLength);
      if (req.length < 7) throw Error('short request');
      const size = d.getUint32(0, true), type = req[4];
      tag = d.getUint16(5, true);
      if (size !== req.length || size > this.msize) throw Error('invalid request size');
      let p = 7;
      const take = n => { if (p + n > size) throw Error('short request'); const b = req.subarray(p, p + n); p += n; return b; };
      const int = n => { const b = take(n), v = new DataView(b.buffer, b.byteOffset, b.length); return n === 8 ? Number(v.getBigUint64(0, true)) : n === 4 ? v.getUint32(0, true) : n === 2 ? v.getUint16(0, true) : b[0]; };
      const string = () => decoder.decode(take(int(2)));
      const respond = payload => this.send(reply, type + 1, tag, payload);
      const fail = msg => { throw Error(msg); };
      if (type === 100) {
        const requested = int(4), version = string();
        if (requested < 256) fail('msize too small');
        this.msize = Math.min(requested, MAXMSIZE);
        this.fids.clear();
        respond(cat(u32(this.msize), str(version === '9P2000' ? '9P2000' : 'unknown')));
        return;
      }
      if (type === 102) fail('authentication not required');
      if (type === 108) { int(2); respond(); return; }
      const fid = int(4);
      if (type === 104) {
        int(4); string(); string();
        if (this.fids.has(fid)) fail('fid in use');
        this.fids.set(fid, { node: this.root, open: false });
        respond(this.qid(this.root)); return;
      }
      const f = this.fids.get(fid);
      if (!f) fail('unknown fid');
      let node = f.node;
      if (type === 110) {
        const newfid = int(4), count = int(2), qids = [];
        if (f.open || (newfid !== fid && this.fids.has(newfid))) fail('fid in use');
        for (let i = 0; i < count; i++) {
          const name = string();
          const next = !node.dir ? null : name === '..' ? node.parent || node : name === '.' ? node : node.children[name];
          if (!next) { if (!qids.length) fail('no such file: ' + node.name + '/' + name); break; }
          node = next; qids.push(this.qid(node));
        }
        if (qids.length === count) this.fids.set(newfid, { node, open: false });
        respond(cat(u16(qids.length), ...qids)); return;
      }
      if (type === 112) {
        const mode = int(1), access = mode & 3;
        if (f.open) fail('already open');
        if ((access === 1 || access === 2 || (mode & 16)) && !node.writable) fail('read only');
        if (node.dir && mode !== 0) fail('directory read only');
        if ((mode & 16) && !node.dir) { node.data = new Uint8Array(); node.version++; node.changed?.(node); }
        f.open = true; f.mode = access;
        respond(cat(this.qid(node), u32(this.msize - 24))); return;
      }
      if (type === 114) {
        const name = string(), perm = int(4), mode = int(1);
        if (!node.dir || !node.writable || f.open) fail('create denied');
        if (!name || name === '.' || name === '..' || name.includes('/') || node.children[name]) fail('invalid name or file exists');
        const child = (perm & DMDIR) ? directory(name, true) : { name, dir: false, data: new Uint8Array(), writable: true };
        child.mode = perm & 0o777;
        this.assign(child, node); node.children[name] = child; node.version++;
        f.node = child; f.open = true; f.mode = mode & 3;
        respond(cat(this.qid(child), u32(this.msize - 24))); return;
      }
      if (type === 116) {
        const offset = int(8), count = Math.min(int(4), this.msize - 11);
        if (!f.open || f.mode === 1) fail('not open for reading');
        let data;
        if (node.read) data = node.read(offset, count);
        else if (node.dir) {
          // Return whole stat records only, and retain a stable directory snapshot.
          if (offset === 0 || !f.entries) f.entries = Object.values(node.children).map(n => this.stat(n));
          let position = 0, length = 0; const records = [];
          for (const record of f.entries) {
            if (position >= offset) {
              if (position !== offset + length) fail('invalid directory offset');
              if (length + record.length > count) break;
              records.push(record); length += record.length;
            }
            position += record.length;
          }
          data = cat(...records);
        } else data = node.data.subarray(offset, offset + count);
        respond(cat(u32(data.length), data)); return;
      }
      if (type === 118) {
        const offset = int(8), count = int(4), data = take(count);
        if (!f.open || (f.mode !== 1 && f.mode !== 2) || !node.writable || node.dir) fail('not open for writing');
        if (offset + count > MAXFILE) fail('file too large');
        if (node.write) node.write(offset, data);
        else {
          if (offset + count > node.data.length) {
            const expanded = new Uint8Array(offset + count); expanded.set(node.data); node.data = expanded;
          }
          node.data.set(data, offset);
        }
        node.version++;
        node.changed?.(node);
        respond(u32(count)); return;
      }
      if (type === 120) { this.fids.delete(fid); respond(); return; }
      if (type === 122) {
        this.fids.delete(fid);
        if (!node.parent?.writable || !node.writable || (node.dir && Object.keys(node.children).length)) fail('remove denied');
        delete node.parent.children[node.name]; node.parent.version++;
        node.removed?.(); respond(); return;
      }
      if (type === 124) { const record = this.stat(node); respond(cat(u16(record.length), record)); return; }
      fail('unsupported 9P opcode ' + type);
    } catch (error) {
      this.errors.push({ type: req[4], message: error.message });
      if (this.errors.length > 40) this.errors.shift();
      this.send(reply, 107, tag, str(error.message));
    }
  }
}
export { NineP2000, makeRoot };
