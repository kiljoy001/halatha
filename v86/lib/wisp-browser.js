// Browser-local WISP relay.
//
// v86's Wisp network adapter normally opens a WebSocket to a relay process
// that dials TCP. That relay is the only reason the VPS ever saw guest
// packets. This module runs the relay side *inside the browser* (in the v86
// task worker) instead: it accepts the same WISP frames and services each
// CONNECT with the browser's own networking.
//
// A browser has no raw TCP, so a stream is fulfilled by `fetch`: the guest's
// HTTP request is parsed out of the stream and replayed with the Host header
// (the guest connects to an IP, but fetch needs the name), upgraded to https
// and following redirects. The response is written back as HTTP/1.x bytes
// over the WISP stream. TLS and non-HTTP traffic cannot be replayed and is
// closed; nothing leaves the browser except the fetch the page itself makes.
//
// Wire format matches wisp/frame.go: little-endian [type:u8][id:u32][body].
//   CONNECT (1) [proto:u8=1][port:u16][hostname]
//   DATA    (2) [payload]
//   CONTINUE(3) [credit:u32]      server -> client
//   CLOSE   (4) [reason:u8]

const TYPE_CONNECT = 1;
const TYPE_DATA = 2;
const TYPE_CONTINUE = 3;
const TYPE_CLOSE = 4;

const MAX_CREDIT = 0xffffffff;
const MAX_REQUEST = 1 << 20;
const FETCH_TIMEOUT = 30000;
const CHUNK = 16384;

function frame(type, id, body) {
  const out = new Uint8Array(5 + (body ? body.length : 0));
  out[0] = type;
  new DataView(out.buffer).setUint32(1, id, true);
  if (body) out.set(body, 5);
  return out;
}

function continueFrame(id) {
  const body = new Uint8Array(4);
  new DataView(body.buffer).setUint32(0, MAX_CREDIT, true);
  return frame(TYPE_CONTINUE, id, body);
}

function closeFrame(id, reason) {
  return frame(TYPE_CLOSE, id, new Uint8Array([reason || 1]));
}

function concat(a, b) {
  const out = new Uint8Array(a.length + b.length);
  out.set(a);
  out.set(b, a.length);
  return out;
}

// Headers the fetch API forbids scripts from setting, plus hop-by-hop ones.
const DROP_HEADERS = new Set([
  "host", "connection", "content-length", "accept-encoding", "origin",
  "referer", "user-agent", "cookie", "transfer-encoding", "upgrade",
  "keep-alive", "proxy-connection", "te", "trailer",
]);

// Plan 9 clients usually send bare LF line endings; browsers and curl send
// CRLF. Accept either terminator and report its length.
function headerEnd(buf) {
  for (let i = 0; i + 3 < buf.length; i++) {
    if (buf[i] === 13 && buf[i + 1] === 10 && buf[i + 2] === 13 && buf[i + 3] === 10) {
      return { end: i, len: 4 };
    }
  }
  for (let i = 0; i + 1 < buf.length; i++) {
    if (buf[i] === 10 && buf[i + 1] === 10) {
      return { end: i, len: 2 };
    }
  }
  return null;
}

function decode(bytes) {
  return new TextDecoder("latin1").decode(bytes);
}

function encode(text) {
  return new TextEncoder().encode(text);
}

export class BrowserWisp {
  constructor() {
    this.binaryType = "arraybuffer";
    this.onmessage = null;
    this.onclose = null;
    this.streams = new Map();

    // v86 gates its CONNECT frames on stream 0's credit, so hand out credit
    // before the guest can open anything.
    setTimeout(() => this._emit(continueFrame(0)), 0);
  }

  _emit(bytes) {
    if (this.onmessage) this.onmessage({ data: bytes.buffer });
  }

  _data(id, bytes) {
    if (!bytes.length) return;
    this._emit(frame(TYPE_DATA, id, bytes));
  }

  _close(id, reason) {
    this._emit(closeFrame(id, reason));
    this.streams.delete(id);
  }

