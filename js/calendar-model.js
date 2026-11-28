// calendar-model.js — чистая модель календаря: недели, сетка месяца, повестка дня, свободные слоты, сводка.
// Без DOM: её проверяет tools/test_calendar.mjs. Время — миллисекунды; посты приходят с unix-секундами `at`.

export const HOUR = 3600000;
export const DAY = 86400000;
const SLOT_TOLERANCE = 60000;                       // слот занят, если пост той же канала в пределах минуты
const OPEN_STATUSES = ['queued', 'sending', 'telegram'];

// Intl formatters are expensive to construct. Keep a small, bounded cache;
// offsets themselves must still be calculated for each instant (DST).
const zoneFormatters = new Map();
function zoneFormatter(timeZone) {
    if (!zoneFormatters.has(timeZone)) {
        const formatter = new Intl.DateTimeFormat('en-US', { timeZone, hourCycle: 'h23', year: 'numeric', month: 'numeric', day: 'numeric', hour: 'numeric', minute: 'numeric' });
        if (zoneFormatters.size >= 32) zoneFormatters.delete(zoneFormatters.keys().next().value);
        zoneFormatters.set(timeZone, formatter);
    }
    return zoneFormatters.get(timeZone);
}

function zonedParts(ms, timeZone) {
    const parts = {};
    for (const { type, value } of zoneFormatter(timeZone).formatToParts(ms)) {
        if (type !== 'literal') parts[type] = Number(value);
    }
    return parts;
}

/** Смещение пояса в миллисекундах (Intl; одна поправка — для перехода на летнее время). */
function zoneOffset(ms, timeZone) {
    const parts = zonedParts(ms, timeZone);
    return Date.UTC(parts.year, parts.month - 1, parts.day, parts.hour, parts.minute) - Math.floor(ms / 60000) * 60000;
}

/** Настенное время в поясе → момент (мс). */
function wallToInstant(year, month, day, hour, minute, timeZone) {
    const guess = Date.UTC(year, month, day, hour, minute);
    const first = zoneOffset(guess, timeZone);
    const instant = guess - first;
    const second = zoneOffset(instant, timeZone);
    return second === first ? instant : guess - second;
}

/** Календарная дата момента в поясе: [год, месяц0, день]. */
function dateParts(date, timeZone) {
    const parts = zonedParts(date, timeZone);
    return [parts.year, parts.month - 1, parts.day];
}

/** Все слоты расписания канала между fromMs и toMs (мс, по возрастанию). Расписание: {tz, slots: {"1": ["10:00"], …"7"}}. */
export function slotTimes(schedule, fromMs, toMs) {
    if (!schedule?.slots) return [];
    const timeZone = schedule.tz || Intl.DateTimeFormat().resolvedOptions().timeZone;
    const out = [];
    for (let day = Date.UTC(...dateParts(new Date(fromMs - DAY), timeZone)); day <= toMs + DAY; day += DAY) {
        const date = new Date(day);
        const weekday = date.getUTCDay() || 7;
        for (const hhmmText of schedule.slots[weekday] || []) {
            const [hour, minute] = hhmmText.split(':').map(Number);
            const instant = wallToInstant(date.getUTCFullYear(), date.getUTCMonth(), date.getUTCDate(), hour, minute, timeZone);
            if (instant >= fromMs && instant <= toMs) out.push(instant);
        }
    }
    return out.sort((a, b) => a - b);
}

/** Местная полночь через n календарных дней (переход на летнее время не сдвигает дату). */
export function addDays(ms, n) {
    const d = new Date(ms);
    return new Date(d.getFullYear(), d.getMonth(), d.getDate() + n).getTime();
}

/** Понедельник 00:00 местного времени недели, в которую попадает момент. */
export function weekStart(ms) {
    const d = new Date(ms);
    return new Date(d.getFullYear(), d.getMonth(), d.getDate() - ((d.getDay() + 6) % 7)).getTime();
}

// Occupancy includes every post, even outside the requested day: a post
// one minute before midnight can occupy the following day's first slot.
function occupancy(posts) {
    const byChannel = new Map();
    for (const post of posts) {
        const at = post.at * 1000;
        if (!Number.isFinite(at)) continue;
        if (!byChannel.has(post.channel)) byChannel.set(post.channel, []);
        byChannel.get(post.channel).push(at);
    }
    for (const times of byChannel.values()) times.sort((a, b) => a - b);
    return byChannel;
}

