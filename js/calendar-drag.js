// calendar-drag.js — жесты календаря: перенос и копия поста (мышь — сразу, палец — после удержания), траектория,
// плашка сдвига, прилипающие отметки; фон: сдвиг по дням и часам одновременно, щипок. Геометрию и запросы даёт host из calendar.js.
import { el } from './core.js';
import { addDays, panAxis, shiftLabel, snapTarget, MIN } from './calendar-model.js';

const MOUSE_START_PX = 5;      // мышь: перенос начинается после сдвига на столько пикселей
const TOUCH_HOLD_MS = 350;     // палец: удержание без сдвига до переноса
const TOUCH_SLOP_PX = 8;       // палец: сдвиг раньше удержания — это прокрутка, а не перенос
const EDGE_PX = 48;            // у края календаря вид сдвигается сам
const EDGE_STEP_MS = 600;
const PAN_START_PX = 3;        // фон: сдвиг, с которого это уже сдвиг, а не клик
const AXIS_DEADZONE_PX = 12;   // фон: по каждой оси первые пиксели не двигают вид — дрожь руки не сдвигает часы
const SVG_NS = 'http://www.w3.org/2000/svg';
const STICK_WORD = { slot: 'к слоту расписания', hour: 'к часу', '10min': 'к 10 минутам', round: 'к минутам' };

let drag = null;   // активный перенос: {post, card, host, origin, began, target, ...}

/** Перенос поста: мышь — со сдвига на 5 px, палец — после удержания; sent, sending, telegram не двигаются. */
export function startDrag(event, post, card, host) {
    if (drag || event.button !== 0 || !['queued', 'failed'].includes(post.status)) return;
    const touch = event.pointerType === 'touch';
    drag = { post, card, host, touch, origin: post.at * 1000, x0: event.clientX, y0: event.clientY, last: event, began: false, copy: false,
        target: null, ghost: null, line: null, marks: null, edgeDir: 0, edgeTimer: 0, vEdgeDir: 0, vEdgeTimer: 0, holdTimer: 0, lastKind: null, lastDay: null };
    if (touch) drag.holdTimer = setTimeout(() => drag && begin(drag), TOUCH_HOLD_MS);
    document.addEventListener('pointermove', onMove);
    document.addEventListener('pointerup', onUp);
    document.addEventListener('pointercancel', cancelDrag);
    document.addEventListener('keydown', onKey);
    document.addEventListener('keyup', onKey);
}

/** Esc или отмена указателя: перенос снимается, пост остаётся на месте. */
function cancelDrag() {
    const d = drag;
    if (!d) return;
    if (d.began) return finishBack(d, () => d.host.status('Перенос отменён.'));
    cleanup();
}

/** Перенос из Предложки: без исходного времени; канал — первый включённый (как у черновика). Отпускание — host.commitPool. */
export function startPoolDrag(event, item, node, host) {
    if (drag || event.button !== 0) return;
    const channel = host.defaultChannel();
    if (!channel) return host.status('Включите канал в календаре, чтобы ставить пост.', true);
    drag = { post: { channel, status: 'queued', text: item.text || '' }, kind: 'pool', item, card: node, host, touch: false, origin: null,
        x0: event.clientX, y0: event.clientY, last: event, began: false, copy: false, target: null, ghost: null, line: null, marks: null,
        edgeDir: 0, edgeTimer: 0, vEdgeDir: 0, vEdgeTimer: 0, holdTimer: 0, lastKind: null, lastDay: null };
    document.addEventListener('pointermove', onMove);
    document.addEventListener('pointerup', onUp);
    document.addEventListener('pointercancel', cancelDrag);
    document.addEventListener('keydown', onKey);
    document.addEventListener('keyup', onKey);
}

export const isDragging = () => drag !== null;

function onKey(event) {
    if (!drag) return;
    if (event.key === 'Escape') return cancelDrag();
    if (drag.began && drag.kind !== 'pool') {   // Ctrl или Alt, нажатые во время переноса, меняют его на копию (из Предложки — не нужно)
        drag.copy = event.ctrlKey || event.altKey;
        update(drag);
    }
}

