"""Генератор вариантов коробки Posta для первого экрана лендинга."""
import re

K = .8660254
def P(*pts): return ' '.join(f'{x:.1f},{y:.1f}' for x, y in pts)
def add(a, b, k=1): return (a[0] + b[0] * k, a[1] + b[1] * k)

BOW = ('<g transform="translate({x:.1f} {y:.1f}) scale({s:.2f})" stroke="#111" stroke-width="{sw:.1f}" stroke-linejoin="round" stroke-linecap="round"><path d="M-5 4 L-30 60 L-19 54 L-13 66 L5 8 Z" fill="{k}"/><path d="M5 4 L32 58 L20 53 L15 65 L-5 8 Z" fill="{k}"/><path d="M0 0 C-14 -44 -84 -62 -96 -28 C-102 -6 -46 6 0 0 Z" fill="{c}"/><path d="M0 0 C14 -44 84 -62 96 -28 C102 -6 46 6 0 0 Z" fill="{c}"/><path d="M-2 -2 C-26 -20 -60 -28 -82 -22 C-64 -10 -30 -2 -2 -2 Z" fill="#9A0014" stroke="none"/><path d="M2 -2 C26 -20 60 -28 82 -22 C64 -10 30 -2 2 -2 Z" fill="#9A0014" stroke="none"/><path d="M-30 -34 Q-52 -42 -70 -38 M30 -34 Q52 -42 70 -38" fill="none" stroke="#fff" stroke-opacity=".6" stroke-width="4"/><rect x="-13" y="-11" width="26" height="22" rx="6" fill="{c}"/><path d="M-6 -5 h8" stroke="#fff" stroke-opacity=".6" stroke-width="3"/></g>')

def G(T, a):
    X, Y = (K * a, .5 * a), (-K * a, .5 * a)
    TX, TY = add(T, X), add(T, Y)
    return X, Y, TX, TY, add(TX, Y)

