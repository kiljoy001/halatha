import test from 'node:test';
import assert from 'node:assert/strict';
import { NineP2000 } from '../extras/harvey/js9p2000.js';
import { AsperaDisplay } from '../extras/harvey/aspera-display.js';
const num = (size, value) => { const b = Buffer.alloc(size); size === 8 ? b.writeBigUInt64LE(BigInt(value)) : b.writeUIntLE(value >>> 0, 0, size); return b; };
const s = value => { const b = Buffer.from(value); return Buffer.concat([num(2, b.length), b]); };
const n = value => num(4, value);
function client(fs) {
  let tag = 0;
  return (type, ...body) => {
    const payload = Buffer.concat(body), request = Buffer.concat([n(payload.length + 7), num(1, type), num(2, tag++), payload]);
    let response;
    fs.handle(request, data => { assert.equal(response, undefined); response = Buffer.from(data); });
    assert.ok(response); assert.equal(response.readUInt32LE(0), response.length);
    assert.equal(response.readUInt16LE(5), tag - 1);
    return response;
  };
}
const attach = call => { assert.equal(call(100, n(65560), s('9P2000'))[4], 101); assert.equal(call(104, n(1), n(0xffffffff), s('harvey'), s(''))[4], 105); };
const walk = (call, fid, path) => call(110, n(1), n(fid), num(2, path.length), ...path.map(s));

test('9P stat and directory records use byte lengths and correct nested framing', () => {
  const fs = new NineP2000(); fs.addFile('bin/café', 'hello');
  const call = client(fs); attach(call); walk(call, 2, ['bin', 'café']);
  const stat = call(124, n(2));
  assert.equal(stat[4], 125);
  assert.equal(stat.readUInt16LE(7), stat.length - 9);
  assert.equal(stat.readUInt16LE(9), stat.length - 11);
  assert.equal(stat.readUInt16LE(50), Buffer.byteLength('café'));
  assert.equal(stat.subarray(52, 57).toString(), 'café');
  walk(call, 3, ['bin']); call(112, n(3), num(1, 0));
  assert.equal(call(116, n(3), num(8, 0), n(4)).readUInt32LE(7), 0);
  const read = call(116, n(3), num(8, 0), n(4096));
  assert.deepEqual(read.subarray(11), stat.subarray(9));
  assert.equal(call(116, n(3), num(8, read.length - 11), n(4096)).readUInt32LE(7), 0);
});

test('9P writable files support shell create/read/truncate/remove, assets stay read-only', () => {
  const fs = new NineP2000(); fs.mkdir('notes', true); fs.addFile('bin/rc', 'binary');
  const call = client(fs); attach(call); walk(call, 2, ['notes']);
  assert.equal(call(114, n(2), s('note'), n(0o600), num(1, 2))[4], 115);
  assert.equal(call(118, n(2), num(8, 0), n(5), Buffer.from('hello'))[4], 119);
  assert.equal(call(116, n(2), num(8, 0), n(5)).subarray(11).toString(), 'hello');
  call(120, n(2)); walk(call, 3, ['notes', 'note']); call(112, n(3), num(1, 17));
  assert.equal(fs.lookup('notes/note').data.length, 0);
  assert.equal(call(122, n(3))[4], 123); assert.equal(fs.lookup('notes/note'), undefined);
  walk(call, 4, ['bin', 'rc']); assert.equal(call(112, n(4), num(1, 17))[4], 107);
  assert.equal(new TextDecoder().decode(fs.lookup('bin/rc').data), 'binary');
});

test('9P partial walks do not install new fids and malformed requests return errors', () => {
  const fs = new NineP2000(), call = client(fs); attach(call);
  const partial = walk(call, 2, ['bin', 'missing']);
  assert.equal(partial[4], 111); assert.equal(partial.readUInt16LE(7), 1); assert.equal(fs.fids.has(2), false);
  assert.equal(call(110, n(1), n(3), num(2, 1), num(2, 100))[4], 107);
  assert.equal(call(116, n(1), num(8, 0), n(10))[4], 107);
});

test('display commits full frames and fragmented delta tiles, forwards input once', () => {
  globalThis.ImageData = class { constructor(data, width, height) { Object.assign(this, { data, width, height }); } };
  const fs = new NineP2000(); let presented;
  const display = new AsperaDisplay(fs, { getContext: () => ({ putImageData: frame => { presented = frame; } }) });
  display.control('begin 0\n');
  assert.throws(() => display.control('commit 0\n'), /incomplete/);
  const pixels = new Uint8Array(1024 * 768 * 4); pixels.set([0x33, 0x22, 0x11, 0]);
  display.write(0, 0, pixels); display.control('commit 0\n');
  assert.deepEqual(Array.from(presented.data.slice(0, 4)), [0x11, 0x22, 0x33, 255]);
  display.control('begin-delta 1\n');
  const tile = Buffer.concat([num(2, 1), num(2, 1), num(2, 1), num(2, 1), Buffer.from([3, 2, 1, 0])]);
  display.write(1, 0, tile.subarray(0, 5)); display.write(1, 5, tile.subarray(5)); display.control('commit 1\n');
  assert.deepEqual(Array.from(presented.data.slice(4100, 4104)), [1, 2, 3, 255]);
  assert.deepEqual(Array.from(presented.data.slice(0, 4)), [0x11, 0x22, 0x33, 255]);
  display.enqueue('key 13 0');
  const input = fs.lookup('build/aspera-live/dev/input');
  assert.equal(new TextDecoder().decode(input.read(0, 96)), 'key 13 0\n');
  assert.equal(input.read(0, 96).length, 0);
});