function onMove(event) {
    const d = drag;
    if (!d) return;
    if (!d.began) {
        const moved = Math.hypot(event.clientX - d.x0, event.clientY - d.y0);
        if (d.touch) {
            if (moved > TOUCH_SLOP_PX) cleanup();   // палец сдвинули раньше удержания — прокрутка
            return;
        }
        if (moved < MOUSE_START_PX) return;
        begin(d);
    }
    d.last = event;
    d.copy = event.ctrlKey || event.altKey;
    update(d);
}

async function onUp(event) {
    const d = drag;
    if (!d) return;
    const copy = d.kind !== 'pool' && (d.copy || event.ctrlKey || event.altKey);
    if (d.began) {
        d.last = event;
        d.copy = copy;
        update(d);
    }
    const { began, target, host, post, kind, item, zone } = d;
    if (!began) { cleanup(); return; }   // без движения — обычный клик откроет черновик
    host.markMoved();
    // Зона проверяется первой: над Предложкой или мимо сетки — возврат, без сообщений об ошибке
    if (zone !== 'grid' || !target) {
        if (zone === 'pool' && kind !== 'pool' && !copy && ['queued', 'failed', 'telegram'].includes(post.status)) return finishBack(d, () => host.unschedule(post));
        const note = kind === 'pool' || zone === 'pool' ? 'Пост вернулся в Предложку.' : `Пост остался на ${host.hhmm(d.origin)}.`;
        return finishBack(d, () => host.status(note));
    }
    cleanup();
    if (kind === 'pool') return host.commitPool(item, target.at, post.channel);
    if (post.status !== 'queued' && !copy) return host.status('Перенести можно только запланированный пост. Копировать — с Ctrl или Alt.', true);
    if (target.at < Date.now() + MIN) return host.status('Раньше, чем через минуту, пост не запланировать.', true);
    if (target.at === d.origin) return;
    await host.commit(post, target.at, copy);
}

function begin(d) {
    if (d.began) return;
    d.began = true;
    clearTimeout(d.holdTimer);
    d.host.markMoved();
    d.host.setDragging(true);
    d.card.classList.add('is-dragging');
    const box = d.card.getBoundingClientRect();
    d.grab = { dx: d.x0 - box.left, dy: d.y0 - box.top };
    document.getElementById('cal-pool-col')?.classList.add('is-drop-zone');
    d.was = d.kind === 'pool' ? null : el('div', { className: 'cal-was', 'aria-hidden': 'true' }, el('span', { textContent: `было: ${d.host.hhmm(d.origin)}` }));
    if (d.was) Object.assign(d.was.style, { left: `${box.left}px`, top: `${box.top}px`, width: `${box.width}px`, height: `${box.height}px` });
    d.preview = d.card.cloneNode(true);   // карточка, которую тащат: рамка, тень и наклон −1.5°
    d.preview.classList.add('cal-drag-card');
    d.preview.style.width = `${box.width}px`;
    d.preview.style.height = `${box.height}px`;
    d.ghost = el('div', { className: 'cal-ghost', 'aria-hidden': 'true' });
    d.line = document.createElementNS(SVG_NS, 'line');
    d.line.setAttribute('class', 'cal-trail-line');
    d.line.style.stroke = d.host.colorOf(d.post.channel);
    const svg = document.createElementNS(SVG_NS, 'svg');
    svg.setAttribute('class', 'cal-trail');
    svg.setAttribute('aria-hidden', 'true');
    svg.append(d.line);
    d.trail = svg;
    document.body.append(...[d.was, svg, d.preview, d.ghost].filter(Boolean));
    document.body.style.userSelect = 'none';
}

