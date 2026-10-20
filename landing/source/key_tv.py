#!/usr/bin/env python3
"""Готовит картинку телевизора для лендинга: белый/серый фон и зелёный экран -> прозрачность.

Запуск: python3 key_tv.py исходник.png  ->  ../img/tv.webp
Печатает проценты экрана — их вписать в .screen (left/top/width/height) в index.html.
"""
import sys
from collections import deque
from pathlib import Path
from PIL import Image

src = sys.argv[1]
out = Path(__file__).resolve().parent.parent / 'img' / 'tv.webp'
im = Image.open(src).convert('RGBA')
W, H = im.size
px = im.load()


def is_bg(c):
    r, g, b, _ = c
    return min(r, g, b) > 178 and max(r, g, b) - min(r, g, b) < 14


# Фон: заливка от краёв, чтобы не съесть светлые детали внутри рисунка
seen = bytearray(W * H)
q = deque([(x, y) for x in range(W) for y in (0, H - 1)] + [(x, y) for y in range(H) for x in (0, W - 1)])
while q:
    x, y = q.popleft()
    i = y * W + x
    if seen[i] or not is_bg(px[x, y]):
        continue
    seen[i] = 1
    for nx, ny in ((x + 1, y), (x - 1, y), (x, y + 1), (x, y - 1)):
        if 0 <= nx < W and 0 <= ny < H and not seen[ny * W + nx]:
            q.append((nx, ny))

gx, gy = [], []
for y in range(H):
    for x in range(W):
        r, g, b, _ = px[x, y]
        if seen[y * W + x]:
            px[x, y] = (r, g, b, 0)
            continue
        k = g - max(r, b)
        if k > 60:
            px[x, y] = (r, g, b, 0)
            gx.append(x)
            gy.append(y)
        elif k > 25:  # зелёный ореол по краю экрана
            px[x, y] = (r, min(g, max(r, b) + 10), b, int(255 * (60 - k) / 35))

bb = im.getbbox()
im = im.crop(bb)
w, h = im.size
im.save(out, 'WEBP', quality=88)
print(f'saved {out} {w}x{h} (aspect-ratio: {w} / {h})')
if gx:
    print('screen: left %.1f%%; top %.1f%%; width %.1f%%; height %.1f%%' % (
        (min(gx) - bb[0]) / w * 100, (min(gy) - bb[1]) / h * 100, (max(gx) - min(gx)) / w * 100, (max(gy) - min(gy)) / h * 100))
