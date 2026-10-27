// Browser endpoint for Aspera's guest-rendered double-buffered display.
export class AsperaDisplay {
  constructor(fs, canvas, { onFrame = () => {} } = {}) {
    this.canvas = canvas;
    canvas.width = 1024; canvas.height = 768;
    this.context = canvas.getContext('2d');
    this.pixels = new Uint8Array(1024 * 768 * 4);
    this.imageData = this.context.createImageData(1024, 768);
    for (let i = 3; i < this.imageData.data.length; i += 4) this.imageData.data[i] = 255;
    this.slots = [null, null];
    this.input = [];
    this.lastInputMove = false;
    this.frames = 0;
    this.onFrame = onFrame;
    const base = 'build/aspera-live';
    fs.mkdir(base);
    fs.addFile(`${base}/clock`, '', { read: (offset, count) => new TextEncoder().encode(new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false }) + '\n').subarray(offset, offset + count) });
    fs.addFile(`${base}/notepath`, '/lib/aspera/note.txt\n');
    fs.addFile(`${base}/dev/input`, '', { read: (_offset, count) => {
      if (!this.input.length) return new Uint8Array();
      const record = this.input[0];
      if (record.length > count) return new Uint8Array();
      this.input.shift();
      if (!this.input.length) this.lastInputMove = false;
      return record;
    } });
    fs.addFile(`${base}/dev/ctl`, '', { writable: true, write: (_offset, data) => this.control(new TextDecoder().decode(data)) });
    for (let slot = 0; slot < 2; slot++) fs.addFile(`${base}/dev/screen${slot}`, '', {
      writable: true, write: (offset, data) => this.write(slot, offset, data),
    });
  }
  enqueue(record) {
    const encoded = new TextEncoder().encode(record + '\n');
    const isMove = record.startsWith('move ');
    if (isMove && this.input.length && this.lastInputMove) {
      this.input[this.input.length - 1] = encoded;
      return;
    }
    if (this.input.length < 4096) {
      this.input.push(encoded);
      this.lastInputMove = isMove;
    }
  }
  control(command) {
    const match = /^(begin|begin-delta|commit|abort) ([01])\n?$/.exec(command);
    if (!match) throw Error('invalid display command');
    const [, action, index] = match, slot = Number(index);
    if (action === 'abort') { this.slots[slot] = null; return; }
    if (action.startsWith('begin')) {
      if (this.slots[slot]) throw Error('display slot busy');
      const delta = action === 'begin-delta';
      this.slots[slot] = { delta, data: delta ? null : new Uint8Array(this.pixels.length), tiles: delta ? [] : null, received: 0, pending: new Uint8Array() };
      return;
    }
    const state = this.slots[slot];
    if (!state || (!state.delta && state.received !== this.pixels.length) || state.pending.length)
      throw Error('incomplete display frame');
    this.slots[slot] = null;
    const rgba = this.imageData.data;
    // Guest pixels are little-endian 0x00RRGGBB words.
    if (state.delta) {
      for (const tile of state.tiles) {
        const { x, y, w, h, bytes } = tile;
        for (let row = 0; row < h; row++) {
          const srcOff = row * w * 4;
          const dstOff = ((y + row) * 1024 + x) * 4;
          this.pixels.set(bytes.subarray(srcOff, srcOff + w * 4), dstOff);
          for (let col = 0; col < w * 4; col += 4) {
            const s = srcOff + col, d = dstOff + col;
            rgba[d] = bytes[s + 2];
            rgba[d + 1] = bytes[s + 1];
            rgba[d + 2] = bytes[s];
          }
        }
      }
    } else {
      this.pixels = state.data;
      for (let i = 0; i < rgba.length; i += 4) {
        rgba[i] = this.pixels[i + 2]; rgba[i + 1] = this.pixels[i + 1];
        rgba[i + 2] = this.pixels[i];
      }
    }
    this.context.putImageData(this.imageData, 0, 0);
    this.frames++;
    this.onFrame(this.frames);
  }
  write(slot, offset, data) {
    const state = this.slots[slot];
    if (!state) throw Error('display slot not begun');
    if (offset !== state.received) throw Error('nonsequential frame write');
    state.received += data.length;
    if (!state.delta) {
      if (offset + data.length > state.data.length) throw Error('frame overflow');
      state.data.set(data, offset); return;
    }
    const pending = new Uint8Array(state.pending.length + data.length);
    pending.set(state.pending); pending.set(data, state.pending.length);
    let pos = 0;
    while (pending.length - pos >= 8) {
      const d = new DataView(pending.buffer, pos);
      const x = d.getUint16(0, true), y = d.getUint16(2, true);
      const w = d.getUint16(4, true), h = d.getUint16(6, true);
      if (!w || !h || x + w > 1024 || y + h > 768) throw Error('invalid display tile');
      const size = 8 + w * h * 4;
      if (pending.length - pos < size) break;
      state.tiles.push({ x, y, w, h, bytes: pending.slice(pos + 8, pos + size) });
      pos += size;
    }
    state.pending = pending.slice(pos);
  }
  attachInput() {
    const canvas = this.canvas;
    const position = e => {
      const r = canvas.getBoundingClientRect();
      return [Math.max(0, Math.min(1023, Math.floor((e.clientX - r.left) * 1024 / r.width))),
        Math.max(0, Math.min(767, Math.floor((e.clientY - r.top) * 768 / r.height)))];
    };
    canvas.addEventListener('pointerdown', e => {
      if (e.button !== 0) return;
      e.preventDefault(); canvas.focus(); canvas.setPointerCapture(e.pointerId);
      this.enqueue('down ' + position(e).join(' '));
    });
    canvas.addEventListener('pointermove', e => { if (e.buttons & 1) this.enqueue('move ' + position(e).join(' ')); });
    for (const event of ['pointerup', 'pointercancel']) canvas.addEventListener(event, () => this.enqueue('up'));
    canvas.addEventListener('keydown', e => {
      const key = e.key === 'Enter' ? 13 : e.key === 'Backspace' ? 8 : e.key.length === 1 ? e.key.charCodeAt(0) : 0;
      if (!key) return;
      e.preventDefault(); this.enqueue(`key ${key} ${(e.altKey ? 1 : 0) | ((e.ctrlKey || e.metaKey) ? 2 : 0)}`);
    });
  }
}
