// calendar-model.js — чистая модель календаря: недели, сетка месяца, повестка дня, свободные слоты, сводка.
// Без DOM: её проверяет tools/test_calendar.mjs. Время — миллисекунды; посты приходят с unix-секундами `at`.

export const HOUR = 3600000;
export const DAY = 86400000;
const SLOT_TOLERANCE = 60000;                       // слот занят, если пост той же канала в пределах минуты
const OPEN_STATUSES = ['queued', 'sending', 'telegram'];

/** Смещение пояса в миллисекундах (Intl; одна поправка — для перехода на летнее время). */
function zoneOffset(ms, timeZone) {
    const parts = Object.fromEntries(new Intl.DateTimeFormat('en-US', { timeZone, hourCycle: 'h23', year: 'numeric', month: 'numeric', day: 'numeric', hour: 'numeric', minute: 'numeric' })
        .formatToParts(ms).filter(p => p.type !== 'literal').map(p => [p.type, Number(p.value)]));
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
    const parts = Object.fromEntries(new Intl.DateTimeFormat('en-US', { timeZone, year: 'numeric', month: 'numeric', day: 'numeric' })
        .formatToParts(date).map(p => [p.type, Number(p.value)]));
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

/** Свободные будущие слоты канала в [fromMs, toMs): без занятых постами того же канала (±1 мин). */
export function freeSlots(channel, posts, fromMs, toMs, nowMs) {
    const taken = posts.filter(p => p.channel === channel.id).map(p => p.at * 1000);
    return slotTimes(channel.schedule, fromMs, toMs - 1)
        .filter(at => at > nowMs && !taken.some(t => Math.abs(t - at) <= SLOT_TOLERANCE))
        .map(at => ({ kind: 'slot', at, channel }));
}

/** Строки дня по времени: посты включённых каналов и свободные слоты тех же каналов. */
export function dayAgenda(posts, channels, dayStartMs, nowMs) {
    const dayEnd = addDays(dayStartMs, 1);
    const ids = new Set(channels.map(c => c.id));
    const rows = posts
        .filter(p => ids.has(p.channel) && p.at * 1000 >= dayStartMs && p.at * 1000 < dayEnd)
        .map(post => ({ kind: 'post', at: post.at * 1000, post }));
    for (const channel of channels) rows.push(...freeSlots(channel, posts, dayStartMs, dayEnd, nowMs));
    return rows.sort((a, b) => a.at - b.at);
}

/** Сводка: сколько постов в очереди (queued, sending, telegram) и свободных слотов на неделе — по включённым каналам. */
export function summary(posts, channels, weekStartMs, nowMs) {
    const ids = new Set(channels.map(c => c.id));
    const queued = posts.filter(p => ids.has(p.channel) && OPEN_STATUSES.includes(p.status)).length;
    const weekEnd = addDays(weekStartMs, 7);
    const freeThisWeek = channels.reduce((sum, channel) => sum + freeSlots(channel, posts, weekStartMs, weekEnd, nowMs).length, 0);
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

/**
 * Карточки одного дня: позиция по времени, колонки внутри пересекающихся групп, стопка вместо узких колонок.
 * rows — объекты с at (мс); возвращает {type: 'card', item, left, width, top}, {type: 'stack', items, from, to, left, width, top}
 * и {type: 'cover', items, more, left, width, top} (первый пост целиком, остальные — за бейджем «+N»).
 */
export function layoutDay(rows, { originMs, pxPerMinute, colWidth, cardHeight, minWidth = 60, colHeight }) {
    const items = rows.map(item => { const top = (item.at - originMs) / MIN * pxPerMinute; return { item, at: item.at, top, timeTop: top }; });   // timeTop — где карточка по настоящему времени
    if (colHeight !== undefined) raiseAtBottom(items, cardHeight, colHeight);   // карточка у низа колонки поднимается, время — на риске
    items.sort((a, b) => a.top - b.top);
    const out = [];
    let group = [];
    let bottom = -Infinity;
    const flush = () => {
        if (!group.length) return;
        const laneEnds = [];
        for (const item of group) {
            let lane = laneEnds.findIndex(end => end <= item.top);
            if (lane < 0) lane = laneEnds.push(0) - 1;
            laneEnds[lane] = item.top + cardHeight;
            item.lane = lane;
        }
        const lanes = laneEnds.length;
        const width = colWidth / lanes;
        if (lanes > 1 && ((lanes >= 3 && group.length >= 4) || colWidth < minWidth)) {   // стопка — от четырёх постов в трёх и более дорожках (цепочка в две дорожки остаётся карточками) или когда вся колонка уже minWidth (месяц)
            const ats = group.map(i => i.at);
            out.push({ type: 'stack', items: group.map(i => i.item), from: Math.min(...ats), to: Math.max(...ats), left: 0, width: colWidth, top: group[0].top, timeTop: Math.min(...group.map(i => i.timeTop)) });
        } else if (lanes > 1 && width < minWidth && group.every(i => i.item.kind !== 'slot')) {   // два-три поста в одно время в узкой колонке: первая карточка целиком и бейдж «+N»; слоты не прячем
            out.push({ type: 'cover', items: group.map(i => i.item), more: group.length - 1, left: 0, width: colWidth, top: group[0].top, timeTop: Math.min(...group.map(i => i.timeTop)) });
        } else {
            for (const item of group) out.push({ type: 'card', item: item.item, left: item.lane * width, width, top: item.top, timeTop: item.timeTop });
        }
        group = [];
    };
    for (const item of items) {
        if (item.top >= bottom) flush();
        group.push(item);
        bottom = Math.max(bottom, item.top + cardHeight);
    }
    flush();
    return out;
}

/** Карточки, которые не помещаются до низа колонки, поднимаются: снизу вверх, одна над другой. Их настоящее время — в raisedFrom.
 *  Карточка ниже видимого окна (top >= colHeight) не трогается; время внутри окна не уезжает выше 0. */
export function raiseAtBottom(items, cardHeight, colHeight) {
    let ceil = colHeight;   // верхний край уже поднятой карточки: следующая (более ранняя) должна уместиться над ней
    for (const it of [...items].sort((a, b) => b.top - a.top)) {
        if (it.top < 0 || it.top >= colHeight || it.top + cardHeight <= colHeight && it.top + cardHeight <= ceil) continue;   // вне окна часов не трогаем
        const top = Math.max(0, Math.min(it.top, ceil - cardHeight));
        if (top !== it.top) it.raisedFrom = it.top;
        it.top = top;
        ceil = top;
    }
}

/** Карточки у правого края строки поднимаются влево, одна за другой (как raiseAtBottom для колонок). Настоящее время — в raisedFrom. */
export function raiseAtEnd(items, cardWidth, rowWidth) {
    let ceil = rowWidth;
    for (const it of [...items].sort((a, b) => b.left - a.left)) {
        if (it.left < 0 || it.left >= rowWidth || it.left + cardWidth <= rowWidth && it.left + cardWidth <= ceil) continue;
        const left = Math.max(0, Math.min(it.left, ceil - cardWidth));
        if (left !== it.left) it.raisedFrom = it.left;
        it.left = left;
        ceil = left;
    }
}

/** Раскладка строки дня (вид «дни строками»): время — по горизонтали, пересекающиеся посты складываются вертикально.
 *  rows — объекты с at (мс). Карточка: {type:'card', item, left, top, width, height, timeLeft}; стопка: {type:'stack', items, from, to, left, top, width, height, timeLeft}.
 *  Ряды делят высоту строки; ниже minLaneH ряд не делаем — стопка. */
export function layoutRow(rows, { originMs, pxPerMinute, rowHeight, rowWidth, cardWidth, cardHeight, minLaneH = 22 }) {
    const items = rows.map(item => { const left = (item.at - originMs) / MIN * pxPerMinute; return { item, at: item.at, left, timeLeft: left }; });
    raiseAtEnd(items, cardWidth, rowWidth);
    items.sort((a, b) => a.left - b.left);
    const out = [];
    let group = [];
    let right = -Infinity;
    const flush = () => {
        if (!group.length) return;
        const laneEnds = [];
        for (const item of group) {
            let lane = laneEnds.findIndex(end => end <= item.left);
            if (lane < 0) lane = laneEnds.push(0) - 1;
            laneEnds[lane] = item.left + cardWidth;
            item.lane = lane;
        }
        const lanes = laneEnds.length;
        const laneH = Math.min(cardHeight, Math.floor(rowHeight / lanes));   // рядов несколько — каждый ниже, но не ниже minLaneH
        const timeLeft = Math.min(...group.map(i => i.timeLeft));
        if (lanes > 1 && (group.length >= 4 || laneH < minLaneH)) {
            const ats = group.map(i => i.at);
            out.push({ type: 'stack', items: group.map(i => i.item), from: Math.min(...ats), to: Math.max(...ats), left: group[0].left, top: 0, width: cardWidth, height: rowHeight, timeLeft });
        } else {
            for (const item of group) out.push({ type: 'card', item: item.item, left: item.left, top: item.lane * laneH, width: cardWidth, height: laneH, timeLeft: item.timeLeft });
        }
        group = [];
    };
    for (const item of items) {
        if (item.left >= right) flush();
        group.push(item);
        right = Math.max(right, item.left + cardWidth);
    }
    flush();
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