def iso(cx, ty, a, h, top, left, right, rib, lidL, lidR, bow=None, bowk=None, lid=True, dots=True, ltext='', rtext='', open_=False, sw=5, over=1.07, inner=''):
    ribL, ribR, ribT = (rib, rib, rib) if rib is None or isinstance(rib, str) else rib
    T = (cx, ty); X, Y, TX, TY, TXY = G(T, a)
    d = lambda p: (p[0], p[1] + h)
    Xu, Yu = (K, .5), (-K, .5)
    rw = .16 * a
    def faceribs(TY, TXY, a, dd):
        X, Y = (K * a, .5 * a), (-K * a, .5 * a)
        m1 = add(TY, X, .5); a1, b1 = add(m1, Xu, -rw / 2), add(m1, Xu, rw / 2)
        m2 = add(TXY, Y, -.5); a2, b2 = add(m2, Yu, -rw / 2), add(m2, Yu, rw / 2)
        return (f'<polygon points="{P(a1, b1, dd(b1), dd(a1))}" fill="{ribL}"/>'
                f'<polygon points="{P(a2, b2, dd(b2), dd(a2))}" fill="{ribR}"/>')
    o = [f'<g stroke="#111" stroke-width="{sw}" stroke-linejoin="round">',
         f'<polygon points="{P(TY, TXY, d(TXY), d(TY))}" fill="{left}"/>',
         f'<polygon points="{P(TXY, TX, d(TX), d(TXY))}" fill="{right}"/>']
    if dots: o.append(f'<polygon points="{P(TXY, TX, d(TX), d(TXY))}" fill="url(#pm-dots)" stroke="none"/>')
    if rib: o.append(faceribs(TY, TXY, a, d))
    o.append('</g>')
    gl1, gl2 = add(TY, (0, h * .15)), add(TY, (0, h * .8))
    o.append(f'<line x1="{gl1[0] + 9:.1f}" y1="{gl1[1] + 6:.1f}" x2="{gl2[0] + 9:.1f}" y2="{gl2[1]:.1f}" stroke="#fff" stroke-opacity=".35" stroke-width="7" stroke-linecap="round"/>')
    if ltext: o.append(f'<g transform="matrix({K} .5 0 1 {TY[0]:.1f} {TY[1]:.1f})">{ltext}</g>')
    if rtext: o.append(f'<g transform="matrix({K} -.5 0 1 {TXY[0]:.1f} {TXY[1]:.1f})">{rtext}</g>')
    if open_:
        o.append(f'<clipPath id="pm-open"><polygon points="{P(T, TX, TXY, TY)}"/></clipPath>')
        o.append(f'<g stroke="#111" stroke-width="{sw}" stroke-linejoin="round">'
                 f'<polygon points="{P(T, TX, TXY, TY)}" fill="#4A000A"/>'
                 f'<polygon clip-path="url(#pm-open)" points="{P(T, TX, add(TX, (0, h * .5)), add(T, (0, h * .5)))}" fill="#7A0012" stroke="none"/>'
                 f'<polygon clip-path="url(#pm-open)" points="{P(T, TY, add(TY, (0, h * .5)), add(T, (0, h * .5)))}" fill="#93001A" stroke="none"/>'
                 f'<g clip-path="url(#pm-open)">{inner}</g>'
                 f'<polygon points="{P(TY, T, add(T, (-.35 * a, -.35 * a)), add(TY, (-.35 * a, -.35 * a)))}" fill="{lidL}"/>'
                 f'<polygon points="{P(T, TX, add(TX, (.35 * a, -.35 * a)), add(T, (.35 * a, -.35 * a)))}" fill="{lidR}"/>'
                 f'<polygon points="{P(TY, TXY, add(TXY, (-.2 * a, .32 * a)), add(TY, (-.2 * a, .32 * a)))}" fill="{lidL}"/>'
                 f'<polygon points="{P(TXY, TX, add(TX, (.2 * a, .32 * a)), add(TXY, (.2 * a, .32 * a)))}" fill="{lidR}"/>'
                 '</g>')
        return ''.join(o)
    if lid:
        a2 = a * over; L = .22 * h
        C2 = (cx, ty + .5 * a - 4)
        T2 = (cx, C2[1] - .5 * a2); X2, Y2, TX2, TY2, TXY2 = G(T2, a2)
        dl = lambda p: (p[0], p[1] + L)
        g = [f'<g class="{"gb-lid" if bow else ""}" stroke="#111" stroke-width="{sw}" stroke-linejoin="round">',
             f'<polygon points="{P(dl(TY2), dl(TXY2), dl(TX2), add(dl(TX2), (0, 7)), add(dl(TXY2), (0, 7)), add(dl(TY2), (0, 7)))}" fill="#111" opacity=".35" stroke="none"/>',
             f'<polygon points="{P(TY2, TXY2, dl(TXY2), dl(TY2))}" fill="{lidL}"/>',
             f'<polygon points="{P(TXY2, TX2, dl(TX2), dl(TXY2))}" fill="{lidR}"/>']
        if rib: g.append(faceribs(TY2, TXY2, a2, dl))
        g.append(f'<polygon points="{P(T2, TX2, TXY2, TY2)}" fill="{top}"/>')
        if rib:
            s1, e1 = add(T2, Y2, .5), add(TX2, Y2, .5)
            g.append(f'<polygon points="{P(add(s1, Yu, -rw / 2), add(e1, Yu, -rw / 2), add(e1, Yu, rw / 2), add(s1, Yu, rw / 2))}" fill="{ribT}"/>')
            s2, e2 = add(T2, X2, .5), add(TY2, X2, .5)
            g.append(f'<polygon points="{P(add(s2, Xu, -rw / 2), add(e2, Xu, -rw / 2), add(e2, Xu, rw / 2), add(s2, Xu, rw / 2))}" fill="{ribT}"/>')
        hl1, hl2 = add(T2, Y2, .3), add(T2, Y2, .55)
        g.append(f'<line x1="{hl1[0] + 6:.1f}" y1="{hl1[1]:.1f}" x2="{hl2[0] + 6:.1f}" y2="{hl2[1]:.1f}" stroke="#fff" stroke-opacity=".55" stroke-width="5" stroke-linecap="round"/>')
        if bow:
            s_ = a / 150
            g.append(BOW.format(x=C2[0], y=C2[1], s=s_, c=bow, k=bowk or bow, sw=sw / s_, sw2=3 / s_))
        g.append('</g>')
        o += g
    return ''.join(o)

