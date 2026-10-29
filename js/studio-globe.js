// studio-globe.js — «смотровое окно» студии: вращающийся точечный глобус (canvas) с метками отобранных постов
// и всплывающими карточками, привязанными к региону источника. Глобус можно крутить мышью/пальцем.
import { LAND } from './studio-land.js';

const RAD = Math.PI / 180;
const REDUCED = matchMedia('(prefers-reduced-motion: reduce)').matches;
const MAX_CARDS = 3;
const CYCLE_MS = 4200;      // как часто всплывает следующая карточка
const IDLE_SPIN = -0.045;   // рад/с: поверхность едет слева направо, как у настоящей Земли

const wrap = a => Math.atan2(Math.sin(a), Math.cos(a));
const clamp = (x, lo, hi) => Math.min(hi, Math.max(lo, x));

/**
 * host — контейнер (position: relative). cardFor(pin) → DOM-элемент карточки, onOpen(item) — щелчок по карточке.
 * Возвращает { setItems(items) } — items: [{ key, lat, lon, color, item }] в порядке «новые сверху».
 */
export function createGlobe(host, { cardFor, onOpen }) {
    const canvas = Object.assign(document.createElement('canvas'), { className: 'globe-canvas' });
    const layer = Object.assign(document.createElement('div'), { className: 'globe-cards' });
    host.append(canvas, layer);
    const ctx = canvas.getContext('2d');

    const n = LAND.length / 2;
    const dotSin = new Float32Array(n), dotCos = new Float32Array(n), dotLon = new Float32Array(n);
    for (let i = 0; i < n; i++) {
        const lat = LAND[2 * i] / 2 * RAD;
        dotSin[i] = Math.sin(lat); dotCos[i] = Math.cos(lat); dotLon[i] = LAND[2 * i + 1] / 2 * RAD;
    }

    const view = { lon: 50 * RAD, lat: 40 * RAD, tLon: null, tLat: 40 * RAD, dragging: false, idleUntil: 0 };
    const size = { w: 0, h: 0, r: 0, cx: 0, cy: 0, dpr: 1 };
    const pins = new Map();          // key → { lat, lon, color, item, born }
    const cards = [];                // { key, node, born, side }
    const fresh = [];                // ключи новых постов: показать первыми
    let order = [], cursor = 0, lastCycle = 0, first = true, raf = 0, visible = true, last = performance.now();

    function project(lat, lon, alt = 1) {
        const cl = Math.cos(lat), d = lon - view.lon;
        const x = cl * Math.sin(d);
        const y = Math.cos(view.lat) * Math.sin(lat) - Math.sin(view.lat) * cl * Math.cos(d);
        const z = Math.sin(view.lat) * Math.sin(lat) + Math.cos(view.lat) * cl * Math.cos(d);
        return { x: size.cx + size.r * alt * x, y: size.cy - size.r * alt * y, z };
    }

    function resize() {
        const box = host.getBoundingClientRect();
        size.dpr = Math.min(2, devicePixelRatio || 1);
        size.w = box.width; size.h = box.height;
        canvas.width = Math.round(box.width * size.dpr); canvas.height = Math.round(box.height * size.dpr);
        size.r = Math.min(box.width * 0.36, box.height * 0.42);
        size.cx = box.width / 2; size.cy = box.height * 0.52;
    }

    // ---- выбор следующей карточки и поворот к ней ----
    function cycle(now) {
        lastCycle = now;
        let key = fresh.shift();
        while (key && !pins.has(key)) key = fresh.shift();
        if (!key) {
            const keys = order.filter(k => pins.has(k) && !cards.some(c => c.key === k));
            if (!keys.length) return;
            key = keys[cursor++ % keys.length];
        }
        const pin = pins.get(key);
        view.tLon = pin.lon;
        view.tLat = clamp(pin.lat * 0.85, -20 * RAD, 52 * RAD);
        const node = cardFor(pin);
        node.classList.add('globe-card');
        node.addEventListener('click', () => onOpen(pin.item));
        layer.append(node);
        void node.offsetWidth;   // применить начальное состояние, чтобы сработал переход появления
        node.classList.add('is-in');
        cards.push({ key, node, born: now, side: cards.length % 2 ? -1 : 1 });
        while (cards.length > MAX_CARDS) dropCard(cards[0]);
    }

    function dropCard(c) {
        cards.splice(cards.indexOf(c), 1);
        c.node.classList.remove('is-in');
        setTimeout(() => c.node.remove(), 400);
    }

    // ---- кадр ----
    function frame(now) {
        raf = 0;
        const dt = Math.min(0.05, (now - last) / 1000);
        last = now;
        if (!view.dragging) {
            if (view.tLon !== null && now > view.idleUntil) {
                const k = REDUCED ? 1 : 1 - Math.exp(-dt * 2.2);
                const dl = wrap(view.tLon - view.lon);
                view.lon += dl * k;
                view.lat += (view.tLat - view.lat) * k;
                if (Math.abs(dl) < 0.002) view.tLon = null;
            } else if (now > view.idleUntil && !REDUCED) {
                view.lon += IDLE_SPIN * dt;
            }
        }
        draw(now);
        if (visible && !REDUCED) raf = requestAnimationFrame(frame);
    }

    function draw(now) {
        const { r, cx, cy, dpr } = size;
        ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
        ctx.clearRect(0, 0, size.w, size.h);
        if (r <= 0) return;

        // атмосфера и сфера
        const halo = ctx.createRadialGradient(cx, cy, r * 0.9, cx, cy, r * 1.35);
        halo.addColorStop(0, 'rgba(124, 92, 255, .38)'); halo.addColorStop(0.4, 'rgba(79, 209, 255, .10)'); halo.addColorStop(1, 'rgba(79, 209, 255, 0)');
        ctx.fillStyle = halo; ctx.beginPath(); ctx.arc(cx, cy, r * 1.35, 0, Math.PI * 2); ctx.fill();
        const body = ctx.createRadialGradient(cx - r * 0.35, cy - r * 0.4, r * 0.1, cx, cy, r);
        body.addColorStop(0, '#20265a'); body.addColorStop(0.6, '#111433'); body.addColorStop(1, '#090b1c');
        ctx.fillStyle = body; ctx.beginPath(); ctx.arc(cx, cy, r, 0, Math.PI * 2); ctx.fill();
        ctx.strokeStyle = 'rgba(160, 140, 255, .55)'; ctx.lineWidth = 1; ctx.stroke();

        // точки суши: обратная сторона едва видна — «стеклянный» шар
        const cl0 = Math.cos(view.lat), sl0 = Math.sin(view.lat);
        const buckets = [[], [], [], []];
        for (let i = 0; i < n; i++) {
            const d = dotLon[i] - view.lon, cd = Math.cos(d), c = dotCos[i];
            const z = sl0 * dotSin[i] + cl0 * c * cd;
            const x = cx + r * c * Math.sin(d), y = cy - r * (cl0 * dotSin[i] - sl0 * c * cd);
            buckets[z < 0 ? 0 : z < 0.35 ? 1 : z < 0.7 ? 2 : 3].push(x, y);
        }
        const styles = ['rgba(140, 150, 255, .07)', 'rgba(130, 170, 255, .35)', 'rgba(150, 190, 255, .62)', 'rgba(185, 215, 255, .9)'];
        buckets.forEach((b, k) => {
            ctx.fillStyle = styles[k];
            const s = k ? 1.7 : 1.2;
            for (let i = 0; i < b.length; i += 2) ctx.fillRect(b[i] - s / 2, b[i + 1] - s / 2, s, s);
        });

        // метки постов
        const featured = cards.length ? cards[cards.length - 1].key : '';
        for (const [key, p] of pins) {
            const pt = project(p.lat, p.lon);
            if (pt.z < 0) continue;
            const age = (now - p.born) / 1000;
            ctx.globalAlpha = 0.35 + 0.65 * pt.z;
            if (age < 3) {   // новый пост: луч из точки и расходящееся кольцо
                const top = project(p.lat, p.lon, 1 + 0.28 * Math.min(1, age * 1.5));
                const g = ctx.createLinearGradient(pt.x, pt.y, top.x, top.y);
                g.addColorStop(0, p.color); g.addColorStop(1, 'rgba(255,255,255,0)');
                ctx.strokeStyle = g; ctx.lineWidth = 2;
                ctx.beginPath(); ctx.moveTo(pt.x, pt.y); ctx.lineTo(top.x, top.y); ctx.stroke();
                ctx.strokeStyle = p.color; ctx.lineWidth = 1.5; ctx.globalAlpha *= 1 - age / 3;
                ctx.beginPath(); ctx.arc(pt.x, pt.y, 4 + age * 9, 0, Math.PI * 2); ctx.stroke();
                ctx.globalAlpha = 0.35 + 0.65 * pt.z;
            }
            const big = key === featured;
            const rr = big ? 4 + Math.sin(now / 260) * 1.2 : 2.6;
            ctx.shadowColor = p.color; ctx.shadowBlur = big ? 14 : 6;
            ctx.fillStyle = p.color;
            ctx.beginPath(); ctx.arc(pt.x, pt.y, rr, 0, Math.PI * 2); ctx.fill();
            ctx.shadowBlur = 0;
        }
        ctx.globalAlpha = 1;

        // карточки: возле своей метки, с выноской; ушедшая за горизонт — гаснет
        cards.forEach((c, i) => {
            const p = pins.get(c.key);
            if (!p) return;
            const pt = project(p.lat, p.lon);
            const w = c.node.offsetWidth || 170, h = c.node.offsetHeight || 120;
            const side = pt.x > cx ? 1 : pt.x < cx - 4 ? -1 : c.side;
            const lift = 40 + (cards.length - 1 - i) * 26;
            let x = pt.x + side * 46 - (side < 0 ? w : 0);
            let y = pt.y - lift - h / 2;
            x = clamp(x, 6, size.w - w - 6); y = clamp(y, 6, size.h - h - 6);
            const shown = pt.z > 0.08;
            c.node.style.transform = `translate(${x.toFixed(1)}px, ${y.toFixed(1)}px)`;
            c.node.style.opacity = shown ? '' : '0';
            c.node.style.pointerEvents = shown ? '' : 'none';
            c.node.classList.toggle('is-featured', i === cards.length - 1);
            if (!shown) return;
            const ax = side > 0 ? x : x + w, ay = y + h / 2;
            ctx.strokeStyle = p.color; ctx.globalAlpha = 0.75 * pt.z; ctx.lineWidth = 1;
            ctx.setLineDash([3, 3]);
            ctx.beginPath(); ctx.moveTo(pt.x, pt.y); ctx.lineTo(pt.x + side * 22, ay); ctx.lineTo(ax, ay); ctx.stroke();
            ctx.setLineDash([]);
        });
        ctx.globalAlpha = 1;
    }

    const kick = () => { if (!raf && visible) { last = performance.now(); raf = requestAnimationFrame(frame); } };

    // ---- управление: перетаскивание ----
    let drag = null;
    canvas.addEventListener('pointerdown', e => {
        drag = { x: e.clientX, y: e.clientY, lon: view.lon, lat: view.lat };
        view.dragging = true; view.tLon = null;
        canvas.setPointerCapture(e.pointerId);
        host.classList.add('is-dragging');
    });
    canvas.addEventListener('pointermove', e => {
        if (!drag) return;
        view.lon = drag.lon - (e.clientX - drag.x) / size.r;
        view.lat = clamp(drag.lat + (e.clientY - drag.y) / size.r, -60 * RAD, 75 * RAD);
        if (REDUCED) draw(performance.now());
    });
    const release = () => {
        if (!drag) return;
        drag = null; view.dragging = false; view.idleUntil = performance.now() + 5000;
        host.classList.remove('is-dragging');
    };
    canvas.addEventListener('pointerup', release);
    canvas.addEventListener('pointercancel', release);

    new ResizeObserver(() => { resize(); draw(performance.now()); }).observe(host);
    new IntersectionObserver(([e]) => { visible = e.isIntersecting; kick(); }).observe(host);
    document.addEventListener('visibilitychange', () => { last = performance.now(); });
    resize();
    kick();
    // смена карточек — по таймеру, а не в кадре: не зависит от частоты requestAnimationFrame
    setInterval(() => {
        const t = performance.now();
        if (visible && !document.hidden && pins.size && t - lastCycle > CYCLE_MS && t > view.idleUntil) cycle(t);
        if (REDUCED) frame(t);
    }, REDUCED ? 1000 : 400);

    return {
        setItems(items) {
            const now = performance.now();
            const keys = new Set(items.map(i => i.key));
            for (const k of pins.keys()) if (!keys.has(k)) pins.delete(k);
            for (const c of [...cards]) if (!keys.has(c.key)) dropCard(c);
            for (const it of [...items].reverse()) {
                if (pins.has(it.key)) continue;
                pins.set(it.key, { ...it, born: first ? -1e9 : now });
                if (!first) fresh.unshift(it.key);
            }
            order = items.map(i => i.key);
            if (first && items.length) { first = false; lastCycle = now - CYCLE_MS + 600; }
            else if (first) first = false;
            if (fresh.length) lastCycle = Math.min(lastCycle, now - CYCLE_MS + 300);   // новый пост — показать сразу
            kick();
        },
    };
}
