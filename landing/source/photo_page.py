# Собирает index-photo.html: тот же лендинг, но в первом экране фото-коробка (слои из photo_box.py) вместо SVG,
# а из коробки вылетают игральные карты с жёлтой рубашкой posta.
# Запуск из landing/: python3 -I source/photo_page.py
# Координаты — в пикселях обрезанного фото 810×840 (исходник минус CROP из photo_box.py).
import math
import random
import re

html = open('index.html', encoding='utf-8').read()

# Внутренность (тёмное дно и задние стенки) видна только при взрыве; верх стенок поднят на EXT=26 из photo_box.py
PHOTO = '''<div class="pb" aria-hidden="true">
<i class="pb-shadow"></i>
<svg class="pb-in" viewBox="0 0 810 840"><defs><radialGradient id="pb-glow" cx=".5" cy=".55" r=".55"><stop offset="0" stop-color="#FFF6B0"/><stop offset=".5" stop-color="#FFD21A" stop-opacity=".7"/><stop offset="1" stop-color="#FFD21A" stop-opacity="0"/></radialGradient></defs>
<polygon points="42,307 406,98 768,307 406,516" fill="#2A1E00"/>
<polygon points="42,307 406,98 406,228 42,437" fill="#5C4400" opacity=".9"/>
<polygon points="406,98 768,307 768,437 406,228" fill="#3E2E00"/>
<polygon points="42,307 406,98 768,307 406,516" fill="url(#pb-glow)"/>
</svg>
<div class="pb-body"><img src="img/box-body.webp" alt="">
<svg viewBox="0 0 810 840"><g transform="matrix(1 -.5733 0 1 437 537)"><g transform="rotate(-3 150 105)">
<text x="150" y="122" text-anchor="middle" font-family="Jost, Futura, sans-serif" font-weight="900" font-size="66" textLength="272" lengthAdjust="spacingAndGlyphs" fill="#111">БЕСПЛАТНО</text>
<text x="150" y="172" text-anchor="middle" font-family="Jost, Futura, sans-serif" font-weight="900" font-size="30" letter-spacing="7" fill="#C8001A">AUTÖPILÖT</text>
</g></g></svg></div>
<img class="pb-lid" src="img/box-lid.webp" alt="">
</div>'''

# Карты: лицо — фото или видео в белой рамке, тема и тип поста; рубашка — жёлтая с логотипом.
# 96 карт, половина с видео (ролики 256×272, 2,5 с самого динамичного куска, без звука — img/vid/, источники в source/vid-credits.json).
# Часть фото-карт переворачивается в полёте; у остальных (класс nf) рубашки нет вовсе — это один слой без 3D
PHOTOS = [('img/boo.jpg?v=2', 'Котики'), ('img/jet/p1025.webp', 'Мопсы'), ('img/jet/p111.webp', 'Ретроавто'),
          ('img/jet/p63.webp', 'Кофе'), ('img/jet/p39.webp', 'Винил'), ('img/jet/p235.webp', 'Горы'),
          ('img/jet/p237.webp', 'Собаки'), ('img/jet/p24.webp', 'Книги'), ('img/jet/p91.webp', 'Фото'),
          ('img/jet/p75.webp', 'Вино'), ('img/jet/p274.webp', 'Города'), ('img/jet/p244.webp', 'Птицы'),
          ('img/jet/p225.webp', 'Чай'), ('img/jet/p215.webp', 'Море'), ('img/jet/p21.webp', 'Мода'),
          ('img/jet/p183.webp', 'Вэнлайф'), ('img/jet/p174.webp', 'Путешествия'), ('img/jet/p145.webp', 'Гитары'),
          ('img/jet/p0.webp', 'IT'), ('img/jet/p1062.webp', 'Уют')]
VIDEOS = [('skate', 'Скейт', '0:14'), ('coaster', 'Аттракционы', '0:21'), ('puppies', 'Собаки', '0:09'), ('city', 'Дороги', '0:32'),
          ('fire', 'Костры', '0:18'), ('hyperlapse', 'Города', '0:11'), ('waterfall', 'Природа', '0:47'), ('fireworks', 'Праздники', '0:26'),
          ('mountains', 'Горы', '0:38'), ('frying', 'Еда', '0:16')]