function isOccupied(times, at) {
    let lo = 0, hi = times.length;
    const earliest = at - SLOT_TOLERANCE;
    while (lo < hi) {
        const mid = Math.floor((lo + hi) / 2);
        if (times[mid] < earliest) lo = mid + 1;
        else hi = mid;
    }
    return lo < times.length && times[lo] <= at + SLOT_TOLERANCE;
}

function availableSlots(channel, taken, fromMs, toMs, nowMs) {
    return slotTimes(channel.schedule, fromMs, toMs - 1)
        .filter(at => at > nowMs && !isOccupied(taken, at))
        .map(at => ({ kind: 'slot', at, channel }));
}

/** Свободные будущие слоты канала в [fromMs, toMs): без занятых постами того же канала (±1 мин). */
export function freeSlots(channel, posts, fromMs, toMs, nowMs) {
    const taken = occupancy(posts.filter(p => p.channel === channel.id)).get(channel.id) || [];
    return availableSlots(channel, taken, fromMs, toMs, nowMs);
}

/** Snapshot for one render; reuse the indexes for every visible day. */
export function calendarAgenda(posts, channels, nowMs) {
    const taken = occupancy(posts);
    const ids = new Set(channels.map(c => c.id));
    const byDay = new Map();
    for (const post of posts) {
        const at = post.at * 1000;
        if (!ids.has(post.channel) || !Number.isFinite(at)) continue;
        const day = addDays(at, 0);
        if (!byDay.has(day)) byDay.set(day, []);
        byDay.get(day).push({ kind: 'post', at, post });
    }
    return dayStartMs => {
        const dayEnd = addDays(dayStartMs, 1);
        // Keep the original API's interval semantics for a non-midnight start.
        const rows = dayStartMs === addDays(dayStartMs, 0)
            ? [...(byDay.get(dayStartMs) || [])]
            : posts.filter(p => ids.has(p.channel) && p.at * 1000 >= dayStartMs && p.at * 1000 < dayEnd)
                .map(post => ({ kind: 'post', at: post.at * 1000, post }));
        for (const channel of channels) rows.push(...availableSlots(channel, taken.get(channel.id) || [], dayStartMs, dayEnd, nowMs));
        return rows.sort((a, b) => a.at - b.at);
    };
}

export function dayAgenda(posts, channels, dayStartMs, nowMs) {
    return calendarAgenda(posts, channels, nowMs)(dayStartMs);
}

/** Сводка: очередь и свободные слоты недели по включённым каналам. */
export function summary(posts, channels, weekStartMs, nowMs) {
    const ids = new Set(channels.map(c => c.id));
    const queued = posts.filter(p => ids.has(p.channel) && OPEN_STATUSES.includes(p.status)).length;
    const taken = occupancy(posts);
    const weekEnd = addDays(weekStartMs, 7);
    const freeThisWeek = channels.reduce((sum, channel) => sum + availableSlots(channel, taken.get(channel.id) || [], weekStartMs, weekEnd, nowMs).length, 0);
    return { queued, freeThisWeek };
}

// ---- Календарь v3: масштаб, прилипание при перетаскивании, раскладка карточек ----

export const MIN = 60000;
const SNAP_SLOT_PX = 16;     // ближе этого к свободному слоту — прилипает к слоту
const SNAP_HOUR_PX = 10;     // ближе этого к часу — прилипает к часу
const SNAP_TEN_PX = 4;       // ближе этого к 10-минутной отметке — прилипает к ней

/** Прилипание точки перетаскивания: слот > час > 10 минут; между ними — минута (roundTo: 10 — когда 10-минутные отметки слиты, tenMin: false). */
export function snapTarget(rawMs, { slots = [], pxPerMinute, dayStartMs, roundTo = 1, tenMin = true }) {
    const px = ms => Math.abs(ms - rawMs) / MIN * pxPerMinute;
    let slot = null;
    for (const at of slots) if (px(at) <= SNAP_SLOT_PX && (slot === null || px(at) < px(slot))) slot = at;
    if (slot !== null) return { at: slot, kind: 'slot' };
    const d = new Date(rawMs);
    const hour = new Date(d.getFullYear(), d.getMonth(), d.getDate(), d.getHours() + (d.getMinutes() >= 30 ? 1 : 0)).getTime();
    if (px(hour) <= SNAP_HOUR_PX) return { at: hour, kind: 'hour' };
    const ten = dayStartMs + Math.round((rawMs - dayStartMs) / (10 * MIN)) * 10 * MIN;
    if (tenMin && px(ten) <= SNAP_TEN_PX) return { at: ten, kind: '10min' };
    const step = roundTo * MIN;
    return { at: dayStartMs + Math.round((rawMs - dayStartMs) / step) * step, kind: 'round' };
}