def sticker(x, y, t1, t2='', r=-12, R0=30, fill='#fff', tc='#E1001A', fs=15):
    return (f'<g transform="translate({x} {y}) rotate({r})"><circle r="{R0}" fill="{fill}" stroke="#111" stroke-width="4"/>'
            f'<text y="{-2 if t2 else 5}" text-anchor="middle" font-family="Rubik, sans-serif" font-style="italic" font-weight="900" font-size="{fs}" fill="{tc}">{t1}</text>'
            + (f'<text y="13" text-anchor="middle" font-family="Rubik, sans-serif" font-weight="800" font-size="9" fill="#111">{t2}</text>' if t2 else '') + '</g>')

def burst(x, y, size, fill, t, tc='#fff', r=12, fs=14):
    pts = '50,2 59,22 80,10 76,33 98,36 82,52 96,70 73,72 74,95 55,83 42,100 36,78 14,88 19,65 0,55 18,42 6,22 29,24 30,2 43,20'
    lines = t.split('|')
    tx = ''.join(f'<text x="50" y="{50 + (i - (len(lines) - 1) / 2) * fs * 1.0 + fs * .35:.1f}" text-anchor="middle" font-family="Rubik, sans-serif" font-style="italic" font-weight="900" font-size="{fs}" fill="{tc}">{l}</text>' for i, l in enumerate(lines))
    return f'<g transform="translate({x} {y}) rotate({r}) scale({size / 100}) translate(-50 -50)"><polygon points="{pts}" fill="{fill}" stroke="#111" stroke-width="3" stroke-linejoin="round"/>{tx}</g>'

SPARKS = ('<g fill="#FFDA1A" stroke="#111" stroke-width="3.5" stroke-linejoin="round">'
          '<path class="gb-spark" d="M360 70 l7 18 18 7 -18 7 -7 18 -7 -18 -18 -7 18 -7z"/>'
          '<path class="gb-spark s2" d="M36 100 l5 13 13 5 -13 5 -5 13 -5 -13 -13 -5 13 -5z" fill="#fff"/>'
          '<path class="gb-spark s3" d="M372 270 l5 12 12 5 -12 5 -5 12 -5 -12 -12 -5 12 -5z"/>'
          '<path class="gb-spark s2" d="M24 300 l6 15 15 6 -15 6 -6 15 -6 -15 -15 -6 15 -6z"/></g>')
SHADOW = '<ellipse class="gb-shadow" cx="208" cy="{y}" rx="{rx}" ry="18" fill="#111" opacity=".22"/>'
J = 'font-family="Jost, Futura, sans-serif"'
R = 'font-family="Rubik, sans-serif"'
BAR = ''.join(f'<rect x="{x}" y="0" width="{w}" height="16"/>' for x, w in ((0, 2), (4, 1), (7, 3), (12, 1), (15, 2), (19, 1), (22, 3), (27, 1), (30, 2)))
def svg(body, vb='0 0 400 420'): return f'<svg viewBox="{vb}" aria-hidden="true">{body}</svg>'

import math
V = {}
PLATE = lambda w, fs, txt='posta': f'<rect x="{(150 - w) / 2}" y="56" width="{w}" height="36" fill="#FFDA1A" stroke="#111" stroke-width="4"/><text x="75" y="81" text-anchor="middle" {J} font-weight="900" font-size="{fs}" letter-spacing="-1" fill="#111">{txt}</text>'
PRICE = lambda c='#111': f'<text x="18" y="80" {J} font-weight="900" font-size="42" letter-spacing="-2" fill="{c}">390<tspan font-size="16" dy="-19">,–₽</tspan></text>'