/** Пересчёт цели по последней позиции указателя: сдвиг по минутам (dy), день — колонка под указателем. */
function update(d) {
    const { host, last } = d;
    const geo = host.geo();
    if (!geo) return;
    const hit = host.dayAt(last.clientX, last.clientY);
    d.zone = hit?.area ? 'grid' : host.poolAt(last.clientX, last.clientY) ? 'pool' : 'none';
    poolZoneClasses(d);
    if (d.zone !== 'grid') {   // цель бывает только над колонкой дня
        d.target = null;
        d.marks?.remove();
        d.marks = null;
        d.line.style.display = 'none';
        showZoneGhost(d);
        edge(d);
        edgeHours(d);
        return;
    }
    const today = new Date();
    today.setHours(0, 0, 0, 0);
    const dayMs = hit.dayMs ?? d.lastDay ?? (d.origin ?? today.getTime());
    d.lastDay = dayMs;
    // Абсолютное время от начала оси часов в колонке или строке (окно часов могло уехать)
    const wall = new Date(host.pxToTime(dayMs, host.axisPx(hit.area, last)));
    const base = new Date(dayMs);
    base.setHours(wall.getHours(), wall.getMinutes(), 0, 0);
    const dense = geo.ppm * 10 < 4;   // 10-минутные отметки сливаются — только часы и слоты
    const target = snapTarget(base.getTime(), { slots: host.freeSlots(d.post.channel, dayMs), pxPerMinute: geo.ppm, dayStartMs: dayMs, roundTo: dense ? 10 : 1, tenMin: !dense });
    if (target.kind !== 'round' && target.kind !== d.lastKind) navigator.vibrate?.(8);   // щелчок на прилипании
    d.lastKind = target.kind;
    d.target = target;
    d.line.style.display = '';
    showMarks(d, dayMs, hit, target, dense);
    showGhost(d, target, dense);
    showTrail(d, dayMs, target);
    edge(d);
    edgeHours(d);
}

/** Колонка «Предложка» во время переноса — зона возврата; под указателем — подсветка. */
function poolZoneClasses(d) {
    const col = document.getElementById('cal-pool-col');
    if (!col) return;
    col.classList.toggle('is-drop-zone', true);
    col.classList.toggle('is-drop-hot', d.zone === 'pool');
}

/** Подсказка у курсора, когда цели нет: над Предложкой — вернуть или снять с расписания, мимо сетки — пост останется на месте. */
function showZoneGhost(d) {
    const { zone, kind, post, copy } = d;
    const stay = d.origin == null ? '' : `Отпустите на сетке — или пост останется на ${d.host.hhmm(d.origin)}`;
    let text;
    if (zone === 'pool') {
        if (kind === 'pool') text = '↩ Отпустите — вернуть в Предложку';
        else if (copy || !['queued', 'failed', 'telegram'].includes(post.status)) text = '↩ Отпустите — перенос отменён';
        else text = post.status === 'telegram' ? '↩ Снять с расписания (уберём из „Отложенных“ канала)' : '↩ Снять с расписания';
    } else {
        text = kind === 'pool' ? 'Отпустите на сетке — или пост вернётся в Предложку' : stay;
    }
    d.ghost.classList.toggle('is-bad', false);
    d.ghost.replaceChildren(el('b', { className: 'cal-ghost-shift', textContent: text }));
    d.ghost.style.transform = `translate(${d.last.clientX + 16}px, ${d.last.clientY + 16}px)`;
    d.preview.style.transform = `translate(${d.last.clientX - d.grab.dx}px, ${d.last.clientY - d.grab.dy}px) rotate(-1.5deg)`;
}

/** Отметки времени в колонке под указателем: точки каждые 10 минут и крупные каждый час, слоты — отдельно. */
function showMarks(d, dayMs, hit, target, dense) {
    d.marks?.remove();
    d.marks = null;
    if (!hit?.area) return;
    const { host } = d;
    const geo = host.geo();
    const marks = el('div', { className: 'cal-marks', 'aria-hidden': 'true' });
    for (let m = geo.sh * 60; m <= geo.eh * 60; m += 10) {
        const hour = m % 60 === 0;
        if (dense && !hour) continue;
        const at = dayMs + m * MIN;
        const dot = el('span', { className: `cal-mark${hour ? ' is-hour' : ''}${at === target.at ? ` is-on is-${target.kind}` : ''}` });
        host.placeAt(dot, dayMs, at);
        marks.append(dot);
    }
    for (const at of host.freeSlots(d.post.channel, dayMs)) {
        const dot = el('span', { className: `cal-mark is-slot${at === target.at ? ' is-on is-slot' : ''}` });
        host.placeAt(dot, dayMs, at);
        marks.append(dot);
    }
    hit.area.append(marks);
    d.marks = marks;
}

