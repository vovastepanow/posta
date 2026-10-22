# Коробка для Postä: стирает надпись «postmaker» с жёлтой таблички на левой грани, новое имя рисует страница (SVG поверх).
# Запуск из landing/: python3 -I source/posta_box.py. Вход: img/box-body.webp, выход: img/box-body-posta.webp
# Табличка — параллелограмм с наклоном .5786 (как у грани); внутренность от рамки до рамки
from PIL import Image
im = Image.open('img/box-body.webp').convert('RGBA')
p = im.load()
K, X0, X1 = .5786, 73, 375
top = lambda x: 425 + K * (x - 80)
bot = lambda x: 514 + K * (x - 80)
# Каждый столбец заливаем медианой светлых (не текстовых) пикселей этого же столбца — так сохраняется свет по грани
for x in range(X0, X1 + 1):
    ys = range(round(top(x)), round(bot(x)) + 1)
    light = sorted((p[x, y] for y in ys if p[x, y][0] > 180 and max(p[x, y][:3]) > 150), key=sum)
    col = light[len(light) // 2] if light else (212, 174, 35, 255)
    for y in ys:
        p[x, y] = col
im.save('img/box-body-posta.webp', quality=90, method=6)

# Верх корпуса для взрыва: когда крышку срывает, видно наращённые стенки и внутренность — рисуем их здесь же,
# в одном слое с корпусом, чтобы при сплющивании внутренность сжималась вместе со стенками.
# Координаты — в кадре webp (исходник photo_box.py минус CROP). Разрез крышки: углы y=322, передний угол (406, 542); кромка стенок на EXT выше
from PIL import ImageDraw, ImageFilter
import random
EXT, SL = 26, 220 / 383
cut = lambda x: 322 + (x - 23) * SL if x <= 406 else 542 - (x - 406) * SL
W, H = im.size
# 1) Наращённые стенки: цвет столбца — медиана чуть ниже разреза (слева глубже, справа ценник начинается через 7px),
#    потом медиана по соседним столбцам той же грани — так нет вертикальных полос, а лента остаётся с чёткими краями
col = {}
for x in range(23, 789):
    yc = round(cut(x))
    ks = range(5, 14) if x < 406 else range(0, 4)
    ss = [p[x, yc + k] for k in ks if p[x, yc + k][3] > 250]
    if ss:
        col[x] = tuple(sorted(s[i] for s in ss)[len(ss) // 2] for i in range(3))
smooth = {}
for x, c in col.items():
    nb = [col[n] for n in range(x - 6, x + 7) if n in col and (n < 406) == (x < 406)]
    smooth[x] = tuple(sorted(c2[i] for c2 in nb)[len(nb) // 2] for i in range(3))
rnd = random.Random(7)
for x, c in smooth.items():
    yc = cut(x)
    for y in range(int(yc - EXT), int(yc) + 3):
        t = (y - (yc - EXT)) / EXT  # к кромке чуть светлее — свет падает на край картона
        k = 1.04 - .04 * t + rnd.uniform(-.012, .012)
        p[x, y] = tuple(min(255, round(v * k)) for v in c) + (255,)
    p[x, int(yc - EXT) - 1] = c + (110,)
# 2) Кромка картона и внутренность, рисуем вчетверо крупнее и ужимаем — ровные края
S = 4
L, F, R = (23, cut(23) - EXT), (406, cut(406) - EXT), (788, cut(788) - EXT)
B = (406, L[1] - (F[1] - L[1]))
T = 7  # толщина стенки
Li, Fi, Ri, Bi = (L[0] + T / SL * .5, L[1]), (F[0], F[1] - T), (R[0] - T / SL * .5, R[1]), (B[0], B[1] + T)
D = 292  # глубина до дна
sc = lambda pts, dy=0: [(x * S, (y + dy) * S) for x, y in pts]
big = Image.new('RGBA', (W * S, H * S), (0, 0, 0, 0))
inner = Image.new('L', big.size, 0)
ImageDraw.Draw(inner).polygon(sc([Li, Bi, Ri, Fi]), fill=255)
def grad(c0, c1, y0, y1):
    g = Image.new('RGBA', big.size)
    gd = ImageDraw.Draw(g)
    for y in range(0, H * S, S):
        t = min(1, max(0, (y / S - y0) / (y1 - y0)))
        gd.rectangle([0, y, W * S, y + S], fill=tuple(round(a + (b - a) * t) for a, b in zip(c0, c1)) + (255,))
    return g
def fill(layer, poly, g):
    m = Image.new('L', big.size, 0)
    ImageDraw.Draw(m).polygon(poly, fill=255)
    layer.paste(g, (0, 0), Image.composite(m, Image.new('L', big.size, 0), inner))
# Дно (видно только клин у дальнего угла), затем дальние стенки изнутри: левая в тени, правая светлее, к низу темнеют
fill(big, sc([Li, Bi, Ri, Fi], D), grad((92, 68, 6), (70, 50, 2), Bi[1] + D, Fi[1]))
fill(big, sc([Li, Bi, (Bi[0], Bi[1] + D), (Li[0], Li[1] + D)]), grad((176, 136, 18), (84, 62, 4), Bi[1], Bi[1] + D))
fill(big, sc([Bi, Ri, (Ri[0], Ri[1] + D), (Bi[0], Bi[1] + D)]), grad((214, 168, 26), (104, 78, 6), Bi[1], Bi[1] + D))
d = ImageDraw.Draw(big)
d.line(sc([Bi, (Bi[0], Bi[1] + D)]), fill=(110, 84, 8, 255), width=2 * S)  # шов в дальнем углу
# Тень от передних стенок на дальних — полоса вдоль передней кромки
sh = Image.new('L', big.size, 0)
ImageDraw.Draw(sh).line(sc([Li, Fi, Ri]), fill=150, width=26 * S)
sh = sh.filter(ImageFilter.GaussianBlur(9 * S))
big.paste(Image.new('RGBA', big.size, (40, 28, 0, 255)), (0, 0), Image.composite(sh, Image.new('L', big.size, 0), inner))
# Кромка: верхний торец стенок, светлый, с тёмной линией по внутреннему краю
rim = Image.new('L', big.size, 0)
ImageDraw.Draw(rim).polygon(sc([L, B, R, F]), fill=255)
ImageDraw.Draw(rim).polygon(sc([Li, Bi, Ri, Fi]), fill=0)
big.paste(Image.new('RGBA', big.size, (250, 226, 120, 255)), (0, 0), rim)
d.line(sc([Li, Bi, Ri, Fi, Li]), fill=(120, 90, 8, 200), width=S)
d.line(sc([L, B, R]), fill=(255, 244, 190, 255), width=S)
top = big.resize((W, H), Image.LANCZOS)
im = Image.alpha_composite(top, im)
im.save('img/box-body-posta.webp', quality=90, method=6)