# Изо: красно-золотая, наклейка NEW, бант с хвостами
V['3'] = svg(SHADOW.format(y=394, rx=150) + '<g class="gb-float">'
    + iso(200, 64, 150, 135, '#FFDA1A', '#E1001A', '#F2B800', ('#FFDA1A', '#FFDA1A', '#E1001A'), '#C40018', '#D9A400', bow='#E1001A', bowk='#B8001A',
          ltext=PLATE(130, 23) + f'<text x="75" y="118" text-anchor="middle" {R} font-style="italic" font-weight="900" font-size="9.5" fill="#fff" stroke="#111" stroke-width="3" paint-order="stroke">АВТОПИЛОТ ДЛЯ КАНАЛОВ</text>',
          rtext=PRICE() + f'<text x="20" y="98" {J} font-weight="800" font-size="10" letter-spacing="2" fill="#111">AUTÖPILÖT</text><g fill="#111" transform="translate(20 108)">{BAR}</g>')
    + sticker(64, 196, 'NEW!', '2026', r=-14) + '</g>' + SPARKS)

# Ценник 3D: жёлтая с чёрным, висящая бирка на банте, штамп на крышке
tag = (f'<g class="gb-tag"><path d="M214 128 Q262 112 292 150" fill="none" stroke="#111" stroke-width="2.5"/>'
       f'<g transform="translate(292 150) rotate(16)"><polygon points="-30,8 -18,-4 30,-4 30,62 -30,62" fill="#E1001A" stroke="#111" stroke-width="3.5" stroke-linejoin="round" transform="translate(4 4)" opacity=".0"/>'
       f'<polygon points="-26,10 -14,-2 26,-2 26,58 -26,58" fill="#FFDA1A" stroke="#111" stroke-width="3.5" stroke-linejoin="round"/><circle cx="0" cy="8" r="4" fill="#fff" stroke="#111" stroke-width="2"/>'
       f'<text x="-20" y="28" {J} font-weight="800" font-size="7.5" letter-spacing="1" fill="#111">AUTÖPILÖT</text>'
       f'<text x="-20" y="50" {J} font-weight="900" font-size="20" letter-spacing="-1" fill="#111">390<tspan font-size="8" dy="-8">,–₽</tspan></text></g></g>')
V['4'] = svg(SHADOW.format(y=394, rx=150) + '<g class="gb-float">'
    + '<path class="gb-light" d="M200 120 L56 -30 H344 Z" fill="#FFF6B0" opacity=".85"/>'
    + iso(200, 64, 150, 135, '#FFE45C', '#FFDA1A', '#F2C200', '#111', '#F5CC00', '#E0B000', bow='#111', bowk='#111',
          ltext=f'<rect x="10" y="56" width="130" height="36" fill="#111"/><text x="75" y="81" text-anchor="middle" {J} font-weight="900" font-size="23" letter-spacing="-1" fill="#FFDA1A">posta</text>'
                f'<g fill="#111" transform="translate(14 112)">{BAR}</g><text x="136" y="126" text-anchor="end" {J} font-weight="700" font-size="7.5" fill="#111">арт. 390.007.24</text>',
          rtext=f'<rect x="16" y="48" width="118" height="50" fill="#fff" stroke="#111" stroke-width="4"/><text x="75" y="88" text-anchor="middle" {J} font-weight="900" font-size="38" letter-spacing="-2" fill="#111">390<tspan font-size="15" dy="-17">,–₽</tspan></text>'
                f'<text x="75" y="120" text-anchor="middle" {J} font-weight="900" font-size="12" letter-spacing="2" fill="#111">NY LÅG PRIS!</text>')
    + tag + '</g>' + SPARKS)