/** Плашка у указателя: сдвиг крупно, новое время, пометки (копия, слот, нельзя раньше минуты). */
function showGhost(d, target, dense) {
    const past = target.at < Date.now() + MIN;
    const notes = [d.copy ? '⧉ копия' : '', dense ? 'приблизьте, чтобы ставить с точностью до 10 минут' : '', past ? 'раньше минуты от «сейчас» нельзя' : ''].filter(Boolean);
    d.ghost.classList.toggle('is-bad', past);
    d.ghost.replaceChildren(
        el('b', { className: 'cal-ghost-shift', textContent: d.origin == null ? d.host.hhmm(target.at) : shiftLabel(d.origin, target.at) }),
        el('span', { className: 'cal-ghost-when', textContent: `${d.host.whenLabel(target.at)} · прилипло ${STICK_WORD[target.kind] ?? 'к минутам'}` }),
        ...notes.map(text => el('span', { className: 'cal-ghost-note', textContent: text })));
    d.ghost.style.transform = `translate(${d.last.clientX + 16}px, ${d.last.clientY + 16}px)`;
    d.preview.style.transform = `translate(${d.last.clientX - d.grab.dx}px, ${d.last.clientY - d.grab.dy}px) rotate(-1.5deg)`;
}

/** Линия-траектория: от исходной карточки до отметки нового времени. */
function showTrail(d, dayMs, target) {
    if (d.kind === 'pool') { d.line.style.display = 'none'; return; }   // из Предложки траектории нет: исходного времени не было
    const from = d.card.getBoundingClientRect();
    const to = d.host.pointOf(dayMs, target.at);
    d.line.style.display = to ? '' : 'none';
    if (!to) return;
    d.line.setAttribute('x1', from.left + from.width / 2);
    d.line.setAttribute('y1', from.top + from.height / 2);
    d.line.setAttribute('x2', to.x);
    d.line.setAttribute('y2', to.y);
}

/** У левого или правого края календаря вид сдвигается на день раз в 600 мс. */
function edge(d) {
    const rect = d.host.bounds();
    const { clientX: x, clientY: y } = d.last;
    // Только полоса внутри сетки: над Предложкой слева и панелью справа (они вне сетки) вид не листается
    const dir = d.host.edgeDirs(x, y, rect, EDGE_PX).day;
    if (dir === d.edgeDir) return;
    d.edgeDir = dir;
    clearInterval(d.edgeTimer);
    if (!dir) return;
    d.edgeTimer = setInterval(() => {
        d.host.shiftDays(dir);
        d.host.flush();
        update(d);
    }, EDGE_STEP_MS);
}

/** У верхнего или нижнего края календаря окно часов сдвигается на полчаса раз в 600 мс (перенос к 22:00, когда 22:00 за окном). */
function edgeHours(d) {
    const rect = d.host.bounds();
    const { clientX: x, clientY: y } = d.last;
    const dir = d.host.edgeDirs(x, y, rect, EDGE_PX).hour;
    if (dir === d.vEdgeDir) return;
    d.vEdgeDir = dir;
    clearInterval(d.vEdgeTimer);
    if (!dir) return;
    d.host.freezeHours();
    d.vEdgeTimer = setInterval(() => {
        d.host.shiftHours(dir * 0.5);
        d.host.flush();
        update(d);
    }, EDGE_STEP_MS);
}

function cleanup() {
    const d = drag;
    drag = null;
    if (!d) return;
    clearTimeout(d.holdTimer);
    clearInterval(d.edgeTimer);
    clearInterval(d.vEdgeTimer);
    if (d.vEdgeDir) d.host.thawHours();
    document.removeEventListener('pointermove', onMove);
    document.removeEventListener('pointerup', onUp);
    document.removeEventListener('pointercancel', cancelDrag);
    document.removeEventListener('keydown', onKey);
    document.removeEventListener('keyup', onKey);
    d.ghost?.remove();
    d.preview?.remove();
    d.was?.remove();
    d.trail?.remove();
    d.marks?.remove();
    d.card.classList.remove('is-dragging');
    document.getElementById('cal-pool-col')?.classList.remove('is-drop-zone', 'is-drop-hot');
    document.body.style.userSelect = '';
    if (d.began) d.host.setDragging(false);
}

