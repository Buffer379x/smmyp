#!/usr/bin/env python3
"""TC-UI-08: Prüft, dass alle Randpixel eines PNG-Icons voll deckend sind (kein transparenter Rand).
Ohne Zusatzbibliotheken; unterstützt 8-Bit RGB/RGBA, nicht interlaced. Aufruf: check_icon_opaque.py icon.png"""
import struct, sys, zlib

def pixels(path):
    data = open(path, "rb").read()
    assert data[:8] == b"\x89PNG\r\n\x1a\n", "keine PNG-Datei"
    pos, idat = 8, b""
    while pos < len(data):
        n, typ = struct.unpack(">I4s", data[pos:pos + 8])
        chunk = data[pos + 8:pos + 8 + n]
        if typ == b"IHDR":
            w, h, depth, ctype, _, _, inter = struct.unpack(">IIBBBBB", chunk)
        elif typ == b"IDAT":
            idat += chunk
        pos += 12 + n
    assert depth == 8 and ctype in (2, 6) and inter == 0, f"nicht unterstützt: depth={depth} type={ctype}"
    bpp = 4 if ctype == 6 else 3
    raw, stride, rows, prev = zlib.decompress(idat), w * bpp, [], bytearray(w * bpp)
    for y in range(h):
        f, line = raw[y * (stride + 1)], bytearray(raw[y * (stride + 1) + 1:(y + 1) * (stride + 1)])
        for x in range(stride):
            a = line[x - bpp] if x >= bpp else 0
            b, c = prev[x], prev[x - bpp] if x >= bpp else 0
            if f == 1: line[x] = (line[x] + a) & 255
            elif f == 2: line[x] = (line[x] + b) & 255
            elif f == 3: line[x] = (line[x] + (a + b) // 2) & 255
            elif f == 4:
                p = a + b - c; pa, pb, pc = abs(p - a), abs(p - b), abs(p - c)
                line[x] = (line[x] + (a if pa <= pb and pa <= pc else b if pb <= pc else c)) & 255
        rows.append(line); prev = line
    return w, h, bpp, rows

w, h, bpp, rows = pixels(sys.argv[1])
if bpp == 3:
    print(f"OK: {w}x{h}, kein Alphakanal"); sys.exit(0)
border = [(x, y) for x in range(w) for y in (0, h - 1)] + [(x, y) for y in range(h) for x in (0, w - 1)]
bad = [(x, y) for x, y in border if rows[y][x * 4 + 3] != 255]
print(f"{'OK' if not bad else 'FEHLER'}: {w}x{h}, {len(bad)} nicht deckende Randpixel von {len(border)}")
sys.exit(1 if bad else 0)