# Распаковка: лучи, конфетти, вылетают TG/VK и плашка
rays = ''.join(f'<path d="M200 150 L{200 + 270 * math.cos(t):.0f} {150 + 270 * math.sin(t):.0f} L{200 + 270 * math.cos(t + .17):.0f} {150 + 270 * math.sin(t + .17):.0f} Z"/>' for t in [i * .4 - 3.1 for i in range(8)])
conf = ''.join(f'<rect x="{x}" y="{y}" width="9" height="5" fill="{c}" transform="rotate({r} {x} {y})"/>' for x, y, c, r in
               ((96, 60, '#E1001A', 30), (140, 30, '#1546C8', -20), (270, 36, '#00A651', 50), (318, 78, '#E1001A', -35), (110, 130, '#FFDA1A', 70), (300, 136, '#1546C8', 15), (180, 14, '#FFDA1A', -60), (236, 10, '#E1001A', 10), (70, 100, '#00A651', 20), (340, 112, '#FFDA1A', -15)))
chip = lambda x, y, t, c, r: f'<g transform="translate({x} {y}) rotate({r})"><rect x="-22" y="-12" width="44" height="24" rx="12" fill="{c}" stroke="#111" stroke-width="3"/><text y="5" text-anchor="middle" {R} font-weight="900" font-size="12" fill="#fff">{t}</text></g>'
glow = '<ellipse cx="200" cy="232" rx="90" ry="48" fill="url(#pm-glow)"/>'
V['5'] = svg(SHADOW.format(y=402, rx=150)
    + f'<g class="gb-rays" fill="#FFDA1A" opacity=".55">{rays}</g>'
    + iso(200, 152, 150, 120, None, '#E1001A', '#B8001A', '#FFDA1A', '#FF3B4E', '#C40018', open_=True, inner=glow,
          ltext=f'<text x="75" y="100" text-anchor="middle" {R} font-style="italic" font-weight="900" font-size="12" fill="#fff" stroke="#111" stroke-width="3" paint-order="stroke">ВАШ АВТОПИЛОТ</text>',
          rtext=f'<text x="75" y="62" text-anchor="middle" {J} font-weight="900" font-size="13" letter-spacing="2" fill="#fff">ÖPPNA MIG!</text>')
    + f'<g class="gb-pop"><g class="gb-confetti" stroke="#111" stroke-width="1.5">{conf}</g>'
    + chip(110, 168, 'TG', '#2AABEE', -14) + chip(296, 172, 'VK', '#0077FF', 12)
    + f'<g transform="rotate(-6 200 100)"><rect x="98" y="72" width="212" height="62" fill="#111"/><rect x="90" y="64" width="212" height="62" fill="#FFDA1A" stroke="#111" stroke-width="5"/>'
      f'<text x="196" y="107" text-anchor="middle" {J} font-weight="900" font-size="40" letter-spacing="-2" fill="#111">posta<tspan font-size="18" dy="-16">,–</tspan></text></g></g>'
    + burst(340, 236, 78, '#E1001A', 'ВАУ!', fs=17, r=14) + SPARKS)