/** Отмена или возврат: копия карточки летит на место исходной (180 мс), потом сброс, подсветка и действие. */
async function finishBack(d, after) {
    document.removeEventListener('pointermove', onMove);   // после отпускания указатель больше не двигает перенос
    const card = d.card.isConnected ? d.card : null;
    const rect = card?.getBoundingClientRect();
    const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
    if (d.preview && rect && !reduced) {
        d.preview.style.transition = 'transform 180ms ease, opacity 180ms ease';
        d.preview.style.transform = `translate(${rect.left}px, ${rect.top}px)`;
        d.preview.style.opacity = '0.4';
        await new Promise(resolve => setTimeout(resolve, 180));
    }
    cleanup();
    if (card) {
        card.classList.add('is-returned');
        setTimeout(() => card.classList.remove('is-returned'), 600);
    }
    after?.();
}

/** Фон календаря: сдвиг по дням мышью и пальцем, щипок двумя пальцами (Pointer Events). Карточки — своя логика. */
export function bindGestures(surface, host) {
    const pointers = new Map();
    let pan = null;      // {x0, y0, startMs, startShift, moved}
    let pinch = null;    // {dist}
    surface.addEventListener('pointerdown', event => {
        if (event.button !== 0 || !host.view()) return;
        if (event.pointerType !== 'touch' && event.target.closest('.cal-card, .cal-free, .cal-cascade-count, button')) return;
        pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
        host.stopAnim();
        if (pointers.size === 1) pan = { x0: event.clientX, y0: event.clientY, startMs: host.view().startMs, startShift: host.hourShift(), moved: false };
        if (pointers.size === 2) {
            if (pan?.moved) { host.thawHours(); document.body.classList.remove('is-panning'); }
            pinch = { dist: spread(pointers) };
            pan = null;
        }
    });
    document.addEventListener('pointermove', event => {
        if (!pointers.has(event.pointerId)) return;
        pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
        if (host.isDragging()) return;
        if (pinch && pointers.size === 2) {
            const [a, b] = [...pointers.values()];
            const dist = spread(pointers);
            host.zoomAt((a.x + b.x) / 2, (a.y + b.y) / 2, pinch.dist / dist, { centre: true });
            pinch.dist = dist;
        } else if (pan) {
            const dx = event.clientX - pan.x0;
            const dy = event.clientY - pan.y0;
            if (!pan.moved && Math.hypot(dx, dy) > PAN_START_PX) {
                pan.moved = true;
                host.freezeHours();   // часы не дышат весь жест: двигаются дни и часы сразу
                host.markMoved();
                document.body.classList.add('is-panning');
            }
            if (pan.moved) {   // без выбора оси: каждый кадр — от точки старта жеста; host сам решает, какая ось — дни
                const axes = host.orient(dx, dy);
                host.panTo(pan.startMs, panAxis(axes.day, AXIS_DEADZONE_PX));
                host.panHours(pan.startShift, panAxis(axes.hour, AXIS_DEADZONE_PX));
            }
        }
    });
    const release = event => {
        pointers.delete(event.pointerId);
        if (pointers.size < 2) pinch = null;
        if (!pointers.size) {
            if (pan?.moved) {
                document.body.classList.remove('is-panning');
                if (event.type === 'pointercancel') host.thawHours();
                else host.endPan(event.clientX - pan.x0);   // отпускание: день прилипает к ближайшей полуночи
            }
            pan = null;
        }
    };
    document.addEventListener('pointerup', release);
    document.addEventListener('pointercancel', release);
}

function spread(pointers) {
    const [a, b] = [...pointers.values()];
    return Math.max(1, Math.hypot(a.x - b.x, a.y - b.y));
}