/** «+2 ч 10 мин», «−1 день, +30 мин», «без изменений»: сдвиг по календарным дням и по времени суток. */
export function shiftLabel(fromMs, toMs) {
    const diff = Math.round((toMs - fromMs) / MIN);
    if (diff === 0) return 'без изменений';
    const f = new Date(fromMs);
    const t = new Date(toMs);
    let days = Math.round((Date.UTC(t.getFullYear(), t.getMonth(), t.getDate()) - Date.UTC(f.getFullYear(), f.getMonth(), f.getDate())) / DAY);
    let rest = diff - days * 1440;
    while (rest > 720) { days++; rest -= 1440; }
    while (rest < -720) { days--; rest += 1440; }
    const parts = [];
    if (days) parts.push(`${days < 0 ? '−' : '+'}${Math.abs(days)} ${dayWord(Math.abs(days))}`);
    if (rest) {
        const sign = rest < 0 ? '−' : '+';
        const h = Math.floor(Math.abs(rest) / 60);
        const m = Math.abs(rest) % 60;
        parts.push(`${sign}${h ? `${h} ч${m ? ` ${m} мин` : ''}` : `${m} мин`}`);
    }
    return parts.join(', ');
}

function dayWord(n) {
    if (n % 10 === 1 && n % 100 !== 11) return 'день';
    if (n % 10 >= 2 && n % 10 <= 4 && (n % 100 < 12 || n % 100 > 14)) return 'дня';
    return 'дней';
}

// Move overflowing cards toward the start of the visible time axis, without
// touching cards outside the viewport. The original position remains available
// for the time marker.
function raiseAtEdge(items, axis, size, extent) {
    let edge = extent;
    for (const item of [...items].sort((a, b) => b[axis] - a[axis])) {
        const pos = item[axis];
        if (pos < 0 || pos >= extent || pos + size <= edge) continue;
        const adjusted = Math.max(0, Math.min(pos, edge - size));
        if (adjusted !== pos) item.raisedFrom = pos;
        item[axis] = adjusted;
        edge = adjusted;
    }
}

export function raiseAtBottom(items, cardHeight, colHeight) {
    raiseAtEdge(items, 'top', cardHeight, colHeight);
}

export function raiseAtEnd(items, cardWidth, rowWidth) {
    raiseAtEdge(items, 'left', cardWidth, rowWidth);
}

// The two calendar orientations use the same interval packing. The first
// available lane wins, and equal positions retain their original order.
function* collisionGroups(items, axis, size) {
    items.sort((a, b) => a[axis] - b[axis]);
    let group = [], laneEnds = [], edge = -Infinity;
    for (const item of items) {
        const pos = item[axis];
        if (group.length && pos >= edge) {
            yield { group, lanes: laneEnds.length };
            group = [];
            laneEnds = [];
        }
        let lane = laneEnds.findIndex(end => end <= pos);
        if (lane < 0) lane = laneEnds.length;
        laneEnds[lane] = pos + size;
        item.lane = lane;
        group.push(item);
        edge = Math.max(edge, pos + size);
    }
    if (group.length) yield { group, lanes: laneEnds.length };
}

function groupRange(group) {
    const times = group.map(item => item.at);
    return { from: Math.min(...times), to: Math.max(...times) };
}

/** Вертикальная раскладка дня: карточки, стопка или первая карточка с бейджем «+N». */
export function layoutDay(rows, { originMs, pxPerMinute, colWidth, cardHeight, minWidth = 60, colHeight }) {
    const items = rows.map(item => {
        const top = (item.at - originMs) / MIN * pxPerMinute;
        return { item, at: item.at, top, timeTop: top };
    });
    if (colHeight !== undefined) raiseAtBottom(items, cardHeight, colHeight);
    const out = [];
    for (const { group, lanes } of collisionGroups(items, 'top', cardHeight)) {
        const width = colWidth / lanes;
        const stack = lanes > 1 && ((lanes >= 3 && group.length >= 4) || colWidth < minWidth);
        // Keep free slots visible; only a group of posts can become a cover.
        const cover = lanes > 1 && width < minWidth && group.every(i => i.item.kind !== 'slot');
        if (stack || cover) {
            out.push({
                type: stack ? 'stack' : 'cover',
                items: group.map(i => i.item),
                ...(stack ? groupRange(group) : { more: group.length - 1 }),
                left: 0, width: colWidth, top: group[0].top, timeTop: Math.min(...group.map(i => i.timeTop))
            });
        } else {
            for (const i of group) out.push({ type: 'card', item: i.item, left: i.lane * width, width, top: i.top, timeTop: i.timeTop });
        }
    }
    return out;
}