# Как в рекламе: тумблер щёлкает при наведении, ценник-звезда, надпись на боку
V['6'] = svg(SHADOW.format(y=402, rx=140) + '<g class="gb-float"><g stroke="#111" stroke-width="5" stroke-linejoin="round">'
    '<rect x="100" y="76" width="240" height="320" fill="#111" stroke="none" transform="translate(10 10)"/>'
    '<polygon points="340,70 380,46 380,366 340,390" fill="#0E2F8C"/>'
    '<polygon points="100,70 140,46 380,46 340,70" fill="#3C6BF0"/>'
    '<rect x="100" y="70" width="240" height="320" fill="#1546C8"/>'
    '<polygon points="100,70 196,70 100,166" fill="#FFDA1A"/><polygon points="100,166 196,70 226,70 100,196" fill="#E1001A"/>'
    '<rect x="130" y="176" width="180" height="128" rx="18" fill="#BFE3FF"/>'
    '<rect x="176" y="202" width="88" height="78" rx="14" fill="#FFDA1A"/><rect x="198" y="214" width="44" height="54" rx="22" fill="#111"/>'
    '<circle class="gb-knob" cx="220" cy="250" r="16" fill="#888"/>'
    '<path d="M144 196 L170 186 M144 214 L196 194 M290 290 L300 280" stroke="#fff" stroke-width="5" stroke-linecap="round" fill="none"/>'
    '<rect x="96" y="326" width="248" height="34" fill="#E1001A" transform="rotate(-3 220 343)"/></g>'
    '<path d="M108 74 V384" stroke="#fff" stroke-opacity=".22" stroke-width="8" stroke-linecap="round"/>'
    f'<text transform="matrix(0 -1 1 -.6 366 330)" {J} font-weight="900" font-size="22" letter-spacing="2" fill="#FFDA1A">POSTA</text>'
    f'<text x="220" y="349" text-anchor="middle" transform="rotate(-3 220 343)" {R} font-style="italic" font-weight="900" font-size="17" fill="#fff">КАК В РЕКЛАМЕ!</text>'
    f'<rect x="196" y="98" width="128" height="40" fill="#FFDA1A" stroke="#111" stroke-width="4"/><text x="260" y="127" text-anchor="middle" {J} font-weight="900" font-size="26" letter-spacing="-1.2" fill="#111">posta</text>'
    f'<text x="220" y="160" text-anchor="middle" {R} font-weight="900" font-size="12" letter-spacing="1" fill="#fff">АВТОПИЛОТ ДЛЯ КАНАЛОВ</text>'
    f'<text class="gb-auto" x="220" y="296" text-anchor="middle" {J} font-weight="900" font-size="13" letter-spacing="2" fill="#111">ВЫКЛ</text>'
    f'<text x="220" y="380" text-anchor="middle" {J} font-weight="700" font-size="9" letter-spacing="1" fill="#BFD0FF">TELEGRAM · ВКОНТАКТЕ · АНАЛОГОВ НЕТ*</text>'
    + sticker(118, 300, 'NEW!', '2026', r=-14)
    + burst(326, 84, 124, '#FFDA1A', '390,–₽|в месяц', tc='#111', fs=11, r=10)
    + '</g>' + SPARKS)

# Тарифы: стопка, у средней — «выбор админов»
big = iso(200, 204, 150, 110, '#3C6BF0', '#1546C8', '#0E2F8C', '#FFDA1A', '#2A55D8', '#0B2A80',
          ltext=f'<rect x="18" y="30" width="114" height="58" fill="#fff" stroke="#111" stroke-width="4"/><text x="75" y="52" text-anchor="middle" {J} font-weight="900" font-size="16" letter-spacing="1" fill="#1546C8">SJÄTKÅ</text><text x="75" y="78" text-anchor="middle" {J} font-weight="900" font-size="22" fill="#111">990,–₽</text>',
          rtext=f'<text x="75" y="70" text-anchor="middle" {J} font-weight="900" font-size="11" letter-spacing="1" fill="#fff">5 КАНАЛОВ</text>')
cB = 204 + 75
mid = iso(200, cB - 80 - 55 - 4, 110, 80, '#FF4757', '#E1001A', '#B8001A', '#FFDA1A', '#E1001A', '#B8001A',
          ltext=f'<rect x="12" y="22" width="86" height="46" fill="#fff" stroke="#111" stroke-width="3.5"/><text x="55" y="40" text-anchor="middle" {J} font-weight="900" font-size="12" fill="#E1001A">AUTÖPILÖT</text><text x="55" y="61" text-anchor="middle" {J} font-weight="900" font-size="18" fill="#111">390,–₽</text>')
cM = cB - 84
top = iso(200, cM - 56 - 35 - 4, 70, 56, '#FFE45C', '#FFDA1A', '#F2C200', '#E1001A', '#F5CC00', '#E0B000', bow='#E1001A', bowk='#C40018', sw=4,
          ltext=f'<rect x="6" y="14" width="58" height="34" fill="#fff" stroke="#111" stroke-width="3"/><text x="35" y="28" text-anchor="middle" {J} font-weight="900" font-size="8.5" fill="#111">BESPLÄTT</text><text x="35" y="43" text-anchor="middle" {J} font-weight="900" font-size="12" fill="#111">0,–₽</text>')
V['7'] = svg(SHADOW.format(y=406, rx=150) + '<g class="gb-float">' + big + mid + top
    + burst(318, 200, 92, '#E1001A', 'ВЫБОР|АДМИНОВ', fs=11, r=12) + '</g>' + SPARKS)