# Куда прилетает карта — точка экрана в vw/vh: сетка 12×8 со случайным сдвигом, чтобы карты равномерно закрыли весь экран
# (раньше летели на фиксированное расстояние от коробки и в основном уходили за верх и правый край)
rnd = random.Random(7)
N = 96
targets = [((c + rnd.uniform(.1, .9)) / 12 * 110 - 5, (r + rnd.uniform(.1, .9)) / 8 * 108 - 4) for r in range(8) for c in range(12)]
rnd.shuffle(targets)
jets = []
for i in range(N):
    tx, ty = targets[i]
    video = i % 2 == 0
    # Видео-карты не переворачиваются (видео всё время лицом к зрителю), крупнее и летят дольше
    flip = 0 if video else rnd.choice([0, 180, 360, 540]) * (1 if rnd.random() < .5 else -1)
    if video:
        key, topic, dur = VIDEOS[(i // 2) % len(VIDEOS)]
        media = (f'<canvas width="256" height="272" data-v="{key}" style="background-image:url(img/vid/{key}.webp)"></canvas>'
                 f'<span class="jd">{dur}</span>')
        meta = f'Видео · {dur}'
        s_, t_ = rnd.uniform(1.05, 1.6), rnd.uniform(1.3, 1.65)
    else:
        src, topic = PHOTOS[(i // 2) % len(PHOTOS)]
        media, meta = f'<img src="{src}" alt="" decoding="async">', 'Фото'
        s_, t_ = rnd.uniform(.8, 1.35), rnd.uniform(.95, 1.3)
    style = (f'--tx:{tx:.0f}vw;--ty:{ty:.0f}vh;--s:{s_:.2f};--r:{rnd.uniform(-24, 24):.0f}deg;'
             f'--f:{flip}deg;--t:{t_:.2f}s;--d:{i * .011 + rnd.uniform(0, .08):.3f}s')
    back = '' if flip == 0 else '<div class="jb"><span>post<sup>,–</sup><br>maker</span></div>'
    jets.append(f'<div class="jet{" nf" if flip == 0 else ""}" style="{style}"><div class="jc">'
                f'<div class="jf"><div class="jp">{media}</div>'
                f'<b>{topic}</b><span>{meta}</span><em>posta<sup>,–</sup></em></div>{back}</div></div>')
# Плееров ровно по одному на ролик: 48 одновременных <video> декодировали видео 48 раз и роняли частоту кадров в 4 раза.
# Карты — <canvas>, в которые скрипт копирует новый кадр своего ролика (копия идёт на видеокарте и почти ничего не стоит)
SOURCES = ''.join(f'<video src="img/vid/{k}.mp4" data-v="{k}" muted loop playsinline preload="none"></video>' for k, _, _ in VIDEOS)
JETS = ('<div class="jets" aria-hidden="true">' + ''.join(jets) + '</div>'
        '<div class="jsrc" aria-hidden="true">' + SOURCES + '</div>')

CSS = '''/* Фото-коробка (слои img/box-body.webp и img/box-lid.webp из source/photo_box.py).
   В покое крышку время от времени чуть подбрасывает изнутри; наведение — мелкая дрожь. Сдвиги в % от высоты слоя
   и не больше нарощенной стенки (3%), поэтому в щели всегда стенка, а не внутренность.
   Клик (класс boom на <html>) — крышку срывает и уносит с вращением, коробка светится изнутри, потом крышка падает обратно.
   Всё анимируется только transform и opacity; класс armed (первое наведение) заранее выносит слои на GPU */
.pb { position: absolute; left: 0; right: 0; top: 0; aspect-ratio: 810 / 840; animation: gbfloat 3.2s ease-in-out infinite }
.pb > *, .pb-body > * { position: absolute; inset: 0; width: 100%; height: 100% }
.pb-shadow { inset: auto 14% -2% 14% !important; height: 7% !important; border-radius: 50%; background: radial-gradient(closest-side, rgba(0,0,0,.35), transparent); animation: gbshadow 3.2s ease-in-out infinite }
.pb-in { opacity: 0 }
.pb-body { transform-origin: 50% 100%; animation: pbjolt 4.6s infinite }
.pb-lid { transform-origin: 97% 38%; animation: pbknock 4.6s infinite }
@keyframes pbknock {
  0%, 56%, 100% { transform: none; animation-timing-function: ease-in }
  58% { transform: translateY(-1.1%); animation-timing-function: ease-in }
  60.5% { transform: none }
  62% { transform: translateY(-.35%) }
  63.5% { transform: none }
  80% { transform: none }
  82% { transform: translateY(-.8%) rotate(.45deg) }
  84.5% { transform: none }
  86% { transform: translateY(-.25%) }
  87.5% { transform: none } }
@keyframes pbjolt { 0%, 56%, 61%, 80%, 85%, 100% { transform: none } 58% { transform: scale(1.006, .994) } 82% { transform: scale(1.006, .994) } }
.giftbox:hover .pb-lid { animation: pbrattle .5s infinite }
.giftbox:hover .pb-body { animation: none }
@keyframes pbrattle { 0%, 100% { transform: none } 20% { transform: translateY(-.7%) rotate(.3deg) } 40% { transform: translateY(-.15%) } 60% { transform: translateY(-.9%) rotate(-.2deg) } 80% { transform: translateY(-.2%) } }
.armed .pb-lid, .armed .pb-body, .armed .pb-in, .armed .flash { will-change: transform, opacity }
/* Взрыв длится 3,6 с: крышку срывает за полсекунды, она пропадает за кадром, пока летят карты,
   а когда последние почти растаяли (~2,2 с), плавно опускается сверху на место без отскока */
html.boom .giftbox .pb-lid { animation: pbfly 3.6s both }
html.boom .giftbox .pb-body { animation: pbsquash 3.6s both }
html.boom .giftbox .pb-in { animation: pbopen 3.6s both }
@keyframes pbfly {
  0% { transform: none; opacity: 1; animation-timing-function: cubic-bezier(.1, .8, .3, 1) }
  15% { transform: translate(-40vw, -95vh) rotate(-540deg) scale(.7); opacity: 1 }
  16% { transform: translate(-40vw, -95vh) rotate(-540deg) scale(.7); opacity: 0 }
  60% { transform: translateY(-60vh) rotate(-4deg); opacity: 0 }
  61% { transform: translateY(-60vh) rotate(-4deg); opacity: 1; animation-timing-function: cubic-bezier(.22, .65, .3, 1) }
  90% { transform: translateY(-.6%) rotate(0); animation-timing-function: ease-in }
  93% { transform: none }
  100% { transform: none; opacity: 1 } }
@keyframes pbsquash { 0% { transform: none } 3% { transform: scale(1.06, .9) } 8% { transform: scale(.97, 1.04) } 13% { transform: none } 93% { transform: none } 95% { transform: scale(1.008, .992) } 98%, 100% { transform: none } }
@keyframes pbopen { 0% { opacity: 0 } 2%, 92% { opacity: 1 } 93%, 100% { opacity: 0 } }
.boom .wrap { animation: none }
.boom .hero { animation: quake .36s }
.armed .hero { z-index: 200; will-change: transform }

/* Игральные карты 63×88 мм. Внешний .jet летит (сдвиг, рост, поворот в плоскости); лицо и рубашка поворачиваются по Y
   каждая сама (рубашка на 180° дальше) — без preserve-3d, который ломается от opacity у родителя.
   Карта рисуется сразу в полном размере и стартует с маленького scale — растр готовится один раз и не мылится */
.jets { position: absolute; left: 50%; top: 34%; width: 0; height: 0; z-index: 5; pointer-events: none }
.giftbox.photo .burst { top: 2%; right: -6% }
.jet { position: absolute; left: 0; top: 0; width: 200px; height: 280px; margin: -140px 0 0 -100px; opacity: 0; border: 0; border-radius: 0; background: none; box-shadow: none; overflow: visible }
.jc { position: absolute; inset: 0 }
.jf, .jb { position: absolute; inset: 0; border-radius: 11px; backface-visibility: hidden; -webkit-backface-visibility: hidden; box-shadow: 0 1px 0 rgba(0,0,0,.1), 0 5px 12px rgba(0,0,0,.24) }
.jf { background: linear-gradient(160deg, #FFFEFA, #F1EEE6); border: 1px solid rgba(0,0,0,.14); padding: 10px; display: grid; grid-template-rows: 1fr auto auto auto; gap: 0; color: #1A1A1A }
.jp { position: relative; border-radius: 4px; overflow: hidden; background: #222; box-shadow: inset 0 0 0 1px rgba(0,0,0,.2) }
.jp img, .jp canvas { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: cover; display: block; background-size: cover; background-position: center }
.jsrc { position: fixed; left: 0; top: 0; width: 1px; height: 1px; overflow: hidden; opacity: 0; pointer-events: none }
.jd { position: absolute; right: 6px; bottom: 6px; font: 600 10px/1 var(--body); color: #fff; background: rgba(0,0,0,.7); border-radius: 3px; padding: 3px 5px }
.jf b { margin-top: 10px; font: 800 16px/1.1 "Jost", var(--body); text-transform: uppercase; letter-spacing: .04em }
.jf span { margin-top: 3px; font: 500 11px/1.2 var(--body); color: #77736A; text-transform: uppercase; letter-spacing: .08em }
.jf em { margin-top: 8px; padding-top: 7px; border-top: 1px solid rgba(0,0,0,.12); font: normal 900 11px/1 "Jost", sans-serif; color: #1A1A1A; text-align: right }
.jf em sup, .jb sup { font-size: .7em; vertical-align: .45em }
.jb { transform: perspective(1100px) rotateY(180deg); background: #FFD21A; border: 1px solid rgba(0,0,0,.18); display: grid; place-items: center; overflow: hidden }
.jb::before { content: ""; position: absolute; inset: 10px; border: 2px solid #111; border-radius: 6px;
  background: repeating-linear-gradient(45deg, rgba(0,0,0,.07) 0 2px, transparent 2px 9px), repeating-linear-gradient(-45deg, rgba(0,0,0,.07) 0 2px, transparent 2px 9px) }
.jb::after { content: ""; position: absolute; inset: 16px; border: 1px solid rgba(0,0,0,.45); border-radius: 3px }
.jb span { position: relative; z-index: 1; background: #FFD21A; border: 3px solid #111; padding: 10px 14px 12px; font: 900 36px/.88 "Jost", sans-serif; letter-spacing: -.02em; color: #111 }
.armed .jet { will-change: transform, opacity }
.armed .jet:not(.nf) .jf, .armed .jb { will-change: transform }
.nf .jf { backface-visibility: visible; -webkit-backface-visibility: visible }
.boom .jet { animation: jet var(--t) var(--d) cubic-bezier(.12, .75, .3, 1) both }
.boom .jet:not(.nf) .jf { animation: jflip var(--t) var(--d) cubic-bezier(.3, .6, .4, 1) both }
.boom .jb { animation: jflipb var(--t) var(--d) cubic-bezier(.3, .6, .4, 1) both }
@keyframes jet {
  0% { transform: scale(.06); opacity: 0 }
  5% { opacity: 1 }
  72% { opacity: 1 }
  100% { transform: translate(calc(var(--tx) - var(--ox, 0px)), calc(var(--ty) - var(--oy, 0px))) rotate(var(--r)) scale(var(--s)); opacity: 0 } }
@keyframes jflip { from { transform: perspective(1100px) rotateY(0) } to { transform: perspective(1100px) rotateY(var(--f)) } }
@keyframes jflipb { from { transform: perspective(1100px) rotateY(180deg) } to { transform: perspective(1100px) rotateY(calc(var(--f) + 180deg)) } }
</style>'''

SCRIPT = '''const vids = [...gift.querySelectorAll('.jsrc video')];
// Каждый ролик копирует свой новый кадр в карты со своим data-v — но только в те, что сейчас в середине полёта
// (не крошечные на старте и не тающие в конце), и через кадр (~12 к/с): так копий втрое меньше, а глазу не заметно
const mirrors = new Map(vids.map(v => [v, [...gift.querySelectorAll(`canvas[data-v="${v.dataset.v}"]`)].map(c => {
  const ctx = c.getContext('2d', { alpha: false }), st = c.closest('.jet').style;
  ctx.from = (parseFloat(st.getPropertyValue('--d')) + parseFloat(st.getPropertyValue('--t')) * .08) * 1000;
  ctx.to = (parseFloat(st.getPropertyValue('--d')) + parseFloat(st.getPropertyValue('--t')) * .7) * 1000;
  return ctx; })]));
let boomAt = 0;
const pump = v => { if (v.pumping) return; v.pumping = true; const ctxs = mirrors.get(v);
  const draw = () => { if (!root.classList.contains('boom')) { v.pumping = false; return; }
    const e = performance.now() - boomAt;
    if (v.readyState >= 2 && (v.odd = !v.odd)) ctxs.forEach(c => { if (e > c.from && e < c.to) c.drawImage(v, 0, 0); });
    next(); };
  const next = () => v.requestVideoFrameCallback ? v.requestVideoFrameCallback(draw) : requestAnimationFrame(draw);
  next(); };
// Карты летят в точки экрана (--tx/--ty), поэтому один раз при клике запоминаем, где на экране вылетают
const jetsBox = gift.querySelector('.jets');
gift.addEventListener('click', () => { const o = jetsBox.getBoundingClientRect(); jetsBox.style.setProperty('--ox', o.left + 'px'); jetsBox.style.setProperty('--oy', o.top + 'px'); root.classList.remove('boom'); void root.offsetWidth; root.classList.add('boom'); clearTimeout(boomTimer); boomTimer = setTimeout(() => { root.classList.remove('boom'); vids.forEach(v => v.pause()); }, 3700);
  boomAt = performance.now(); vids.forEach(v => { v.currentTime = 0; v.play().catch(() => {}); pump(v); }); });
// Перед первым взрывом выносим слои карт на GPU и декодируем картинки — чтобы сам клик не подлагивал
// Видео на картах играют только во время взрыва; при наведении их начинают грузить и рисуют первый кадр во все карты —
// так полотна и декодеры создаются заранее, а не в момент клика
const arm = () => { if (root.classList.contains('armed')) return; root.classList.add('armed'); gift.querySelectorAll('.jet img').forEach(i => i.decode && i.decode().catch(() => {})); vids.forEach(v => { v.preload = 'auto'; v.load(); v.addEventListener('loadeddata', () => mirrors.get(v).forEach(c => c.drawImage(v, 0, 0)), { once: true }); }); };
['pointerenter', 'pointerdown', 'focusin'].forEach(e => gift.addEventListener(e, arm));'''

def sub(pattern, repl, s, flags=re.S):
    out, n = re.subn(pattern, lambda m: repl, s, count=1, flags=flags)
    assert n == 1, pattern
    return out

new = sub(r'<svg data-v="3".*?</svg>', PHOTO, html)
new = sub(r'<div class="jets" aria-hidden="true">(?:<div class="jet".*?</div>)+</div>', JETS, new)
new = sub(r"gift\.addEventListener\('click', \(\) => \{ root\.classList\.remove\('boom'\).*?\}\);", SCRIPT, new)
# Старые стили карточек-«мемов» заменяются новыми ниже
new = sub(r'\.jets \{.*?\n\.gb-note', '.gb-note', new)
new = new.replace('<div class="giftbox" role="img"', '<div class="giftbox photo" role="img"', 1)
new = new.replace('</style>', CSS, 1)
open('index-photo.html', 'w', encoding='utf-8').write(new)
print('ok', len(jets), 'cards')