/** Горизонтальная раскладка дня: время слева направо, дорожки делят высоту строки. */
export function layoutRow(rows, { originMs, pxPerMinute, rowHeight, rowWidth, cardWidth, cardHeight, minLaneH = 22 }) {
    const items = rows.map(item => {
        const left = (item.at - originMs) / MIN * pxPerMinute;
        return { item, at: item.at, left, timeLeft: left };
    });
    raiseAtEnd(items, cardWidth, rowWidth);
    const out = [];
    for (const { group, lanes } of collisionGroups(items, 'left', cardWidth)) {
        const laneH = Math.min(cardHeight, Math.floor(rowHeight / lanes));
        if (lanes > 1 && (group.length >= 4 || laneH < minLaneH)) {
            out.push({
                type: 'stack', items: group.map(i => i.item), ...groupRange(group),
                left: group[0].left, top: 0, width: cardWidth, height: rowHeight, timeLeft: Math.min(...group.map(i => i.timeLeft))
            });
        } else {
            for (const i of group) out.push({ type: 'card', item: i.item, left: i.left, top: i.lane * laneH, width: cardWidth, height: laneH, timeLeft: i.timeLeft });
        }
    }
    return out;
}

/** Окно часов по вертикали: [начало, конец) в целых часах. Весь день — [0, 24]; иначе от самого раннего до самого позднего часа ±1, но не меньше 8 часов. */
export function hourWindow(postTimesMs, slotTimesMs, fromMs, toMs, fullDay) {
    if (fullDay) return [0, 24];
    const hours = [...postTimesMs, ...slotTimesMs].filter(t => t >= fromMs && t < toMs).map(t => { const d = new Date(t); return d.getHours() + d.getMinutes() / 60; });
    if (!hours.length) return [8, 20];
    let start = Math.max(0, Math.floor(Math.min(...hours)) - 1);
    let end = Math.min(24, Math.ceil(Math.max(...hours)) + 1);
    if (end - start < 8) {
        end = Math.min(24, start + 8);
        start = Math.max(0, end - 8);
    }
    return [start, end];
}

/** Масштаб у курсора: день под курсором остаётся под курсором. view = {startMs, days, width}; cursorX — в пикселях от левого края. */
export function zoomAround(view, cursorX, factor, { min = 1, max = 31 } = {}) {
    const days = Math.min(max, Math.max(min, view.days * factor));
    const frac = view.width ? cursorX / view.width : 0;
    const cursorMs = view.startMs + frac * view.days * DAY;
    return { ...view, days, startMs: cursorMs - frac * days * DAY };
}

// ---- Колесо, щипок и доводка: чистые функции (проверяются в tools/test_calendar.mjs) ----

export const WHEEL_STEP_MAX = 120;    // одна порция колеса, px: рывок не перепрыгивает с недели на месяц
export const WHEEL_ZOOM_K = 0.00125;  // 100 px (щелчок колеса в Chrome) ≈ +13 % масштаба
export const ZOOM_TAU_MS = 70;        // постоянная времени доводки вида к цели

/** Порция колеса в пикселях: строки (deltaMode 1) ×16, страницы (2) — высота сетки; не больше WHEEL_STEP_MAX по модулю. */
export function wheelPixels(delta, mode, pageHeight = 0) {
    const px = mode === 1 ? delta * 16 : mode === 2 ? delta * pageHeight : delta;
    return Math.max(-WHEEL_STEP_MAX, Math.min(WHEEL_STEP_MAX, px));
}

/** Множитель масштаба за порцию: положительная px — вниз, то есть «наружу» (дней больше). */
export const wheelFactor = px => Math.exp(px * WHEEL_ZOOM_K);

/** Предел масштаба в эту сторону: тогда колесо не перехватываем, страница прокручивается. */
export function zoomLimitReached(days, px, { min = 1, max = 31 } = {}) {
    return px > 0 ? days >= max - 1e-6 : days <= min + 1e-6;
}

/** Целые дни: обычным округлением (6,6–7,4 → 7), месяц липкий: от 30,2 — 31. В пределах [min, max]. */
export function snapDays(days, { min = 1, max = 31 } = {}) {
    const rounded = days >= 30.2 ? 31 : Math.round(days);
    return Math.min(max, Math.max(min, rounded));
}

/** Прилипание цели после жеста: целые дни, начало — полночь, и день под курсором (cursorX) остаётся тем же.
 *  Полночь берём через addDays, а не «+24 ч»: день перехода на зимнее время — 25 часов. */
