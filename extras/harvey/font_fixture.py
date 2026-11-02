"""Build a deterministic COLRv1 test font from the staged TrueType outlines."""
import struct


def colour_fixture(data):
    """Give A a solid red COLRv1 paint, preserving the font's outline tables."""
    u16 = lambda buf, at: struct.unpack_from('>H', buf, at)[0]
    tables = {}
    for i in range(u16(data, 4)):
        tag, _, offset, size = struct.unpack_from('>4sIII', data, 12 + 16*i)
        tables[tag] = data[offset:offset+size]
    cmap = tables[b'cmap']
    gid = None
    for i in range(u16(cmap, 2)):
        platform, encoding, offset = struct.unpack_from('>HHI', cmap, 4 + 8*i)
        if platform not in (0, 3) or u16(cmap, offset) != 4:
            continue
        count = u16(cmap, offset+6)//2
        for j in range(count):
            end = u16(cmap, offset+14+2*j)
            start = u16(cmap, offset+16+2*count+2*j)
            if start <= 65 <= end:
                delta = u16(cmap, offset+16+4*count+2*j)
                at = offset+16+6*count+2*j
                distance = u16(cmap, at)
                raw = u16(cmap, at+distance+2*(65-start)) if distance else 65
                gid = (raw+delta) & 0xffff if raw else 0
                break
        if gid:
            break
    if not gid:
        raise ValueError('Colour fixture needs A in a Unicode format-4 cmap')
    # CPAL palette 0: opaque red. COLRv1: PaintGlyph -> PaintSolid.
    tables[b'CPAL'] = struct.pack('>HHHHIH4B', 0, 1, 1, 1, 14, 0, 0, 0, 255, 255)
    tables[b'COLR'] = (struct.pack('>HHIIHIIIII', 1, 0, 0, 0, 0, 34, 0, 0, 0, 0)
                       + struct.pack('>IHI', 1, gid, 10)
                       + bytes([10, 0, 0, 6]) + struct.pack('>H', gid)
                       + struct.pack('>BHH', 2, 0, 0x4000))
    head = bytearray(tables[b'head'])
    head[8:12] = bytes(4)
    tables[b'head'] = head

    def checksum(blob):
        padded = blob + bytes(-len(blob) % 4)
        return sum(struct.unpack('>' + 'I'*(len(padded)//4), padded)) & 0xffffffff

    count = len(tables)
    power = count.bit_length()-1
    result = bytearray(struct.pack('>IHHHH', 0x10000, count, 16*(1 << power),
                                   power, 16*count-16*(1 << power)))
    result.extend(bytes(16*count))
    head_offset = None
    for i, (tag, blob) in enumerate(sorted(tables.items())):
        offset = len(result)
        struct.pack_into('>4sIII', result, 12+16*i, tag, checksum(blob), offset, len(blob))
        result.extend(blob)
        result.extend(bytes(-len(blob) % 4))
        if tag == b'head':
            head_offset = offset
    struct.pack_into('>I', result, head_offset+8, (0xb1b0afba-checksum(result)) & 0xffffffff)
    return result