  send(buf) {
    const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf);
    if (bytes.length < 5) return;
    const id = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength).getUint32(1, true);
    switch (bytes[0]) {
    case TYPE_CONNECT: {
      const body = bytes.subarray(5);
      if (body.length < 3 || body[0] !== 1) {
        this._close(id, 1);
        return;
      }
      const port = body[1] | (body[2] << 8);
      const host = decode(body.subarray(3));
      this.streams.set(id, {
        id, host, port,
        buf: new Uint8Array(0),
        started: false,
        done: false,
        ctl: null,
      });
      // Grant credit for this stream: the client will send DATA now.
      this._emit(continueFrame(id));
      break;
    }
    case TYPE_DATA: {
      const stream = this.streams.get(id);
      if (!stream || stream.done) return;
      stream.buf = concat(stream.buf, bytes.subarray(5));
      if (stream.buf.length > MAX_REQUEST) {
        this._fail(stream, 413, "request too large");
        return;
      }
      if (!stream.started) this._maybeStart(stream);
      break;
    }
    case TYPE_CLOSE: {
      const stream = this.streams.get(id);
      if (!stream) return;
      stream.done = true;
      if (stream.ctl) stream.ctl.abort();
      this.streams.delete(id);
      break;
    }
    }
  }

  _maybeStart(stream) {
    const found = headerEnd(stream.buf);
    if (!found) return;
    const head = decode(stream.buf.subarray(0, found.end));
    const body = stream.buf.subarray(found.end + found.len);
    const lines = head.split(/\r?\n/);
    const requestLine = lines.shift() || "";
    const [method, target] = requestLine.split(" ");
    if (!method || !target) {
      this._fail(stream, 400, "malformed request");
      return;
    }

    const headers = new Headers();
    let hostHeader = "";
    let contentLength = 0;
    for (const line of lines) {
      const colon = line.indexOf(":");
      if (colon < 1) continue;
      const name = line.slice(0, colon).trim();
      const value = line.slice(colon + 1).trim();
      if (name.toLowerCase() === "host") hostHeader = value;
      if (name.toLowerCase() === "content-length") contentLength = parseInt(value, 10) || 0;
      if (!DROP_HEADERS.has(name.toLowerCase())) {
        try { headers.append(name, value); } catch { /* forbidden */ }
      }
    }

    let url;
    try {
      if (/^https?:\/\//i.test(target)) {
        url = new URL(target);
      } else {
        url = new URL(target, `http://${hostHeader || stream.host}`);
      }
    } catch {
      this._fail(stream, 400, "bad request target");
      return;
    }
    // The guest connects to an IP, so the Host header is the real name. The
    // v86 worker's location is a blob: URL, so there is no page protocol to
    // consult: always upgrade. An https page cannot fetch http anyway, and
    // nearly every host serves https.
    if (url.protocol === "http:") {
      url.protocol = "https:";
    }

    if (method !== "GET" && method !== "HEAD" && contentLength > body.length) {
      // Wait for the rest of the body before sending the request.
      return;
    }
    stream.started = true;

    const ctl = new AbortController();
    stream.ctl = ctl;
    const timer = setTimeout(() => ctl.abort(), FETCH_TIMEOUT);
    const init = {
      method,
      headers,
      redirect: "follow",
      credentials: "omit",
      mode: "cors",
      signal: ctl.signal,
    };
    if (method !== "GET" && method !== "HEAD" && body.length) {
      init.body = body;
    }

    fetch(url.toString(), init).then(async (resp) => {
      const payload = new Uint8Array(await resp.arrayBuffer());
      const out = [
        `HTTP/1.1 ${resp.status} ${resp.statusText || "OK"}`,
        `Content-Type: ${resp.headers.get("content-type") || "application/octet-stream"}`,
        `Content-Length: ${payload.length}`,
        `Connection: close`,
        ``,
        ``,
      ].join("\r\n");
      // v86's emulated TCP writes one buffered chunk per ACK; feeding the
      // whole response at once would leave all but the first segment queued.
      // Send headers+body in chunks with a short gap so each write is pumped.
      const all = concat(encode(out), payload);
      for (let off = 0; off < all.length; off += CHUNK) {
        this._data(stream.id, all.subarray(off, off + CHUNK));
        if (off + CHUNK < all.length) {
          await new Promise((resolve) => setTimeout(resolve, 30));
        }
      }
      this._close(stream.id, 1);
    }).catch((err) => {
      const msg = String((err && err.message) || err || "fetch failed");
      this._fail(stream, 502, `in-browser fetch failed: ${msg}`);
    }).finally(() => clearTimeout(timer));
  }

  _fail(stream, status, text) {
    const out = [
      `HTTP/1.1 ${status} ${text}`,
      `Content-Type: text/plain`,
      `Connection: close`,
      ``,
      text,
      ``,
    ].join("\r\n");
    this._data(stream.id, encode(out));
    this._close(stream.id, 1);
  }

  close() {
    for (const stream of this.streams.values()) {
      if (stream.ctl) stream.ctl.abort();
    }
    this.streams.clear();
    if (this.onclose) this.onclose();
  }
}