export function snapView(view, cursorX, width, limits, days = snapDays(view.days, limits)) {
    const frac = width ? cursorX / width : 0;
    const dayStart = addDays(view.startMs + frac * view.days * DAY, 0);   // полночь дня под курсором
    return { days, startMs: addDays(dayStart, -Math.floor(frac * days)) };   // курсор остаётся внутри того же дня
}

/** Расстояние между видами в пикселях (по большему из краёв), чтобы остановить доводку при разнице меньше 0,5 px. */
export function viewGapPx(a, b, width) {
    if (!width) return 0;
    const pxPerDay = width / b.days;
    return Math.max(Math.abs(a.startMs - b.startMs), Math.abs(a.startMs + a.days * DAY - (b.startMs + b.days * DAY))) / DAY * pxPerDay;
}

/** Один шаг доводки к цели за dtMs: экспонента по времени кадра, не зависит от частоты кадров. */
export function easeView(view, target, dtMs) {
    const k = 1 - Math.exp(-Math.max(0, dtMs) / ZOOM_TAU_MS);
    return { days: view.days + (target.days - view.days) * k, startMs: view.startMs + (target.startMs - view.startMs) * k };
}

/** Порция колеса поверх цели (не поверх текущего кадра): быстрые щелчки накапливаются. */
export function wheelZoom(target, cursorX, width, px, limits) {
    return zoomAround({ startMs: target.startMs, days: target.days, width }, cursorX, wheelFactor(px), limits);
}

/** Окно часов, сдвинутое на offsetH часов и зажатое в [0, 24] с сохранением длины. Дробные часы допустимы. */
export function shiftHourWindow([sh, eh], offsetH) {
    const len = eh - sh;
    const start = Math.min(Math.max(sh + offsetH, 0), 24 - len);
    return [start, start + len];
}

// ---- Вертикальный масштаб часов: одна шкала зума с горизонтальным (CALENDAR_ZOOM_V2_PROMPT.md) ----

export const VZOOM_MAX = 2.4;     // во сколько раз час выше на трёх днях, чем при «окно вписано в высоту» (месяц)
export const MIN_SPAN_H = 4;      // меньше этого часов по вертикали не показываем
export const ZOOM_DAYS_MIN = 3;   // на компьютере приближение до трёх дней
export const ZOOM_DAYS_MAX = 31;  // отдаление — до месяца

/** Положение на шкале зума: 0 — месяц, 1 — три дня. Логарифм дней: каждый щелчок колеса даёт одинаковый шаг. */
export function zoomT(days, { min = ZOOM_DAYS_MIN, max = ZOOM_DAYS_MAX } = {}) {
    return Math.min(1, Math.max(0, Math.log(max / days) / Math.log(max / min)));
}

/** Видимая длина окна часов по вертикали: при месяце — всё окно, при трёх днях — в VZOOM_MAX раз короче (не короче MIN_SPAN_H). */
export function hourSpan(baseLen, days, limits = {}) {
    const vz = VZOOM_MAX ** zoomT(days, limits);
    return Math.min(baseLen, Math.max(Math.min(MIN_SPAN_H, baseLen), baseLen / vz));
}

/** Окно часов после зума: час под курсором (cursorFrac — доля высоты сверху) остаётся на месте; окно зажато в [0, 24] и сохраняет длину. */
export function zoomHoursAround([sh, eh], cursorFrac, newSpan) {
    const hour = sh + cursorFrac * (eh - sh);
    const start = Math.min(Math.max(hour - cursorFrac * newSpan, 0), 24 - newSpan);
    return [start, start + newSpan];
}

/** Окно часов, центрированное на базовом: при пресете масштаба видна середина рабочего дня, а не начало окна. */
export function centeredHourShift(base, span) {
    return (base[1] - base[0] - span) / 2;
}

/** Целые дни после жеста: если жест сдвинул масштаб, а округление вернуло прежнее целое, шаг — на одно целое в сторону жеста.
 *  Без этого мелкие щелчки (4 дня → 3,5) возвращали бы масштаб на место, и приблизить дальше было бы нельзя. */
export function snapGestureDays(days, from, limits = {}) {
    const n = snapDays(days, limits);
    const s = Math.round(from);
    if (n !== s || Math.abs(days - from) < 0.05) return n;
    return Math.min(limits.max ?? ZOOM_DAYS_MAX, Math.max(limits.min ?? 1, days < from ? s - 1 : s + 1));
}

/** Сдвиг фона по оси с мёртвой зоной: до порога — 0, дальше — от порога без скачка (чтобы почти горизонтальный жест не дрожал часами). */
export function panAxis(delta, deadzone) {
    return Math.sign(delta) * Math.max(0, Math.abs(delta) - deadzone);
}