if __name__ == '__main__':
    import sys
    idx = sys.argv[1]
    page = open(idx, encoding='utf8').read()
    m = re.search(r'(<div class="giftbox"[^>]*>)\s*(<svg viewBox="0 0 480 540".*?</svg>)', page, flags=re.S)
    order = [('3', 'Изо')]
    stack = ''.join(V[k].replace('<svg ', f'<svg data-v="{k}"' + ('' if i == 0 else ' hidden') + ' ', 1) for i, (k, _) in enumerate(order))
    page = page.replace(m.group(0), '<div class="giftbox" role="img" aria-label="Коробка с товаром Posta">' + stack)
    chips = '<div class="gb-pick" role="group" aria-label="Вариант коробки">' + ''.join(f'<button type="button" data-v="{k}" aria-pressed="{str(i == 0).lower()}">{n}</button>' for i, (k, n) in enumerate(order)) + '</div>'
    if len(order) > 1: page = page.replace('\n  </div>\n</header>', '\n  </div>\n  ' + chips + '\n</header>', 1)
    defs = ('<defs><pattern id="pm-dots" width="8" height="8" patternUnits="userSpaceOnUse"><circle cx="4" cy="4" r="1.6" fill="#111" opacity=".22"/></pattern>'
            '<radialGradient id="pm-glow"><stop offset="0" stop-color="#FFF6B0"/><stop offset=".55" stop-color="#FFDA1A" stop-opacity=".55"/><stop offset="1" stop-color="#FFDA1A" stop-opacity="0"/></radialGradient></defs>')
    page = page.replace('<symbol id="i-ph"', defs + '<symbol id="i-ph"', 1)
    css = '''
/* Варианты коробки и переключатель */
.giftbox > svg[hidden] { display: none }
.gb-pick { grid-column: 2; display: flex; flex-wrap: wrap; gap: 6px; justify-content: center }
.gb-pick button { font: 700 13px/1 var(--body); padding: 8px 11px; border-radius: 999px; border: 2px solid var(--outline); background: var(--paper); color: var(--ink); cursor: pointer }
.gb-pick button[aria-pressed="true"] { background: var(--yellow); color: #111; border-color: #111; box-shadow: 2px 2px 0 var(--outline) }
.gb-pick button:focus-visible { outline: 3px solid var(--blue); outline-offset: 2px }
.giftbox:hover .gb-lid { transform: translateY(-28px) }
.gb-light { opacity: 0; transition: opacity .3s }
.giftbox:hover .gb-light { opacity: 1 }
.gb-pop { animation: gbpop 2.4s ease-in-out infinite }
@keyframes gbpop { 50% { transform: translateY(-14px) } }
.gb-rays { transform-box: fill-box; transform-origin: center; animation: spin 24s linear infinite }
.gb-tag { transform-box: view-box; transform-origin: 214px 128px; animation: swing 2.6s ease-in-out infinite }
@keyframes swing { 50% { transform: rotate(6deg) } }
.gb-knob { transition: transform .25s, fill .25s }
.giftbox:hover .gb-knob { transform: translateY(-20px); fill: #00A651 }
.gb-auto { transition: opacity .2s }
.giftbox:hover .gb-auto { opacity: 0 }
@media (max-width: 760px) { .gb-pick { grid-column: 1 } }
</style>'''
    page = page.replace('\n</style>', css, 1)
    js = """
const boxes = document.querySelectorAll('.giftbox > svg'), picks = document.querySelectorAll('.gb-pick button');
picks.forEach(b => b.addEventListener('click', () => {
  boxes.forEach(s => s.toggleAttribute('hidden', s.dataset.v !== b.dataset.v));
  picks.forEach(p => p.setAttribute('aria-pressed', String(p === b)));
}));
</script>"""
    if len(order) > 1: page = page.replace('\n</script>', js, 1)
    open(idx, 'w', encoding='utf8').write(page)
    print('variants:', len(order))
