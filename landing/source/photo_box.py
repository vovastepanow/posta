# Фото-коробка для первого экрана: вырезает фон, стирает ценник «390 ₽», режет на крышку и корпус.
# Запуск из landing/: python3 -I source/photo_box.py
# Вход: source/box-photo-grey.png (1024×1024, zsky.ai). Выход: img/box-lid.webp, img/box-body.webp
# Координаты ниже — в пикселях исходника; на странице те же числа стоят в viewBox (обрезка смещает на CROP).
from collections import deque
from PIL import Image, ImageDraw, ImageFilter

SRC = 'source/box-photo-grey.png'
im = Image.open(SRC).convert('RGB')
W, H = im.size
px = im.load()

# Фон серый и почти без насыщенности, коробка жёлтая/красная — заливаем от краёв всё «серое»
def is_bg(p):
    r, g, b = p
    return max(r, g, b) - min(r, g, b) < 42 and max(r, g, b) < 236
bg = bytearray(W * H)
q = deque()
for x in range(W):
    for y in (0, H - 1):
        q.append((x, y))
for y in range(H):
    for x in (0, W - 1):
        q.append((x, y))
while q:
    x, y = q.popleft()
    i = y * W + x
    if bg[i] or not is_bg(px[x, y]):
        continue
    bg[i] = 1
    if x > 0: q.append((x - 1, y))
    if x < W - 1: q.append((x + 1, y))
    if y > 0: q.append((x, y - 1))
    if y < H - 1: q.append((x, y + 1))
mask = Image.frombytes('L', (W, H), bytes(0 if v else 255 for v in bg))
# Ужать на пиксель, чтобы срезать серую кайму, и смягчить край
mask = mask.filter(ImageFilter.MinFilter(3)).filter(ImageFilter.GaussianBlur(0.9))

# Ценник: внутренний белый параллелограмм закрашиваем цветом бумаги, текст рисует страница
LABEL = [(547, 627), (847, 455), (847, 664), (547, 836)]
d = ImageDraw.Draw(im)
d.polygon(LABEL, fill=(189, 183, 176))

out = im.convert('RGBA')
out.putalpha(mask)

# Разрез по нижней кромке крышки: левый угол, передний угол, правый угол (+2px нахлёст на корпус)
CUT = [(0, 412), (133, 412), (516, 632), (898, 412), (W, 412)]
lid_m = Image.new('L', (W, H), 0)
ImageDraw.Draw(lid_m).polygon([(0, 0), (W, 0)] + CUT[::-1], fill=255)
CUT2 = [(x, y - 3) for x, y in CUT]
body_m = Image.new('L', (W, H), 255)
ImageDraw.Draw(body_m).polygon([(0, 0), (W, 0)] + CUT2[::-1], fill=0)

CROP = (110, 90, 920, 930)
layers = {}
for name, m in (('lid', lid_m), ('body', body_m)):
    layer = out.copy()
    a = Image.composite(out.getchannel('A'), Image.new('L', (W, H), 0), m)
    layer.putalpha(a)
    layers[name] = layer

# Стенки корпуса наращиваем вверх на EXT под крышкой: в каждом столбце берём медиану пикселей на 0–2px ниже разреза (ценник справа начинается уже на 4px)
# и тянем этот цвет вверх. Когда крышка подпрыгивает, в щели видна стенка, а не пустота; внутренность — только при взрыве
EXT = 26
def cut_y(x):
    for (x0, y0), (x1, y1) in zip(CUT, CUT[1:]):
        if x0 <= x <= x1:
            return y0 + (y1 - y0) * (x - x0) / (x1 - x0)
body = layers['body']
bp = body.load()
ext = Image.new('RGBA', (W, H), (0, 0, 0, 0))
ep = ext.load()
for x in range(W):
    yc = cut_y(x)
    samples = [bp[x, round(yc) + k] for k in (0, 1, 2)]
    if min(p[3] for p in samples) < 250:
        continue
    col = tuple(sorted(p[i] for p in samples)[1] for i in range(3)) + (255,)
    for y in range(int(yc - EXT), int(yc) + 4):
        ep[x, y] = col
ext = ext.filter(ImageFilter.BoxBlur(1))
ext.alpha_composite(body)
layers['body'] = ext

for name, layer in layers.items():
    layer.crop(CROP).save(f'img/box-{name}.webp', quality=88, method=6)
print('crop', CROP)
