// calendar.js — вкладка «Календарь»: сетка дней с масштабом (колесо — от трёх дней до месяца), карточки постов
// с содержимым по ширине колонки, свободные слоты, список, панель подробностей, подсказка «один раз».
// Перетаскивание и жесты фона — в calendar-drag.js, чистая модель — в calendar-model.js.
import { $, api, apiUrl, cleanCaption, el, isTabActive, onTabOpen, PHOTO_ICON, postTypeLabel, switchTab, VIDEO_ICON } from './core.js';
import { addDays, centeredHourShift, dayAgenda, easeView, freeSlots, hourSpan, hourWindow, layoutDay, layoutRow, shiftHourWindow, slotTimes, snapGestureDays, snapView, summary, viewGapPx, weekStart, wheelFactor, wheelPixels, zoomAround, zoomHoursAround, zoomLimitReached, ZOOM_DAYS_MAX, ZOOM_DAYS_MIN, DAY, HOUR, MIN } from './calendar-model.js';
import { postItemToTelegram } from './content.js';
import { isPhone, phoneView } from './calendar-phone.js';
import { draftChildren, flashPoolItem, holesChildren, planPanel, poolPanel, renderPoolColumn, UNDO_SECONDS, undoable } from './calendar-pool.js';
import { bindGestures, isDragging, startDrag, startPoolDrag } from './calendar-drag.js';
import { closePostView, openPostView } from './post-view.js';
import { fromQueuePost, fromRecord } from './post-adapters.js';
import { openAccountSection } from './account.js';

const PALETTE = ['#FFDA1A', '#E1001A', '#1546C8', '#0B7A3B', '#8A2BE2', '#FF7A00'];
const STATUS = {
    queued: 'В очереди: уйдёт в свой срок', sending: 'Отправляется…', failed: 'Не вышел', telegram: 'В «Отложенных» Telegram: уйдёт само', sent: 'Опубликован'
};
const STATUS_SHORT = { queued: 'в очереди', sending: 'отправляется', failed: 'не вышел', telegram: 'в «Отложенных» Telegram', sent: 'опубликован' };
const BADGE = { sent: ['вышел', 'ok'], failed: ['ошибка', 'danger'], telegram: ['отложен в TG', 'blue'], sending: ['отправляется', 'accent'] };
const MONTHS_SHORT = ['янв', 'фев', 'мар', 'апр', 'май', 'июн', 'июл', 'авг', 'сен', 'окт', 'ноя', 'дек'];
const WEEK_SHORT = ['пн', 'вт', 'ср', 'чт', 'пт', 'сб', 'вс'];   // неделя с понедельника
const MONTHS_GEN = ['января', 'февраля', 'марта', 'апреля', 'мая', 'июня', 'июля', 'августа', 'сентября', 'октября', 'ноября', 'декабря'];
const AXIS_W = 74;             // колонка охвата по часам слева (макет v4)
const HEAD_H = 56;             // шапка дня (совпадает с --cal-head в css)
const GUTTER = 4;              // отступ карточек от краёв колонки
const CARD_MAX_W = 320;        // карточка не шире этого, даже когда колонка широкая
const CARD_H = { full: 72, mid: 56, pill: 22 };   // CALENDAR_CARDS: полная 72, средняя 56, пилюля 22 (как свободный слот и стопка)
const CASCADE_MAX = 6;      // карточек в каскаде; остальные — за числом в кружке
const CASCADE_PEEK = { full: 30, mid: 32, pill: 14 };   // сколько торчит из-под карточки (px по вертикали): время, миниатюра и начало текста
const CASCADE_SHIFT = 4;    // сдвиг каждой следующей карточки вправо
const ROWS_HEAD_H = 30;                                  // вид «дни строками»: полоса часов сверху
const ROW_CARD_W = { full: 164, pill: 96 };              // карточка в строке дня: ширина
const ROW_CARD_H = 52;                                    // высота карточки в строке; рядов в строке несколько — каждый ниже (layoutRow)
const ROW_MIN_LANE_H = 22;                                // ряд ниже этого — уже не ряд, а стопка
const ROW_FULL_H = 52;                                    // ряд не ниже высоты карточки (52) — полная карточка с миниатюрой; ниже — пилюля (CALENDAR_CARDS_FINISH, 6)
const MIN_LANE_W = 60;         // уже этого — посты в колонке складываются в стопку
const ZOOM_SETTLE_MS = 180;    // пауза после жеста колеса или щипка: дальше вид прилипает к целым дням
const PAN_SNAP_PX = 8;          // фон: отпустили дальше этого — день прилипает к целым суткам, меньше — вид остаётся как есть
const CHIP_KEY = 'postaCalendarChannels';
const VIEW_KEY = 'postaCalendarView';
const AXIS_KEY = 'postaCalendarAxis';   // 'days-cols' — дни колонками (по умолчанию), 'days-rows' — дни строками
const ZOOM_KEY = 'postaCalendarZoom';
const FULL_KEY = 'postaCalendarFullDay';
const HINT_KEY = 'postaCalendarHintSeen';   // запасной вариант, если запрос «подсказка показана» не прошёл
const HINT_NAME = 'calendar_v3';
const PHONE = matchMedia('(max-width: 700px)');

const state = {
    channels: [], posts: [], disabled: new Set(), connected: false, view: 'grid', zoom: { startMs: 0, days: 7 }, fullDay: false, hourShift: 0, pendingCancel: new Map(),
    geo: null, panel: null, drag: false, droppedAt: 0, anim: 0, frame: 0, hintsSeen: null, listDays: 14, cards: new Map(),
    target: null, gesture: false, cursorX: 0, frozenHours: null, settleTimer: 0, animAt: 0,   // цель масштаба, жест, заморозка часов
    anchorY: null,   // доля высоты сетки, которая остаётся на месте при зуме (null — часы не трогаем)
    gestureFrom: 0, keepCentre: false,   // число дней в начале жеста; сохранять середину часов после доводки (жест и кнопки — да, пресеты — нет)
    panelPost: null, resourcesOpen: false,   // открытый пост панели; развёрнут ли блок «Куда публиковать» в панели поста
    centreHours: false,  // пресет: окно часов держится по центру базового окна на каждом кадре (иначе зажатый сдвиг теряется)
    axis: 'days-cols'    // вид сетки: дни колонками или строками
};
let body, panel, title, zoomLabel, statusEl, main, empty, hintBox, fullBtn, axisBtn, poolCol;   // poolCol — колонка «Предложка» слева

const pad = n => String(n).padStart(2, '0');
const hhmm = ms => { const d = new Date(ms); return `${pad(d.getHours())}:${pad(d.getMinutes())}`; };
const dayName = ms => { const d = new Date(ms); return `${WEEK_SHORT[(d.getDay() + 6) % 7]} ${d.getDate()} ${MONTHS_GEN[d.getMonth()]}`; };
const whenLabel = ms => `${dayName(ms)}, ${hhmm(ms)}`;
const unix = ms => Math.floor(ms / 1000);
const postKey = post => post.id || `${post.at}|${post.channel}|${(post.text || '').slice(0, 16)}`;
const midnight = ms => addDays(ms, 0);
const isPast = post => post.status === 'sent' || post.at * 1000 < Date.now();
const postWord = n => (n % 10 === 1 && n % 100 !== 11 ? 'пост' : n % 10 >= 2 && n % 10 <= 4 && (n % 100 < 12 || n % 100 > 14) ? 'поста' : 'постов');

/** Значок статуса: одинаковый во всех видах и в панели; queued значка не имеет. */
function statusBadge(post, compact = false) {
    const spec = BADGE[post.status];
    if (!spec) return null;
    if (compact) return el('i', { className: `cal-status-dot is-${spec[1]}`, role: 'img', 'aria-label': STATUS[post.status], title: STATUS[post.status] });   // узкая колонка — кружок 14 px
    const badge = el('span', { className: `cal-badge is-${spec[1]}`, textContent: spec[0] });
    setAttrs(badge, { role: 'img', 'aria-label': STATUS[post.status], title: STATUS[post.status] });
    return badge;
}

/** Атрибуты data-* и aria-* задаются через setAttribute: свойства в el() для них не работают. */
function setAttrs(node, attrs) {
    for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, String(value));
    return node;
}

function readStore(key, fallback) {
    try { const value = localStorage.getItem(key); return value === null ? fallback : JSON.parse(value); } catch { return fallback; }
}

function writeStore(key, value) {
    try { localStorage.setItem(key, JSON.stringify(value)); } catch { /* без памяти вид и выбор каналов сбросятся */ }
}

const STATUS_MS = 4000;   // обычное сообщение пропадает само; ошибка остаётся, пока её не закроют ✕
let statusTimer = 0;

/** Сообщение — плавающее поверх календаря (position: fixed в css): в поток не встаёт, сетка по нему не сдвигается.
 *  ms — сколько живёт обычное сообщение (с кнопкой «Вернуть» или «Отменить» — дольше). */
function setStatus(text, isError = false, ms = STATUS_MS) {
    clearTimeout(statusTimer);
    statusEl.textContent = text;
    statusEl.hidden = !text;
    statusEl.classList.toggle('is-error', isError);
    if (isError && text) statusEl.append(' ', el('button', { type: 'button', className: 'cal-status-close', textContent: '✕', ariaLabel: 'Закрыть сообщение', onclick: () => setStatus('') }));
    else if (text) statusTimer = setTimeout(() => setStatus(''), ms);
}

const justDropped = () => Date.now() - state.droppedAt < 400;

// ---- Каналы и данные ----

/** Цвет и название канала по id; посты неизвестного канала — серым «Другие». */
function laneOf(channelId) {
    const i = state.channels.findIndex(c => c.id === channelId);
    if (i < 0) return { color: 'var(--faint)', title: 'Другие' };
    const channel = state.channels[i];
    return { color: PALETTE[i % PALETTE.length], title: channel.title || channel.username || 'Канал' };
}

const enabledChannels = () => state.channels.filter(c => !state.disabled.has(c.id));

let historyTimer = 0;

async function loadCalendar() {
    try {
        const data = await api('calendar');
        state.connected = data.connected !== false;
        state.channels = data.channels || [];
        state.posts = (data.posts || []).filter(p => !state.pendingCancel.has(p.id));   // снятые с расписания до возврата не показываем
        state.hintsSeen = data.hints_seen ?? null;
        clearTimeout(historyTimer);
        if (data.history_pending) historyTimer = setTimeout(loadCalendar, 8000);   // прошлые посты каналов собираются в фоне — перечитаем
        const usable = state.connected && state.channels.length > 0;
        main.hidden = !usable;
        empty.hidden = usable;
        main.classList.toggle('is-single', state.channels.length < 2);
        if (!usable) return;
        state.disabled = new Set(readStore(CHIP_KEY, []).filter(id => state.channels.some(c => c.id === id)));
        renderAll();
        if (!state.panel) renderEmptyPanel();
        if (!state.hintsSeen && !readStore(HINT_KEY, false)) showHint();
        await renderPoolColumn(poolContext(), poolCol);   // телефон берёт число Предложки отсюда
        if (isPhone()) scheduleRender();
    } catch (error) {
        setStatus(error.message, true);
    }
}

// ---- Шапка: заголовок диапазона, масштаб, сводка, чипы каналов ----

function renderAll() {
    state.frame = 0;
    fitHeight();
    renderHeader();
    renderBody();
}

/** Сколько дней на экране: от одного до месяца (на телефоне — до трёх). */
const zoomLimits = () => ({ min: PHONE.matches ? 1 : ZOOM_DAYS_MIN, max: PHONE.matches ? 3 : ZOOM_DAYS_MAX });
const clampDays = days => Math.min(zoomLimits().max, Math.max(zoomLimits().min, days));

function scheduleRender() {
    if (!state.frame) state.frame = requestAnimationFrame(renderAll);
}

const FIT_RESERVE = 36;   // под сеткой: отступ панели вкладки (16 px) и отступ страницы снизу (20 px) — иначе страница выше окна
let fittedPx = 0, fittedRaw = 0;   // последняя записанная высота: без изменений стиль не трогаем

/** Высота сетки: весь блок календаря (шапка и сетка) — до низа окна. Верх вкладки берём от начала документа,
 *  а не от окна: прокрутка страницы высоту не меняет. Липкая плашка «вкладка неактивна» стоит в потоке выше вкладки, её высота уже в tabTop. */
function fitHeight() {
    const tab = $('calendar-tab');
    const tabTop = tab.getBoundingClientRect().top + scrollY;
    const headsH = main.getBoundingClientRect().top - tab.getBoundingClientRect().top;   // шапка календаря над сеткой
    const px = Math.max(360, Math.round(innerHeight - tabTop - headsH - FIT_RESERVE));
    if (px === fittedRaw) return;
    fittedRaw = fittedPx = px;
    main.style.height = `${px}px`;
    const over = document.documentElement.scrollHeight - innerHeight;   // запас не угадал: страница всё равно выше окна — убираем остаток
    if (over > 0 && px - over >= 360) { fittedPx = px - over; main.style.height = `${fittedPx}px`; }
}

function rangeTitle(startMs, days) {
    const first = new Date(midnight(startMs));
    const last = new Date(midnight(startMs + days * DAY - 1));
    if (first.getTime() === last.getTime()) return `${dayName(first.getTime())} ${first.getFullYear()}`;
    return first.getMonth() === last.getMonth()
        ? `${first.getDate()}–${last.getDate()} ${MONTHS_GEN[last.getMonth()]} ${last.getFullYear()}`
        : `${first.getDate()} ${MONTHS_GEN[first.getMonth()]} – ${last.getDate()} ${MONTHS_GEN[last.getMonth()]} ${last.getFullYear()}`;
}

function zoomName(days) {
    const n = Math.round(days);
    if (n <= 1) return '1 день';
    return { 3: '3 дня', 7: 'неделя', 14: '2 недели', 31: 'месяц' }[n] ?? `${n} дн.`;
}

function renderHeader() {
    const { startMs, days } = state.zoom;
    title.textContent = state.view === 'list' ? `С ${dayName(startMs)}` : rangeTitle(startMs, days);
    zoomLabel.textContent = zoomName(days);
    document.querySelectorAll('[data-cal-zoom]').forEach(b => b.setAttribute('aria-pressed', String(state.view === 'grid' && Math.round(days) === Number(b.dataset.calZoom))));
    document.querySelectorAll('[data-cal-view]').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.calView === state.view)));
    fullBtn.setAttribute('aria-pressed', String(state.fullDay));
    axisBtn.setAttribute('aria-pressed', String(state.axis === 'days-rows'));
    axisBtn.disabled = state.view !== 'grid';
}

/** Отмечен ли ресурс: отмеченные — цели постановки и видны в сетке (поле postaCalendarChannels хранит выключенные). */
const isOn = id => !state.disabled.has(id);

/** Включить или выключить ресурс. Хотя бы один остаётся отмеченным: последний снять нельзя. */
function toggleChannel(id, on = !state.disabled.has(id)) {
    const next = new Set(state.disabled);
    if (on) next.delete(id);
    else next.add(id);
    if (state.channels.every(c => next.has(c.id))) {
        setStatus('Хотя бы один ресурс должен остаться отмеченным.', true);
        return refreshPanel();
    }
    state.disabled = next;
    writeStore(CHIP_KEY, [...next]);
    renderAll();
    refreshPanel();
}

// ---- Масштаб и движение по времени ----

/** Остановить доводку (перетаскивание начинается с места, где вид сейчас). */
function stopAnim() {
    if (!state.target) return;
    cancelAnimationFrame(state.anim);
    state.anim = 0;
    clearTimeout(state.settleTimer);
    state.gesture = false;
    finishZoom();
}

function persistZoom() {
    writeStore(ZOOM_KEY, Math.round(state.zoom.days * 100) / 100);
}

/** Конец движения: цель снята, окно часов пересчитано (одним кадром), масштаб записан один раз. */
function finishZoom() {
    const g = state.geo;
    const { startMs, days } = state.zoom;
    if (g && !PHONE.matches) {   // базовое окно могло смениться за время движения — сохраняем то, что видит пользователь
        const [b0, b1] = visibleHours(startMs, startMs + days * DAY);
        if (state.centreHours) state.hourShift = centeredHourShift([b0, b1], hourSpan(b1 - b0, days, zoomLimits()));   // пресет — по центру
        else if (state.keepCentre) state.hourShift = g.sh - b0;   // жест и кнопки — точка под курсором (абсолютное начало окна) остаётся на месте
    }
    state.target = null;
    state.frozenHours = null;
    state.anchorY = null;
    state.centreHours = false;
    persistZoom();
    renderAll();
}

/** Раньше чем за неделю до сегодня уйти нельзя: левый край вида не меньше полуночи 7 дней назад. */
const clampStart = startMs => Math.max(startMs, addDays(midnight(Date.now()), -7));

/** Цель масштаба или сдвига: вид тянется к ней (stepZoom). gesture — колесо или щипок: прилипание через ZOOM_SETTLE_MS после последнего шага. */
function aimZoom(target, { gesture = false, cursorX = state.cursorX, anchorY = null, centre = false } = {}) {
    if (gesture && !state.gesture) state.gestureFrom = state.zoom.days;   // первый щелчок жеста — от этого масштаба
    state.target = { days: target.days, startMs: clampStart(target.startMs) };
    state.cursorX = cursorX;
    state.anchorY = anchorY;
    state.centreHours = centre;
    state.keepCentre = gesture || anchorY !== null;
    state.gesture = gesture;
    clearTimeout(state.settleTimer);
    state.settleTimer = gesture ? setTimeout(settleZoom, ZOOM_SETTLE_MS) : 0;
    if (!state.frozenHours && state.geo) state.frozenHours = { sh: state.geo.baseSh, eh: state.geo.baseEh };   // часы не дышат во время жеста (базовое окно, без сдвига)
    kickZoom();
}

function kickZoom() {
    if (!state.anim) state.anim = requestAnimationFrame(stepZoom);
}

/** Кадр доводки. Лёгкий: шапка и сетка, без высоты, чипов и записи в localStorage. */
function stepZoom(now) {
    const target = state.target;
    if (!target) { state.anim = 0; return; }
    const last = state.animAt || now - 16;
    state.animAt = now;
    const reached = viewGapPx(state.zoom, target, state.geo?.dayLen ?? 0) < 0.5;
    state.zoom = reached ? { ...target } : easeView(state.zoom, target, now - last);
    anchorHours();
    renderMotion();
    if (reached) {
        state.anim = 0;
        state.animAt = 0;
        if (!state.gesture) finishZoom();   // жест ещё идёт — дойдём до конца после паузы
        return;
    }
    state.anim = requestAnimationFrame(stepZoom);
}

function renderMotion() {
    renderHeader();
    renderBody();
}

/** Пауза после жеста: цель прилипает к целым дням и полуночи, день под курсором остаётся на месте. */
function settleZoom() {
    state.settleTimer = 0;
    if (!state.target) return;
    state.gesture = false;
    const lim = zoomLimits();
    const days = snapGestureDays(state.target.days, state.gestureFrom, lim);
    state.target = snapView(state.target, state.cursorX, state.geo?.dayLen ?? 0, lim, days);
    kickZoom();
}

/** Плавный переход к виду (кнопки «День», «Неделя», «Месяц», «Сегодня», клик по стопке). */
function tweenTo(target, anchorY = null) {
    aimZoom(target, { anchorY });
}

/** Окно часов по центру базового окна целевого диапазона (пресеты и «Круглые сутки»): видна середина рабочего дня. */
function recenterHours(days, startMs) {
    if (PHONE.matches) { state.hourShift = 0; return; }
    const base = visibleHours(startMs, startMs + days * DAY);
    state.hourShift = centeredHourShift(base, hourSpan(base[1] - base[0], days, zoomLimits()));
}

/** Кадр зума: часы по вертикали — либо по центру базового окна (пресет), либо с точкой под курсором на месте (жест и кнопки). */
function anchorHours() {
    const g = state.geo;
    if (!g || PHONE.matches) return;
    const span = hourSpan(g.baseEh - g.baseSh, state.zoom.days, zoomLimits());
    if (state.centreHours) { state.hourShift = centeredHourShift([g.baseSh, g.baseEh], span); return; }
    if (state.anchorY === null) return;
    state.hourShift = zoomHoursAround([g.sh, g.eh], state.anchorY, span)[0] - g.baseSh;
}

/** Пресет масштаба — относительно центра текущего вида. */
function presetZoom(days) {
    const target = clampDays(days);
    const base = state.target ?? state.zoom;
    const center = base.startMs + base.days * DAY / 2;
    const startMs = center - target * DAY / 2;
    aimZoom({ days: target, startMs }, { centre: true });   // пресет — видна середина базового окна часов, а не его начало
}

/** Масштаб у точки на экране (clientX, clientY): день под указателем остаётся под ним. Цель строится от цели, а не от кадра.
 *  centre — «−/+» и щипок: часы держатся по середине сетки. */
function zoomAtClient(clientX, clientY, factor, { gesture = true, centre = false } = {}) {
    const g = state.geo;
    if (!g) return;
    const rect = body.getBoundingClientRect();
    const dayCursor = rows() ? clientY - rect.top - headH() : clientX - rect.left - AXIS_W;   // курсор на оси дней
    const anchorY = centre ? 0.5 : clamp01(rows() ? (clientX - rect.left - AXIS_W) / g.width : (clientY - rect.top - HEAD_H) / g.height);   // доля оси часов
    const base = state.target ?? state.zoom;
    const next = zoomAround({ startMs: base.startMs, days: base.days, width: g.dayLen }, dayCursor, factor, zoomLimits());
    aimZoom({ days: next.days, startMs: next.startMs }, { gesture, cursorX: dayCursor, anchorY });
}

const clamp01 = v => Math.min(1, Math.max(0, v));

/** Размораживание часов: окно остаётся на месте по абсолютному началу (как в finishZoom), иначе сетка прыгнула бы по вертикали. */
function thawHours() {
    if (state.frozenHours && state.geo && !PHONE.matches) {
        renderAll();   // сдвиг последнего кадра уже в state.hourShift, но геометрия могла не успеть обновиться
        const [b0] = visibleHours(state.zoom.startMs, state.zoom.startMs + state.zoom.days * DAY);
        state.hourShift = state.geo.sh - b0;
    }
    state.frozenHours = null;
    scheduleRender();
}

/** Отпускание фона: начало вида прилипает к ближайшей полуночи (масштаб не меняется), часы не прилипают. Курсор не используется: прилипание к суткам под ним отменило бы тягу. */
function endPan(dx) {
    if (Math.abs(dx) < PAN_SNAP_PX) { thawHours(); return; }
    aimZoom({ days: state.zoom.days, startMs: midnight(state.zoom.startMs + DAY / 2) });
    state.keepCentre = true;   // окно часов остаётся на месте и при доводке, и после неё
}

function zoomBy(factor) {
    const g = state.geo;
    if (!g) return;
    const rect = body.getBoundingClientRect();
    zoomAtClient(rect.left + AXIS_W + g.width / 2, rect.top + headH() + g.height / 2, factor, { centre: true });
}

/** Сдвиг на n дней (края при перетаскивании): через цель, календарно — addDays. */
function shiftDays(n) {
    const base = state.target ?? state.zoom;
    aimZoom({ days: base.days, startMs: addDays(base.startMs, n) });
}

/** Сдвиг по горизонтали в пикселях колонок сетки: пропорционально масштабу. */
function shiftView(px) {
    const base = state.target ?? state.zoom;
    aimZoom({ days: base.days, startMs: base.startMs + px / state.geo.dayLen * base.days * DAY }, { gesture: true });
}

/** Колесо: плавный масштаб у курсора; Shift или горизонтальная прокрутка — сдвиг. Страницу перехватываем, только если вид меняется:
 *  за пределом масштаба колесо прокручивает страницу, над панелью поста и предложкой — не трогаем. */
function onWheel(event) {
    if (state.view !== 'grid' || !state.geo || event.target.closest('.cal-panel, .cal-pool-col')) return;
    const g = state.geo;
    const rect = body.getBoundingClientRect();
    const overAxis = rows() ? event.clientY - rect.top < headH() : event.clientX - rect.left < AXIS_W;   // ось часов не ловит мышь: смотрим по координате
    if (!event.ctrlKey && !event.shiftKey && (overAxis || event.altKey)) return wheelHours(event);
    const dx = wheelPixels(event.deltaX, event.deltaMode, g.height);
    const dy = wheelPixels(event.deltaY, event.deltaMode, g.height);
    if (!event.ctrlKey && (event.shiftKey || Math.abs(dx) > Math.abs(dy))) {
        const px = dx || dy;
        if (!px) return;
        if (rows()) {   // строки: горизонтальная прокрутка листает часы, дни — вертикальной
            if (moveHours(px)) event.preventDefault();
            return;
        }
        event.preventDefault();
        return shiftView(px);
    }
    const days = (state.target ?? state.zoom).days;
    if (!dy || zoomLimitReached(days, dy, zoomLimits())) return;
    event.preventDefault();
    zoomAtClient(event.clientX, event.clientY, wheelFactor(dy));
}

/** Колесо над осью часов (и Alt над сеткой) листает часы; в строках — горизонталь. У края 0:00 или 24:00 отдаёт прокрутку странице. */
function wheelHours(event) {
    const g = state.geo;
    const horizontal = rows() && Math.abs(event.deltaX) > Math.abs(event.deltaY);
    const px = horizontal ? wheelPixels(event.deltaX, event.deltaMode, g.width) : wheelPixels(event.deltaY, event.deltaMode, g.height);
    if (px && moveHours(px)) event.preventDefault();
}

/** Окно часов на px пикселей вдоль оси часов; false — упёрлись в 0:00 или 24:00. */
function moveHours(px) {
    const g = state.geo;
    const next = Math.min(Math.max(g.sh + px / (g.ppm * 60), 0), 24 - (g.eh - g.sh));
    if (Math.abs(next - g.sh) < 1e-6) return false;
    state.anchorY = null;
    state.hourShift = next - g.baseSh;
    scheduleRender();
    return true;
}

/** Сдвиг по дням с кнопок ‹ › и стрелок: на видимый диапазон (в списке — неделя). */
function step(direction) {
    const base = state.target ?? state.zoom;
    shiftDays(direction * (state.view === 'list' ? 7 : Math.round(base.days)));
}

function setView(view) {
    state.view = view;
    writeStore(VIEW_KEY, view);
    renderAll();
}

// ---- Сетка: два вида — «дни колонками» (часы вертикально) и «дни строками» (часы горизонтально) ----

/** Вид «дни строками»: только сетка на компьютере. На телефоне и в списке не действует. */
const rows = () => state.axis === 'days-rows' && state.view === 'grid' && !isPhone();
/** Полоса над сеткой: шапка дня (колонки) или полоса часов (строки). */
const headH = () => (rows() ? ROWS_HEAD_H : HEAD_H);
/** Положение момента ms внутри дня dayMs вдоль оси часов: y в колонке, x в строке; 0 — начало окна часов. */
const timeToPx = (dayMs, ms) => ((ms - dayMs) / MIN - state.geo.sh * 60) * state.geo.ppm;
/** Точка на оси часов: top в колонке, left в строке. */
function setPos(node, px) {
    if (rows()) node.style.left = `${px}px`;
    else node.style.top = `${px}px`;
    return node;
}
/** Отрезок на оси часов: top/height в колонке, left/width в строке. */
function alongHours(node, start, length) {
    if (rows()) Object.assign(node.style, { left: `${start}px`, width: `${length}px` });
    else Object.assign(node.style, { top: `${start}px`, height: `${length}px` });
    return node;
}
/** Место записи раскладки: в колонке x — от края колонки, y — по времени; в строке x — по времени, y — внутри строки. */
const entryBox = (entry, g) => (g.rows ? { x: entry.left, y: GUTTER + entry.top, w: entry.width } : { x: GUTTER + entry.left, y: entry.top, w: entry.width });

/** Окно часов по постам и слотам видимого диапазона. Во время жеста не пересчитывается (state.frozenHours). */
function visibleHours(startMs, endMs) {
    const slots = enabledChannels().flatMap(c => slotTimes(c.schedule, startMs, endMs));
    return hourWindow(state.posts.map(p => p.at * 1000), slots, startMs, endMs, state.fullDay);   // весь день — только по «Круглые сутки»
}

/** Геометрия сетки по размеру окна: окно часов, px на минуту (ppm), px на день (pxPerDay), длина оси дней (dayLen). null — вкладка скрыта. */
function computeGeo(now) {
    const width = body.clientWidth - AXIS_W;
    const height = body.clientHeight - headH();
    if (width <= 0 || height <= 0) return null;
    const { startMs, days } = state.zoom;
    const endMs = startMs + days * DAY;
    const base = state.frozenHours ? [state.frozenHours.sh, state.frozenHours.eh] : visibleHours(startMs, endMs);
    // Видимая длина окна часов следует за масштабом дней (hourSpan); сдвиг по часам зажат в [0, 24]
    const span = PHONE.matches ? base[1] - base[0] : hourSpan(base[1] - base[0], days, zoomLimits());
    const [sh, eh] = shiftHourWindow([base[0], base[0] + span], state.hourShift);
    state.hourShift = sh - base[0];
    const isRows = rows();
    const hourLen = isRows ? width : height;   // ось часов: высота колонки или ширина строки
    const dayLen = isRows ? height : width;    // ось дней: ширина колонок или высота строк
    const ppm = hourLen / ((eh - sh) * 60);
    const pxPerDay = dayLen / days;
    return { width, height, sh, eh, baseSh: base[0], baseEh: base[1], ppm, pxPerDay, dayLen, rows: isRows, rowMode: pxPerDay >= 60 ? 'full' : 'pill', startMs, days, now };
}

function gridNodes(now) {
    const g = computeGeo(now);
    if (!g) return null;
    state.geo = g;
    body.classList.toggle('is-cut-top', !g.rows && g.sh > 0.01 && g.eh - g.sh < g.baseEh - g.baseSh - 0.01);   // часы выше окна — тень у края
    body.classList.toggle('is-cut-bottom', !g.rows && g.eh < 23.99 && g.eh - g.sh < g.baseEh - g.baseSh - 0.01);
    const enabled = enabledChannels();
    body.style.setProperty('--hour-px', `${g.ppm * 60}px`);
    body.classList.toggle('is-half', g.ppm * 60 >= 48);
    body.classList.toggle('is-tenth', g.ppm * 60 >= 120);
    const seen = new Set();
    const days = [];
    for (let day = midnight(g.startMs); day < g.startMs + g.days * DAY; day = addDays(day, 1)) days.push(dayTrack(day, enabled, now, seen));
    for (const key of state.cards.keys()) if (!seen.has(key)) state.cards.delete(key);
    return [axisNode(g, now), el('div', { className: 'cal-days' }, ...days)];
}

/** Подписи часов и метка «сейчас» на оси часов: слева (колонки) или сверху (строки). */
function axisNode(g, now) {
    const step = Math.max(1, Math.ceil(16 / (g.ppm * 60)));
    const reach = bestHoursInfo();   // полоски охвата по часам и подсветка лучших
    const hourMax = Math.max(1, ...Object.values(reach?.hours ?? {}));
    const labels = [el('span', { className: 'cal-axis-title', textContent: reach ? 'охват по часам' : 'мало данных' })];
    for (let h = Math.ceil(g.sh - 0.05); h < g.eh; h += step) {
        const at = (g.rows ? AXIS_W : HEAD_H) + (h - g.sh) * g.ppm * 60;
        const median = reach?.hours?.[h] ?? 0;
        const best = reach?.best?.includes(h);
        const children = [];
        if (reach && median > 0) {
            const level = best || median >= hourMax * 0.85 ? ' is-best' : median >= hourMax * 0.6 ? ' is-good' : '';
            const bar = el('i', { className: `cal-reach-bar${level}`, 'aria-hidden': 'true' });
            bar.style.width = `${Math.round((median / hourMax) * 36)}px`;
            children.push(bar);
        }
        labels.push(setPos(el('span', { className: `cal-hour${best ? ' is-best' : ''}`, textContent: `${pad(h)}:00`, title: reach ? `охват ≈ ${median} просмотров` : 'мало данных для охвата' }, ...children), at));
    }
    return el('div', { className: 'cal-axis' }, ...labels);
}

/** Один день: колонка (колонки) или строка (строки) с шапкой дня, постами, слотами и линией «сейчас». */
function dayTrack(day, enabled, now, seen) {
    const g = state.geo;
    const today = day === midnight(now);
    const weekend = [0, 6].includes(new Date(day).getDay());
    const agenda = dayAgenda(state.posts, enabled, day, now);
    const best = bestHoursInfo()?.best ?? [];
    const hole = agenda.some(r => r.kind === 'slot' && best.includes(new Date(r.at).getHours()));   // свободный слот в лучший час
    const section = el('section', { className: `cal-day${today ? ' is-today' : ''}${isPastDay(day, now) ? ' is-past' : ''}${weekend ? ' is-weekend' : ''}` },
        dayHead(day, hole, g.rows || g.pxPerDay < 130, today));
    section.classList.toggle('is-narrow', !g.rows && g.pxPerDay < 100);   // узкая колонка: день недели и число, месяц не пишем
    const start = (day - g.startMs) / DAY * g.pxPerDay;   // отсчёт от начала видимого диапазона
    if (g.rows) Object.assign(section.style, { top: `${start}px`, height: `${g.pxPerDay}px` });
    else Object.assign(section.style, { left: `${start}px`, width: `${g.pxPerDay}px` });
    setAttrs(section, { 'data-drop-day': day });
    const area = el('div', { className: 'cal-col' });
    const origin = day + g.sh * HOUR;
    for (const h of best) area.append(alongHours(el('div', { className: 'cal-best-row', 'aria-hidden': 'true' }), timeToPx(day, day + h * HOUR), 60 * g.ppm));   // подложка лучших часов канала
    // Посты и свободные слоты — одна раскладка: слот занимает место как событие и никогда не прячется под карточкой
    const laid = g.rows
        ? layoutRow(agenda, { originMs: origin, pxPerMinute: g.ppm, rowHeight: g.pxPerDay, rowWidth: g.width, cardWidth: ROW_CARD_W[g.rowMode], cardHeight: ROW_CARD_H, minLaneH: ROW_MIN_LANE_H })
        : layoutColumn(agenda, origin, g);
    const slotMode = entry => (g.rows ? cardMode(entry) : g.pxPerDay >= 150 ? 'full' : g.pxPerDay >= 60 ? 'mid' : 'pill');
    const cardMode = entry => (g.rows ? (entry.height >= ROW_FULL_H ? 'full' : 'pill') : entry.width >= 150 ? 'full' : entry.width >= 60 ? 'mid' : 'pill');   // содержимое — по ширине полосы или высоте ряда
    for (const entry of laid) {
        const raised = g.rows ? entry.type === 'card' && Math.abs(entry.left - entry.timeLeft) > 0.5 : Math.abs(entry.top - entry.timeTop) > 0.5;
        if (raised) area.append(timeTick(entry, g));   // карточка поднята к краю — засечка на настоящем времени
        if (entry.type === 'stack') {
            for (const item of entry.items) if (item.post) seen.add(postKey(item.post));
            area.append(stackNode(entry, day));
        }
        else if (entry.type === 'cover') {
            for (const item of entry.items) seen.add(postKey(item.post));
            area.append(coverNode(entry));
        } else if (entry.item.kind === 'slot') {
            const plan = ghostFor(entry.item);
            area.append(plan ? ghostNode(plan, entry, g) : slotPill(entry.item, entry, slotMode(entry)));
        } else {
            const card = cardNode(entry.item.post, cardMode(entry), g.rows);
            seen.add(postKey(entry.item.post));
            area.append(placeCard(card, entry, g));
        }
    }
    if (today && now >= g.startMs && now < g.startMs + g.days * DAY) area.append(nowLine(day, now));
    section.append(area);
    return section;
}

/** Раскладка колонки дня: колонки внутри пересекающихся групп (layoutDay); при узкой колонке — стопка. */
function layoutColumn(agenda, origin, g) {
    const width = g.pxPerDay - 2 * GUTTER;
    const widest = width >= 150 ? 'full' : width >= 60 ? 'mid' : 'pill';   // самая высокая карточка, которая может встать в эту колонку
    return layoutDay(agenda, { originMs: origin, pxPerMinute: g.ppm, colWidth: width, cardHeight: CARD_H[widest], minWidth: MIN_LANE_W, colHeight: g.height });
}

const isPastDay = (day, now) => addDays(day, 1) <= midnight(now);

/** Шапка дня: день недели, число; месяц — только у 1-го числа. Метка «дыра в лучшее время» — строкой или красной точкой.
 *  narrow — только точка (колонка узкая или строка дня). */
function dayHead(day, hole, narrow, today = false) {
    const d = new Date(day);
    const parts = [el('span', { className: 'cal-dow', textContent: WEEK_SHORT[(d.getDay() + 6) % 7] }), el('span', { className: 'cal-dnum', textContent: String(d.getDate()) })];
    if (today && !narrow) parts.push(el('span', { className: 'cal-today-label', textContent: 'сегодня' }));   // в узкой колонке не помещается: сегодня и так выделено жёлтой шапкой
    if (d.getDate() === 1) parts.push(el('span', { className: 'cal-mon', textContent: MONTHS_SHORT[d.getMonth()] }));
    if (hole) parts.push(narrow
        ? el('i', { className: 'cal-hole-dot', title: 'дыра в лучшее время', 'aria-label': 'дыра в лучшее время' })
        : el('span', { className: 'cal-hole-note', textContent: 'дыра в лучшее время' }));
    return el('header', { className: 'cal-day-head' }, ...parts);
}

/** Засечка на истинном времени поднятой карточки: тонкая черта в цвете канала. */
function timeTick(entry, g) {
    const row = entry.items?.[0] ?? entry.item;
    const channel = row.kind === 'slot' ? row.channel.id : row.post.channel;
    const tick = el('i', { className: 'cal-time-tick', 'aria-hidden': 'true' });
    if (g.rows) {
        tick.style.left = `${Math.min(Math.max(0, entry.timeLeft - 1), g.width - 8)}px`;   // у правого края засечка не выходит за строку
        tick.style.top = `${GUTTER + entry.top}px`;
    } else {
        tick.style.top = `${Math.min(Math.max(0, entry.timeTop - 4), g.height - 8)}px`;   // у самого низа засечка не выходит за колонку
        tick.style.left = `${GUTTER + entry.left}px`;
    }
    tick.style.setProperty('--c', laneOf(channel).color);
    return tick;
}

function placeNode(node, x, y, width) {
    node.style.transform = `translate(${x}px, ${y}px)`;
    node.style.width = `${width}px`;
}

/** Карточка на месте записи: в колонке — полоса с потолком ширины, в строке — фиксированная ширина и высота ряда. */
function placeCard(card, entry, g) {
    const box = entryBox(entry, g);
    placeNode(card, box.x, box.y, g.rows ? box.w : Math.min(box.w - 4, CARD_MAX_W));
    card.style.height = g.rows ? `${entry.height}px` : '';
    return card;
}

function nowLine(day, now) {
    const line = el('div', { className: 'cal-now', role: 'separator' }, el('span', { className: 'cal-now-tag-col', textContent: hhmm(now) }));   // метка — у правого края колонки
    return setPos(line, timeToPx(day, now));
}
// ---- Карточки ----

/** Карточка поста: узел берётся из кэша по ключу; перестраивается только если изменилось содержимое. */
function cardNode(post, mode, rows = false) {
    const key = postKey(post);
    let card = state.cards.get(key);
    if (!card) {
        card = el('button', { type: 'button', className: 'cal-card' });
        setAttrs(card, { 'data-post-key': key });
        card.addEventListener('click', () => { if (!justDropped()) openPanel(card.post, card); });
        card.addEventListener('pointerdown', event => startDrag(event, card.post, card, host));
        state.cards.set(key, card);
    }
    card.post = post;
    const sig = [mode, rows, post.status, post.at, post.text, post.thumb_url, post.media, post.group, post.forecast, post.duplicate ? 1 : 0, post.error, post.channel, isPast(post)].join('|');
    if (card.dataset.sig !== sig) {
        fillCard(card, post, mode, rows);
        card.dataset.sig = sig;
    }
    return card;
}

/** Цвет буквы на фоне: тёмная на светлом (жёлтый канал), белая на тёмном. Порог — там, где контраст с обоими одинаковый. */
function inkOn(hex) {
    const m = /^#([0-9a-f]{6})$/i.exec(hex || '');
    if (!m) return '#111111';
    const lin = c => { const v = parseInt(c, 16) / 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; };
    const [r, g, b] = [m[1].slice(0, 2), m[1].slice(2, 4), m[1].slice(4, 6)].map(lin);
    return 0.2126 * r + 0.7152 * g + 0.0722 * b > 0.179 ? '#111111' : '#FFFFFF';
}

/** Аватар канала: первая буква названия на его цвете (в узкой карточке — без буквы, только кружок). */
function channelAvatar(channelId, size, letter = true) {
    const lane = laneOf(channelId);
    const av = el('span', { className: `cal-avatar is-${size}`, textContent: letter ? (lane.title || '?').trim().charAt(0).toUpperCase() : '', title: lane.title, 'aria-hidden': 'true' });
    av.style.setProperty('--c', lane.color);
    av.style.setProperty('--ink', inkOn(lane.color));
    return av;
}

/** Каналы одной записи (группа из календаря): поле group связывает посты, у которых одна запись в нескольких ресурсах. */
function groupChannels(post) {
    if (!post.group) return [post.channel];
    return [...new Set(state.posts.filter(p => p.group === post.group).map(p => p.channel))];
}

/** Аватары группы внахлёст: до трёх каналов и «+N». */
function avatarStack(post, size, letter = true) {
    const ids = groupChannels(post);
    const shown = ids.slice(0, 3).map(id => channelAvatar(id, size, letter));
    if (ids.length > 3) shown.push(el('span', { className: `cal-avatar-more is-${size}`, textContent: `+${ids.length - 3}` }));
    return el('span', { className: 'cal-avatars', title: ids.length > 1 ? `в ${ids.length} каналах` : '' }, ...shown);
}

/** Плашка статуса на миниатюре (полная карточка): текст и цвет по статусу; прошедший пост — «вышел». Для очереди — нет. */
function statusPlate(post) {
    if (post.status === 'failed') return ['ошибка', 'is-danger'];
    if (post.status === 'sending') return ['уходит…', 'is-accent'];
    if (post.status === 'telegram') return ['в TG', 'is-blue'];
    if (post.status === 'sent' || isPast(post)) return ['вышел', 'is-ok'];
    return null;
}

/** Точка статуса рядом со временем (средняя и пилюля): без точки — обычный или прошедший пост. */
function statusDot(post) {
    const dot = { failed: 'is-danger', telegram: 'is-blue', sending: 'is-accent' }[post.status];
    return dot ? el('i', { className: `cal-status-dot ${dot}`, role: 'img', 'aria-label': STATUS[post.status], title: STATUS[post.status] }) : null;
}

/** Миниатюра карточки: картинка или камера; без медиа — большая кавычка на тонированном фоне. Полная карточка — ещё и плашка статуса. */
function cardMedia(post, mode) {
    const box = el('span', { className: 'cal-media' });
    box.append(mediaNode(post) || el('span', { className: 'cal-quote', textContent: '“', 'aria-hidden': 'true' }));
    if (mode === 'full') {
        const plate = statusPlate(post);
        if (plate) box.append(el('span', { className: `cal-plate ${plate[1]}`, textContent: plate[0] }));
        if (post.status === 'sending') box.append(el('span', { className: 'cal-plate-bar', 'aria-hidden': 'true' }));
    }
    return box;
}

/** Содержимое карточки по ширине — как в утверждённом макете (cards-mock.html):
 *  полная — миниатюра во всю высоту, время, сразу за ним аватар канала, справа прогноз или «повтор», текст, при ошибке — причина под заголовком;
 *  средняя — первая строка: миниатюра 24, время, точка статуса, аватар справа; вторая: текст во всю ширину (при ошибке — «ошибка»);
 *  пилюля — время и точка статуса, без аватара. */
function fillCard(card, post, mode, rows = false) {
    const lane = laneOf(post.channel);
    const past = isPast(post);
    const when = el('b', { className: 'cal-when', textContent: hhmm(post.at * 1000) });
    const dot = mode === 'full' ? null : statusDot(post);
    const avatars = mode === 'pill' ? null : avatarStack(post, mode === 'full' ? 'lg' : 'sm', mode === 'full');
    const meta = mode === 'full' && !rows && post.duplicate ? el('span', { className: 'cal-dup', textContent: 'повтор', title: 'Похожий пост уже выходил' })
        : mode === 'full' && !rows && post.forecast && !past ? el('span', { className: 'cal-forecast', textContent: `≈${fmtViews(post.forecast)}`, title: 'Прогноз просмотров по часу канала' })
        : null;
    const head = el('span', { className: 'cal-card-row' }, when, ...(dot ? [dot] : []), ...(avatars ? [avatars] : []), el('span', { className: 'cal-spacer' }), ...(meta ? [meta] : []));
    const failed = post.status === 'failed';
    const title = el('span', { className: 'cal-text', textContent: post.text || postTypeLabel(post) });
    const reason = failed && post.error && mode === 'full' ? el('span', { className: 'cal-err', textContent: post.error }) : null;
    if (mode === 'mid') {
        // средняя: миниатюра и первая строка в одном ряду, текст — во всю ширину под ними
        const top = el('span', { className: 'cal-card-top' }, cardMedia(post, mode), head);
        card.replaceChildren(top, el('span', { className: 'cal-text', textContent: failed ? 'ошибка' : (post.text || postTypeLabel(post)) }));
    } else if (mode === 'pill') {
        card.replaceChildren(el('span', { className: 'cal-card-body' }, head));
    } else {
        const body = el('span', { className: 'cal-card-body' }, head, title, ...(reason ? [reason] : []));
        card.replaceChildren(cardMedia(post, mode), body);
    }
    card.dataset.mode = mode;
    card.dataset.status = post.status;
    card.classList.toggle('is-past', past);
    card.classList.toggle('is-duplicate', !!post.duplicate);
    card.style.setProperty('--c', lane.color);
    const groupN = groupChannels(post).length;
    setAttrs(card, {
        'aria-label': `Пост, ${dayName(post.at * 1000)} ${hhmm(post.at * 1000)}, ${STATUS_SHORT[post.status] || post.status}${groupN > 1 ? `, в ${groupN} каналах` : `, ${lane.title}`}${post.duplicate ? ', похожий пост уже выходил' : ''}`,
        title: `${lane.title}${groupN > 1 ? ` и ещё ${groupN - 1}` : ''} · ${whenLabel(post.at * 1000)}\n${(post.full_text || post.text || '').slice(0, 200)}`
    });
}

/** Картинка поста; без картинки, но с медиа — 📷 с числом; без медиа — пусто. */
function mediaNode(post) {
    if (post.thumb_url) {
        const img = el('img', { className: 'cal-thumb', src: post.thumb_url, alt: '', loading: 'lazy' });
        img.addEventListener('error', () => img.replaceWith(placeholder(post)));
        return img;
    }
    return post.media > 0 ? placeholder(post) : null;
}

/** Нет картинки, но в посте есть медиа: значок камеры без цифр (число файлов — в подсказке). */
const CAMERA_ICON = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 8h4l2-3h6l2 3h4v11H3z" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/><circle cx="12" cy="13" r="3.5" fill="none" stroke="currentColor" stroke-width="2"/></svg>';
const placeholder = post => el('span', { className: 'cal-ph', title: `Медиа: ${post.media || 1}`, 'aria-hidden': 'true', innerHTML: post.kind === 'video' ? VIDEO_ICON : PHOTO_ICON });

/** Каскад: несколько постов, которые не помещаются рядом, лежат веером настоящих карточек (как отдельные, по ширине колонки): из-под каждой
 *  торчит её верхняя полоса, по наведению карточка целиком выходит вперёд. Клик по карточке — как обычно, кружок с числом — приблизить день. */
function stackNode(entry, day) {
    const g = state.geo;
    const items = [...entry.items].sort((a, b) => a.at - b.at);
    const n = items.length;
    const shown = items.slice(0, CASCADE_MAX);
    const word = items.every(i => i.kind === 'slot') ? `${n} свободно` : `${n} ${postWord(n)}`;
    const box = g.rows ? { x: entry.left, y: GUTTER + entry.top, w: entry.width } : { x: GUTTER, y: entry.top, w: g.pxPerDay - 2 * GUTTER };
    const mode = g.rows ? (entry.height >= ROW_FULL_H ? 'full' : 'pill') : box.w >= 150 ? 'full' : box.w >= 60 ? 'mid' : 'pill';
    const cardH = g.rows ? (mode === 'full' ? ROW_CARD_H : CARD_H.pill) : CARD_H[mode];
    let peek = CASCADE_PEEK[mode];
    if (g.rows && shown.length > 1) peek = Math.max(8, Math.min(peek, Math.floor((entry.height - cardH) / (shown.length - 1))));   // в строке дня всё вместе — не выше ряда
    const step = Math.max(0, Math.min(CASCADE_SHIFT, Math.floor((box.w - 44) / Math.max(1, shown.length - 1))));   // сдвиг вправо, пока карточка не уже 40 px
    const width = Math.max(40, box.w - 4 - step * (shown.length - 1));
    const cascade = el('div', { className: 'cal-cascade' });
    shown.forEach((item, i) => {
        let node;
        if (item.kind === 'slot') {
            node = slotPill(item, { ...entry, width }, 'pill');
        } else {
            node = cardNode(item.post, mode, g.rows);
            node.dataset.text = (item.post.text || postTypeLabel(item.post)).replace(/\s+/g, ' ');
        }
        node.style.setProperty('--z', i + 1);
        placeNode(node, step * i, peek * i, width);
        cascade.append(node);
    });
    const count = el('button', { type: 'button', className: 'cal-cascade-count', textContent: String(n), title: `${word} · ${hhmm(entry.from)}–${hhmm(entry.to)}: приблизить день` });
    setAttrs(count, { 'aria-label': `${word}, ${hhmm(entry.from)}–${hhmm(entry.to)}: приблизить день` });
    count.addEventListener('click', event => { event.stopPropagation(); if (!justDropped()) tweenTo({ days: 1, startMs: day }); });
    cascade.append(count);
    placeNode(cascade, box.x, box.y, box.w);
    cascade.style.height = `${cardH + peek * (shown.length - 1)}px`;
    return cascade;
}

/** Призрачная миниатюра будущего поста из плана «Заполнить дыры» на месте свободного слота. */
const ghostFor = row => (state.ghosts ?? []).find(g => Math.abs(g.slot_at * 1000 - row.at) < 60000);

function ghostNode(plan, entry, g) {
    const ghost = el('div', { className: 'cal-ghost-card', title: `План: ${plan.text || 'пост'}` },
        plan.display ? el('img', { src: plan.display, alt: '', loading: 'lazy' }) : el('span', { className: 'cal-text-ph', textContent: 'план' }));
    const box = entryBox(entry, g);
    placeNode(ghost, box.x, box.y, box.w);
    ghost.style.height = `${g.rows ? entry.height : CARD_H.full}px`;
    return ghost;
}

/** Два-три поста в одно время в узкой колонке: первая карточка целиком, бейдж «+N» открывает список этого места. */
function coverNode(entry) {
    const wrap = el('div', { className: 'cal-cover' });
    const card = cardNode(entry.items[0].post, entry.width >= 150 ? 'full' : 'mid');
    placeNode(card, 0, 0, entry.width);
    const more = entry.items.length - 1;
    const list = el('div', { className: 'cal-cover-list', hidden: true },
        ...entry.items.map(item => el('button', { type: 'button', className: 'cal-cover-item', textContent: `${hhmm(item.post.at * 1000)} · ${item.post.text || 'Без текста'}`, onclick: () => openPanel(item.post, wrap) })));
    const badge = el('button', { type: 'button', className: 'cal-cover-more', textContent: `+${more}`, 'aria-label': `Ещё ${more} ${postWord(more)} в это время`, title: 'Посты в это время' });
    badge.addEventListener('click', event => { event.stopPropagation(); list.hidden = !list.hidden; });
    wrap.append(card, badge, list);
    placeNode(wrap, GUTTER + entry.left, entry.top, entry.width);
    wrap.style.height = `${CARD_H.full}px`;
    return wrap;
}

/** Свободный слот расписания: пунктирная пилюля с «+» и временем; в узкой колонке — только «+». */
function slotPill(row, entry, mode) {
    const g = state.geo;
    const lane = laneOf(row.channel.id);
    const best = bestHoursInfo()?.best?.includes(new Date(row.at).getHours());   // слот в лучший час канала — подложка
    const width = entry.width;
    const tight = width < 60;    // две слота рядом в узкой колонке: «+» и время, без лишних знаков
    // Пилюля «+ 10:40» (CALENDAR_CARDS_FINISH): без слова «свободно» и без отдельного круга «+»
    const label = mode === 'pill' ? '+' : `+ ${hhmm(row.at)}`;
    const pill = el('button', { type: 'button', className: `cal-free${best ? ' is-best' : ''}${tight ? ' is-tight' : ''}` },
        el('span', { className: 'cal-free-time', textContent: label }));
    pill.style.setProperty('--c', lane.color);
    setAttrs(pill, { 'data-slot-at': row.at, 'aria-label': `Свободный слот ${whenLabel(row.at)}, ${lane.title}: добавить пост`, title: `Свободно · ${lane.title}` });
    const box = entryBox(entry, g);
    if (g.rows) {
        placeNode(pill, box.x, box.y, box.w);
        pill.style.height = `${CARD_H.pill}px`;   // слот в «строках» — пилюля 22 px у верха ряда, а не на всю высоту ряда
    } else placeNode(pill, box.x, box.y - 15, box.w);   // центр 30-пиксельной пилюли — на времени слота
    pill.addEventListener('click', () => { if (!justDropped()) openPicker(row, pill); });
    return pill;
}

// ---- Список ----

function listView() {
    const now = Date.now();
    const enabled = enabledChannels();
    const start = midnight(state.zoom.startMs);
    const days = Array.from({ length: state.listDays }, (_, i) => listDay(addDays(start, i), enabled, now));
    const more = el('button', { type: 'button', className: 'cal-more', textContent: 'Показать ещё 14 дней', onclick: () => { state.listDays += 14; renderAll(); } });
    return el('div', { className: 'cal-list' }, ...days, more);
}

function listDay(day, enabled, now) {
    const rows = dayAgenda(state.posts, enabled, day, now);
    const tomorrow = addDays(midnight(now), 1);
    const label = day === midnight(now) ? `Сегодня, ${dayName(day)}` : day === tomorrow ? `Завтра, ${dayName(day)}` : dayName(day);
    const section = el('section', { className: 'cal-lday' }, el('h4', { className: 'cal-lday-head', textContent: label.replace(/^(Сегодня|Завтра), /, '$1 · ') }),
        ...(rows.length ? rows.map(row => (row.kind === 'post' ? listRow(row.post) : listSlot(row))) : [el('p', { className: 'cal-muted cal-lempty', textContent: 'Пусто' })]));
    setAttrs(section, { 'data-drop-day': day });
    return section;
}

/** Строка списка: превью (или заглушка того же размера), время и значок, канал, текст в две строки. */
function listRow(post) {
    const lane = laneOf(post.channel);
    const media = mediaNode(post);
    const badge = statusBadge(post);
    const row = el('button', { type: 'button', className: `cal-row is-${post.status}${isPast(post) ? ' is-past' : ''}` },
        el('span', { className: `cal-row-media${media ? '' : ' is-empty'}`, 'aria-hidden': 'true' }, media || 'Аа'),
        el('span', { className: 'cal-row-body' },
            el('span', { className: 'cal-row-top' }, el('b', { textContent: hhmm(post.at * 1000) }), ...(badge ? [badge] : []),
                el('span', { className: 'cal-row-channel' }, el('span', { className: 'cal-dot' }), lane.title)),
            el('span', { className: 'cal-text', textContent: post.text || 'Без текста' })));
    row.style.setProperty('--c', lane.color);
    row.addEventListener('click', () => openPanel(post, row));
    return row;
}

/** Свободный слот в списке: одна строка «+ 11:30 · свободно · канал». */
function listSlot(row) {
    const lane = laneOf(row.channel.id);
    const button = el('button', { type: 'button', className: 'cal-lfree' }, el('span', { className: 'cal-plus', textContent: '+', 'aria-hidden': 'true' }),
        el('span', { textContent: `${hhmm(row.at)} · свободно · ${lane.title}` }));
    button.style.setProperty('--c', lane.color);
    setAttrs(button, { 'data-slot-at': row.at, 'aria-label': `Свободный слот ${whenLabel(row.at)}, ${lane.title}: добавить пост` });
    button.addEventListener('click', () => openPicker(row, button));
    return button;
}

// ---- Перенос: сохранение результата жеста ----

/** Перенос поста: оптимистично — карточка уже на новом месте; при ошибке возвращается с анимацией. */
async function commitDrop(post, at, copy) {
    const when = whenLabel(at);
    if (copy) {
        try {
            await api('telegram_duplicate', { body: { id: post.id, publish_at: unix(at) } });
            setStatus(`Копия создана на ${when}.`);
        } catch (error) {
            setStatus(error.message, true);
        }
        return loadCalendar();
    }
    const before = post.at;
    const card = state.cards.get(postKey(post));
    post.at = unix(at);
    animate(card);
    scheduleRender();
    try {
        await api('telegram_reschedule', { body: { id: post.id, publish_at: unix(at) } });
        setStatus(`Пост перенесён на ${when}.`);
    } catch (error) {
        post.at = before;
        animate(card);
        scheduleRender();
        setStatus(error.message, true);
    }
    await loadCalendar();
}

/** Карточка плавно едет на новое место 300 мс. */
function animate(card) {
    if (!card) return;
    card.classList.add('is-animate');
    setTimeout(() => card.classList.remove('is-animate'), 320);
}

/** Связка «сетка — жесты»: геометрия и действия, которые нужны calendar-drag.js. */
const host = {
    geo: () => state.geo,
    view: () => (state.view === 'grid' && state.geo ? { startMs: state.zoom.startMs, days: state.zoom.days } : null),
    isDragging,
    setDragging: value => { state.drag = value; },
    markMoved: () => { state.droppedAt = Date.now(); },
    timeToPx,
    placeAt: (node, dayMs, ms) => setPos(node, timeToPx(dayMs, ms)),
    /** День под точкой: номер дня и его колонка или строка (зависит от вида). */
    dayAt: (x, y) => {
        const day = document.elementFromPoint(x, y)?.closest('[data-drop-day]');
        return day ? { dayMs: Number(day.dataset.dropDay), area: day.querySelector('.cal-col') } : null;
    },
    /** Точка на оси часов внутри колонки или строки дня (px от начала оси). */
    axisPx: (area, point) => {
        const rect = area.getBoundingClientRect();
        return rows() ? point.clientX - rect.left : point.clientY - rect.top;
    },
    pxToTime: (dayMs, px) => dayMs + (state.geo.sh * 60 + px / state.geo.ppm) * MIN,
    /** Отклонение жеста в координаты вида: day — по дням, hour — по часам. */
    orient: (dx, dy) => (rows() ? { day: dy, hour: dx } : { day: dx, hour: dy }),
    /** Край сетки по каждой оси: −1 у начала, 1 у конца, 0 — не у края (за пределами сетки — 0). */
    edgeDirs: (x, y, rect, edgePx) => {
        if (!(x >= rect.left && x <= rect.right && y >= rect.top && y <= rect.bottom)) return { day: 0, hour: 0 };
        const edge = (v, lo, hi) => (v < lo + edgePx ? -1 : v > hi - edgePx ? 1 : 0);
        return rows()
            ? { day: edge(y, rect.top, rect.bottom), hour: edge(x, rect.left, rect.right) }
            : { day: edge(x, rect.left, rect.right), hour: edge(y, rect.top, rect.bottom) };
    },
    pointOf: (dayMs, ms) => {
        const area = body.querySelector(`[data-drop-day="${dayMs}"] .cal-col`);
        if (!area) return null;
        const rect = area.getBoundingClientRect();
        return rows() ? { x: rect.left + timeToPx(dayMs, ms), y: rect.top + rect.height / 2 } : { x: rect.left + rect.width / 2, y: rect.top + timeToPx(dayMs, ms) };
    },
    freeSlots: (channelId, dayMs) => {
        const channel = state.channels.find(c => c.id === channelId);
        return channel ? freeSlots(channel, state.posts, dayMs - DAY, dayMs + 2 * DAY, Date.now()).map(r => r.at) : [];
    },
    colorOf: channelId => laneOf(channelId).color,
    whenLabel,
    hhmm,
    bounds: () => body.getBoundingClientRect(),
    shiftDays,
    panTo: (startMs, dx) => {
        state.zoom.startMs = clampStart(startMs - dx / state.geo.pxPerDay * DAY);
        scheduleRender();
    },
    zoomAt: zoomAtClient,   // (clientX, clientY, factor, opts)
    hourShift: () => state.hourShift,
    panHours: (startShift, dh) => {   // тянем вниз (в строках — вправо) — видны более ранние часы
        if (!state.geo) return;
        state.hourShift = startShift - dh / (state.geo.ppm * 60);
        scheduleRender();
    },
    shiftHours: delta => { state.hourShift += delta; scheduleRender(); },
    poolAt: (x, y) => Boolean(document.elementFromPoint(x, y)?.closest('#cal-pool-col')),
    unschedule: post => unschedulePost(post),
    defaultChannel: () => enabledChannels()[0]?.id ?? null,
    commitPool: (item, at) => commitPoolItem(item, at),
    freezeHours: () => { if (!state.frozenHours && state.geo) state.frozenHours = { sh: state.geo.baseSh, eh: state.geo.baseEh }; },
    thawHours,
    endPan,
    stopAnim,
    flush: () => {
        cancelAnimationFrame(state.frame);
        renderAll();
    },
    commit: commitDrop,
    status: setStatus
};

// ---- Отрисовка тела: сетка или список ----

function renderBody() {
    body.classList.toggle('is-list', state.view === 'list');
    body.classList.toggle('is-rows', rows());
    if (isPhone()) {   // телефон: не сетка, а экран дня (calendar-phone.js)
        state.geo = null;
        return body.replaceChildren(phoneView(phoneCtx()));
    }
    if (state.view === 'list') {
        state.geo = null;
        return body.replaceChildren(listView());
    }
    const nodes = gridNodes(Date.now());
    if (nodes) body.replaceChildren(...nodes);
}

// ---- Телефон: данные и действия для calendar-phone.js ----

let phoneDay = null;   // выбранный день на телефоне; по умолчанию — сегодня

function phoneCtx() {
    return {
        day: phoneDay ?? midnight(Date.now()), now: Date.now(), posts: state.posts, channels: enabledChannels(),
        best: bestHoursInfo()?.best ?? [],
        pool: poolCol.querySelector('.cal-pool-count')?.textContent.match(/\d+/)?.[0] ?? 0,   // число из шапки Предложки
        select: day => { phoneDay = day; scheduleRender(); },
        openPost: (post, from) => openPanel(post, from),
        openSlot: (row, from) => openPicker(row, from),
        openFill: openFillPanel,
        openPool: async () => { try { showPanel('Предложка', await poolPanel(poolContext())); } catch (error) { setStatus(error.message, true); } },
        laneOf: channelId => laneOf(channelId),
        statusOf: post => STATUS_SHORT[post.status] || post.status,
        // тот же язык карточек, что в сетке (CALENDAR_CARDS_PROMPT.md)
        plate: statusPlate, avatar: channelAvatar, past: isPast, camera: CAMERA_ICON,
        thumbUrl: post => post.thumb_url
    };
}

// ---- Панель подробностей ----

function openPanel(post, from) {
    state.panelPost = post;
    showPanel(whenLabel(post.at * 1000), postChildren(post), from);   // заголовок — день и время, как в макете
}

/** Рамка выбора у карточки открытого поста; снимается при закрытии панели. */
let selectedCard = null;
function markSelected(node) {
    selectedCard?.classList.remove('is-selected');
    selectedCard = node?.classList?.contains('cal-card') ? node : null;
    selectedCard?.classList.add('is-selected');
}

/** Панель справа (на телефоне — снизу): заголовок, содержимое, ✕; закрытие возвращает фокус на from. */
function showPanel(heading, children, from = null) {
    state.panel = { from };
    markSelected(from);
    panel.classList.add('is-sheet');   // на телефоне панель появляется только по выбору
    panel.replaceChildren(el('div', { className: 'cal-panel-head' }, el('b', { textContent: heading }),
        el('button', { type: 'button', className: 'cal-pop-close', textContent: '✕', ariaLabel: 'Закрыть', onclick: closePanel })), ...children);
    panel.hidden = false;
    panel.focus();
}

// ---- «+» в свободном слоте: пост из «Предложки» в редактор с этим временем и каналом ----

/** Панель выбора: посты «Предложки» (лайкнутые на «Подборе»); выбор открывает редактор с временем слота. */
async function openPicker(row, from) {
    const lane = laneOf(row.channel.id);
    const heading = `Пост на ${dayName(row.at)}, ${hhmm(row.at)} · ${lane.title}`;
    showPanel(heading, [el('p', { className: 'cal-muted', textContent: 'Загружаю «Предложку»…' })], from);
    state.panelPost = null;
    try {
        const items = (await api('proposals')).items || [];
        const children = items.length
            ? [el('div', { className: 'cal-picks' }, ...items.map(item => pickerItem(item, row)))]
            : [el('p', { className: 'cal-muted', textContent: 'В Предложке пусто: лайкните посты на вкладке «Подбор»' }),
                el('button', { type: 'button', className: 'cal-act', textContent: 'Перейти на «Подбор»', onclick: () => { closePanel(); switchTab('content-tab'); } })];
        if (state.panel?.from === from) showPanel(heading, children, from);
    } catch (error) {
        setStatus(error.message, true);
        closePanel();
    }
}

function pickerItem(item, row) {
    const src = item.display_image || item.image || '';
    const thumb = src ? el('img', { className: 'cal-pick-thumb', src, alt: '', loading: 'lazy', decoding: 'async', referrerPolicy: 'no-referrer' }) : el('span', { className: 'cal-ph', innerHTML: CAMERA_ICON });
    if (src) thumb.addEventListener('error', () => thumb.replaceWith(el('span', { className: 'cal-ph', innerHTML: CAMERA_ICON })));
    const button = el('button', { type: 'button', className: 'cal-pick' }, thumb, el('span', { className: 'cal-pick-text', textContent: item.text || item.description || 'Без текста' }));
    button.addEventListener('click', () => pickItem(item, row));
    return button;
}

/** Редактор поста с временем слота и каналом; после постановки в очередь календарь перечитывается. */
async function pickItem(item, row) {
    closePanel();
    await postItemToTelegram(item, { scheduledTs: unix(row.at), channel: row.channel.id, onDone: () => loadCalendar() });
}

function closePanel() {
    const from = state.panel?.from;
    state.panel = null;
    state.panelPost = null;
    markSelected(null);
    if (state.ghosts) { state.ghosts = null; scheduleRender(); }
    panel.classList.remove('is-sheet');
    renderEmptyPanel();   // панель не прячется: без выбора показываются «Дыры недели»
    if (from?.isConnected) from.focus();
}

/** Панель без выбора: «Куда публиковать» (ресурсы с чекбоксами), ниже — «Дыры недели»: сводка и первые строки плана. */
async function renderEmptyPanel() {
    const enabled = enabledChannels();
    const sum = summary(state.posts, enabled, weekStart(Date.now()), Date.now());
    panel.replaceChildren(resourcesBlock(), ...(await holesChildren(poolContext(), { queued: sum.queued, free: sum.freeThisWeek })));
    panel.hidden = false;
}

/** Перерисовать панель: открытый пост — его подробности, иначе — ресурсы и дыры. */
function refreshPanel() {
    if (state.panelPost) return openPanel(state.panelPost, state.panel?.from ?? null);
    renderEmptyPanel();
}

const PLATFORM = { telegram: ['✈', 'Telegram'] };   // площадка → значок и название; новые площадки добавятся сюда

/** «Куда публиковать»: все ресурсы пользователя с чекбоксами. Отмеченные — цели постановки и видны в сетке.
 *  collapsed — одна строка «Публикуется в: A, B» с кнопкой «Изменить» (панель открытого поста). */
function resourcesBlock({ collapsed = false } = {}) {
    const names = enabledChannels().map(c => laneOf(c.id).title).join(', ') || '—';
    if (collapsed) {
        return el('div', { className: 'cal-res-line' }, el('span', { textContent: `Публикуется в: ${names}` }),
            el('button', { type: 'button', className: 'cal-act', textContent: 'Изменить', onclick: () => { state.resourcesOpen = true; refreshPanel(); } }));
    }
    const onCount = enabledChannels().length;
    const rows = state.channels.map(channel => {
        const lane = laneOf(channel.id);
        const box = el('input', { type: 'checkbox', id: `cal-res-${channel.id}`, checked: isOn(channel.id), disabled: isOn(channel.id) && onCount === 1 });
        box.addEventListener('change', () => toggleChannel(channel.id, box.checked));
        const dot = el('span', { className: 'cal-dot' });
        dot.style.background = lane.color;
        const [icon, platformName] = PLATFORM[channel.platform || 'telegram'] ?? ['•', channel.platform || ''];
        return el('li', { className: 'cal-res' }, box,
            el('label', { className: 'cal-res-name', htmlFor: box.id }, dot, el('span', { className: 'cal-res-title', textContent: lane.title }),
                el('small', { className: 'cal-res-user', textContent: channel.username ? `@${channel.username}` : '' })),
            el('span', { className: 'cal-res-platform', title: platformName, 'aria-label': platformName, textContent: icon }));
    });
    return el('section', { className: 'cal-resources', 'aria-label': 'Куда публиковать' },
        el('h3', { className: 'cal-resources-title', textContent: 'Куда публиковать' }),
        el('ul', { className: 'cal-res-list' }, ...rows),
        onCount === 1 ? el('p', { className: 'cal-muted cal-res-note', textContent: 'Хотя бы один ресурс должен остаться отмеченным.' }) : null,
        el('button', { type: 'button', className: 'cal-act', textContent: 'Подключить ещё', onclick: () => openAccountSection('social') }));
}
// ---- Заполнить дыры и Предложка (calendar-pool.js) ----

/** Снять запланированный пост с расписания: карточка сразу пропадает, запрос уходит через UNSCHEDULE_SECONDS, можно вернуть. */
const UNSCHEDULE_SECONDS = 10;
function unschedulePost(post) {
    const held = { post, timer: 0 };
    held.timer = setTimeout(() => finishUnschedule(post.id), UNSCHEDULE_SECONDS * 1000);
    state.pendingCancel.set(post.id, held);
    showPoolAfterUnschedule(post.image_key || '');
    setStatus('Пост снят с расписания.', false, UNSCHEDULE_SECONDS * 1000);
    const undo = el('button', { type: 'button', className: 'account-secondary', textContent: `Вернуть (${UNSCHEDULE_SECONDS} с)`, onclick: () => restoreUnschedule(post.id) });
    statusEl.append(' ', undo);
}
/** Перерисовать сетку и Предложку, потом подсветить пост в Предложке (он там и остаётся). */
async function showPoolAfterUnschedule(image) {
    await loadCalendar();   // ждёт и перерисовку Предложки, иначе подсветка может пропасть при замене элементов
    flashPoolItem(image);
}

async function finishUnschedule(id) {
    if (!state.pendingCancel.delete(id)) return;
    try {
        await api('telegram_cancel', { body: { id } });
        setStatus('Пост снят с расписания и остался в Предложке.');
    } catch (error) { setStatus(error.message, true); }
    await loadCalendar();
    renderPoolColumn(poolContext(), poolCol);
}

function restoreUnschedule(id) {
    const held = state.pendingCancel.get(id);
    if (!held) return;
    clearTimeout(held.timer);
    state.pendingCancel.delete(id);
    setStatus('Пост на прежнем времени.');
    loadCalendar();
}

/** Уход со страницы: отложенные снятия уходят сразу (keepalive — запрос дойдёт и после закрытия). */
function flushUnschedule() {
    for (const [id, held] of state.pendingCancel) {
        clearTimeout(held.timer);
        fetch(apiUrl('telegram_cancel'), { method: 'POST', keepalive: true, headers: { 'Content-Type': 'application/json', 'X-Posta': '1' }, body: JSON.stringify({ id }) }).catch(() => {});
    }
    state.pendingCancel.clear();
}

/** Пост в очереди или в «Отложенных» с той же картинкой, что у записи Предложки (для пометки «в расписании»). */
function queuedPostFor(item) {
    const key = item.image || '';
    if (!key) return null;
    return state.posts.find(p => p.image_key === key && ['queued', 'telegram'].includes(p.status)) || null;
}

const poolContext = () => ({
    reload: () => loadCalendar(), draft: openDraft, openFill: openFillPanel,
    scheduledFor: item => queuedPostFor(item),
    startDrag: (event, item, node) => startPoolDrag(event, item, node, host), justDropped,
    best: bestHoursInfo()?.best ?? [],
    pickSlot: isPhone() ? pickSlotForItem : null,   // телефон: пост Предложки — список свободных слотов дня
    showGhosts: rows => { state.ghosts = rows; scheduleRender(); },   // план «Заполнить дыры» — призрачные миниатюры в сетке
    openView: openPoolView,   // клик по записи Предложки — просмотрщик постов
    targets: () => enabledChannels().map(c => c.id)   // отмеченные ресурсы — цели «Заполнить дыры» и постановки
});

/** Пост из Предложки на время — во все отмеченные ресурсы одной группой. Фото — через очередь; видео и текст без картинки —
 *  в редактор с первым отмеченным ресурсом (редактор ставит в один канал). Возвращает id первого поста очереди или null. */
async function commitPoolItem(item, at) {
    if (at < Date.now() + MIN) { setStatus('Раньше, чем через минуту, пост не запланировать.', true); return null; }
    const targets = enabledChannels().map(c => c.id);
    if (!targets.length) { setStatus('Отметьте хотя бы один ресурс.', true); return null; }
    const image = item.image || item.display_image || '';
    if (item.format === 'video' || !image) {
        postItemToTelegram(item, { scheduledTs: Math.floor(at / 1000), channel: targets[0], onDone: () => loadCalendar() }).catch(error => setStatus(error.message, true));
        return null;
    }
    setStatus('Ставлю пост из Предложки в очередь…');
    try {
        const result = await api('calendar_fill_apply', { body: { items: [{ slot_at: Math.floor(at / 1000), channels: targets, image, text: cleanCaption(item.text) }] } });
        const posted = result.posted ?? [];
        const failed = (result.errors ?? []).map(e => `${laneOf(e.channel).title}: ${e.error}`);
        if (posted.length) {
            const part = targets.length > 1 ? ` — в ${posted.length} из ${targets.length} ресурсов` : '';
            setStatus(`Пост из Предложки запланирован на ${whenLabel(at)}${part}.${failed.length ? ` Не вышло: ${failed.join('; ')}` : ''}`, failed.length > 0, UNDO_SECONDS * 1000);
            undoable(posted, statusEl, poolContext());
        } else setStatus(failed.join('; ') || 'Пост не поставлен.', true);
        await loadCalendar();
        renderPoolColumn(poolContext(), poolCol);
        return posted[0] ?? null;
    } catch (error) { setStatus(error.message, true); return null; }
}

/** Просмотрщик постов для записей Предложки: листание ‹ › и ← → — по видимым записям колонки, в её порядке. */
function openPoolView(items, index) {
    const posts = items.map(item => fromRecord(item));
    openPostView(posts, index, { actions: post => poolViewActions(items[posts.indexOf(post)]), title: 'Предложка' });
}

/** Действия просмотрщика Предложки: поставить в ближайший слот, открыть в редакторе, убрать из Предложки. */
function poolViewActions(item) {
    return [
        { label: 'Поставить в календарь', icon: '📅', primary: true, run: () => placeNearest(item) },
        { label: 'Редактировать', icon: '✎', run: () => { closePostView(); return postItemToTelegram(item, { channel: enabledChannels()[0]?.id, onDone: () => loadCalendar() }); } },
        { label: 'Убрать из Предложки', icon: '✕', run: () => removeFromPool(item) },
    ];
}

/** «Поставить в календарь»: ближайший свободный слот отмеченных ресурсов, просмотрщик закрывается, пост подсвечивается в сетке. */
async function placeNearest(item) {
    const now = Date.now();
    const slot = enabledChannels().flatMap(c => freeSlots(c, state.posts, now, now + 14 * DAY, now)).sort((a, b) => a.at - b.at)[0];
    if (!slot) return setStatus('У отмеченных ресурсов нет свободных слотов на две недели.', true);
    closePostView();
    const id = await commitPoolItem(item, slot.at);
    const post = id ? state.posts.find(p => p.id === id) : null;
    const card = post ? body.querySelector(`[data-post-key="${id}"]`) : null;
    if (card) { openPanel(post, card); animate(card); }
}

/** «Убрать из Предложки» — тот же запрос, что 👎 на вкладке «Подбор» (запись уходит из лайкнутых). */
async function removeFromPool(item) {
    try {
        await api('vote', { body: { vote: 'dislike', image: item.item_key || item.image || '' } });
        closePostView();
        setStatus('Пост убран из Предложки.');
        await loadCalendar();
    } catch (error) { setStatus(error.message, true); }
}

async function openFillPanel() {
    try { showPanel('Заполнить дыры', await planPanel(poolContext())); } catch (error) { setStatus(error.message, true); }
}


/** Черновик из Предложки: канал — первый включённый; «В лучшее время» открывает редактор с ближайшим лучшим слотом. */
async function openDraft(item) {
    const channel = state.channels.find(c => !state.disabled.has(c.id)) ?? state.channels[0];
    try {
        showPanel('Черновик из Предложки', await draftChildren(item, channel, (picked, slot, channelId) =>
            postItemToTelegram(picked, { scheduledTs: slot, channel: channelId, onDone: () => loadCalendar() })), $('cal-pool'));
    } catch (error) { setStatus(error.message, true); }
}

/** Телефон: свободные слоты выбранного дня, лучшие сверху; выбор слота — редактор поста с этим временем. */
async function pickSlotForItem(item) {
    const now = Date.now();
    const day = phoneDay ?? midnight(now);
    const best = bestHoursInfo()?.best ?? [];
    const isBest = at => best.includes(new Date(at).getHours());
    const slots = enabledChannels().flatMap(c => freeSlots(c, state.posts, day, day + DAY, now)).sort((a, b) => (isBest(b.at) - isBest(a.at)) || a.at - b.at);
    const list = el('ul', { className: 'cal-holes' }, ...(slots.length ? slots.map(row => el('li', {},
        el('button', { type: 'button', className: 'cal-pick-slot', onclick: () => postItemToTelegram(item, { scheduledTs: unix(row.at), channel: row.channel.id, onDone: () => loadCalendar() }) },
            `${hhmm(row.at)} · ${row.channel.title}${isBest(row.at) ? ' · лучшее время' : ''}`)))
        : [el('li', { textContent: 'В этот день свободных слотов нет.' })]));
    showPanel('Свободные слоты', [el('p', { className: 'cal-pop-text', textContent: item.text || 'Без текста' }), list]);
}

/** Лучшие часы канала с охватом (первый включённый канал) — для подсветки шкалы. */
const bestHoursInfo = () => state.channels.find(c => !state.disabled.has(c.id))?.reach ?? null;

function postChildren(post) {
    const lane = laneOf(post.channel);
    const dot = el('span', { className: 'cal-tg-dot' });
    dot.style.background = lane.color;
    const message = [];
    if (post.thumb && post.id) message.push(el('img', { src: apiUrl('outbox_media', { id: post.id }), alt: '', loading: 'lazy' }));
    message.push(el('p', { textContent: post.full_text || post.text || 'Без текста' }));
    const forecast = post.forecast ? ` · прогноз ≈ ${fmtViews(post.forecast)} просмотров` : '';
    const tg = el('div', { className: 'cal-tg' },
        el('div', { className: 'cal-tg-head' }, dot, el('span', { textContent: post.channel_title || lane.title })),
        el('div', { className: 'cal-tg-msg' }, ...message, el('span', { className: 'cal-tg-foot', textContent: `${hhmm(post.at * 1000)}${forecast}` })),
        el('p', { className: 'cal-tg-note', textContent: 'Так пост будет выглядеть в канале' }));
    const best = bestHoursInfo()?.best ?? [];
    const rows = el('dl', { className: 'cal-rows' },
        el('div', {}, el('dt', { textContent: 'Статус' }), el('dd', {}, statusBadge(post) || '', ` ${STATUS[post.status] || ''}`)),
        el('div', {}, el('dt', { textContent: 'Лучшее время канала' }), el('dd', { textContent: best.length ? `${pad(best[0])}:00–${pad((best[0] + 2) % 24)}:00` : '—' })),
        el('div', {}, el('dt', { textContent: 'Соседний пост' }), el('dd', { textContent: neighbourText(post) })));
    const children = [resourcesOf(post), tg, rows, groupBlock(post)];
    if (post.error) children.push(el('p', { className: 'cal-pop-error', textContent: post.error }));
    if (post.duplicate) children.push(el('p', { className: 'cal-dup-warning', role: 'alert' }, `Похожий пост уже выходил в этом канале ${whenLabel(post.duplicate.at * 1000)}. `,
        ...(post.duplicate.url?.startsWith('https://') ? [el('a', { href: post.duplicate.url, target: '_blank', rel: 'noopener noreferrer', textContent: 'Показать' })] : [])));
    if (post.status === 'sent' && post.url?.startsWith('https://')) children.push(el('a', { href: post.url, target: '_blank', rel: 'noopener noreferrer', textContent: 'Открыть в Telegram ↗' }));
    const actions = postActions(post);
    if (actions.length) children.push(el('div', { className: 'cal-panel-grid' }, ...actions));
    children.push(el('button', { type: 'button', className: 'cal-act', textContent: 'Открыть пост', onclick: () => openQueuePost(post) }));
    return children;
}

/** Ресурсы поста: одна строка «Публикуется в: A, B» (все копии группы) с «Изменить» — развёртывание блока с чекбоксами. */
function resourcesOf(post) {
    if (state.resourcesOpen) return resourcesBlock();
    const names = groupCopies(post).map(c => laneOf(c.channel).title).join(', ');
    return el('div', { className: 'cal-res-line' }, el('span', { textContent: `Публикуется в: ${names}` }),
        el('button', { type: 'button', className: 'cal-act', textContent: 'Изменить', onclick: () => { state.resourcesOpen = true; refreshPanel(); } }));
}

/** Копии поста в группе (один пост в нескольких ресурсах); без группы — сам пост. */
const groupCopies = post => (post.group ? state.posts.filter(p => p.group === post.group) : [post]);

/** Группа: «В N ресурсах» и список копий; у каждой копии — «Снять только здесь». Одиночный пост — без блока. */
function groupBlock(post) {
    const copies = groupCopies(post);
    if (copies.length < 2) return null;
    const rows = copies.map(copy => {
        const lane = laneOf(copy.channel);
        const dot = el('span', { className: 'cal-dot' });
        dot.style.background = lane.color;
        const off = copy.id !== post.id && ['queued', 'failed', 'telegram'].includes(copy.status)
            ? el('button', { type: 'button', className: 'cal-act', textContent: 'Снять только здесь', onclick: () => unscheduleOne(copy) }) : null;
        return el('li', { className: 'cal-group-row' }, dot, el('span', { textContent: `${lane.title} · ${hhmm(copy.at * 1000)}` }), off);
    });
    return el('div', { className: 'cal-group' }, el('p', { className: 'cal-group-note', textContent: `В ${copies.length} ресурсах` }), el('ul', { className: 'cal-group-list' }, ...rows));
}

/** Снять одну копию группы; остальные остаются в расписании. */
async function unscheduleOne(copy) {
    closePanel();
    try {
        await api('telegram_cancel', { body: { id: copy.id, only: true } });
        setStatus('Копия снята с расписания, остальные ресурсы оставлены.');
    } catch (error) { setStatus(error.message, true); }
    await loadCalendar();
}

/** «Открыть пост» из панели: тот же просмотрщик, что у записей Предложки, через адаптер очереди. */
function openQueuePost(post) {
    const thumb = post.thumb && post.id ? apiUrl('outbox_media', { id: post.id }) : '';
    openPostView([fromQueuePost({ ...post, thumb }, { title: laneOf(post.channel).title })], 0, { title: laneOf(post.channel).title });
}

/** Просмотры: «3,1 тыс.» или число. */
function fmtViews(n) { return n >= 1000 ? `${(n / 1000).toFixed(1).replace('.', ',')} тыс.` : String(n); }

/** Соседний пост того же канала: сколько часов до него или после (ближайший). */
function neighbourText(post) {
    const others = state.posts.filter(p => p.channel === post.channel && p !== post && p.id !== post.id);
    if (!others.length) return '—';
    const near = others.reduce((best, p) => (Math.abs(p.at - post.at) < Math.abs(best.at - post.at) ? p : best));
    const hours = Math.round(Math.abs(near.at - post.at) / 3600);
    return near.at > post.at ? `через ${hours} ч` : `${hours} ч назад`;
}
function postActions(post) {
    const { id } = post;
    const toneOf = label => (['В лучшее время', 'Повторить'].includes(label) ? 'primary-button' : ['Отменить', 'Удалить'].includes(label) ? 'cal-act is-danger' : 'cal-act');
    const action = (label, run, confirmText = '') => el('button', { type: 'button', className: toneOf(label), textContent: label, onclick: () => runAction(run, confirmText) });
    if (post.status === 'queued') return [
        action('В лучшее время', () => api('telegram_reschedule', { body: { id, slot: true } }).then(() => 'Пост перенесён в ближайший слот.')),
        action('Перенести…', async () => {
            const at = await pickTime(post);
            if (!at) return '';
            await api('telegram_reschedule', { body: { id, publish_at: unix(at.getTime()) } });
            return `Пост перенесён на ${whenLabel(at.getTime())}.`;
        }),
        action('Копия в канал…', async () => {
            const at = await pickTime(post);
            if (!at) return '';
            await api('telegram_duplicate', { body: { id, publish_at: unix(at.getTime()) } });
            return `Копия создана на ${whenLabel(at.getTime())}.`;
        }),
        action('Отменить', () => api('telegram_cancel', { body: { id } }).then(() => 'Отложенный пост отменён.'), 'Отменить пост? Он не будет опубликован.')
    ];
    if (post.status === 'failed') return [
        action('Повторить', () => api('telegram_retry', { body: { id } }).then(() => 'Пост снова в очереди.')),
        action('Копия в канал…', async () => {
            const at = await pickTime(post);
            if (!at) return '';
            await api('telegram_duplicate', { body: { id, publish_at: unix(at.getTime()) } });
            return `Копия создана на ${whenLabel(at.getTime())}.`;
        }),
        action('Удалить', () => api('telegram_cancel', { body: { id } }).then(() => 'Пост удалён.'), 'Удалить пост из очереди?')
    ];
    if (post.status === 'telegram') return [
        action('Отменить', () => api('telegram_cancel', { body: { id } }).then(() => 'Пост отменён в Telegram.'), 'Отменить пост в «Отложенных» Telegram?')
    ];
    return [];
}

/** Выбор времени; null — окно закрыли. «Сразу» для переноса не годится. */
async function pickTime(post) {
    const { pickDateTime } = await import('./editor.js');   // editor.js трогает document при загрузке — грузим, когда нужен
    const chosen = await pickDateTime({ value: new Date(post.at * 1000) });
    if (chosen === null) throw new Error('Выберите конкретное время: «сразу» здесь не подходит.');
    return chosen ? new Date(chosen) : null;
}

async function runAction(run, confirmText) {
    if (confirmText && !confirm(confirmText)) return;
    closePanel();
    try {
        const message = await run();
        if (!message) return;
        await loadCalendar();
        setStatus(message);
    } catch (error) {
        setStatus(error.message, true);
    }
}

// ---- Подсказка «один раз, но явно» ----

function showHint() {
    const touch = matchMedia('(pointer: coarse)').matches;
    const lines = touch
        ? [['🤏', 'Щипок двумя пальцами — приблизить или отдалить, от трёх дней до месяца'],
            ['↕', 'Тяните часы над временной шкалой или Alt + колесо — листать часы'],
            ['👆', 'Удержите пост и тяните — он прилипает к каждым 10 минутам и к каждому часу; сверху видно, на сколько сдвигаете'],
            ['⧉', 'Копия — «Копия в канал…» в панели поста']]
        : [['🖱', 'Колесо мыши — приблизить или отдалить (от трёх дней до месяца)'],
            ['🖱', 'Колесо над часами слева или Alt + колесо — листать часы'],
            ['✋', 'Перетащите пост — он прилипает к каждым 10 минутам и к каждому часу; сверху видно, на сколько сдвигаете'],
            ['⧉', 'С Ctrl или Alt — копия поста'],
            ['↕', 'Тяните фон вверх-вниз — другие часы; пост из Предложки перетащите на день и время']];
    $('cal-hint-lines').replaceChildren(...lines.map(([icon, text]) => el('li', {}, el('span', { className: 'cal-hint-ico', textContent: icon, 'aria-hidden': 'true' }), el('span', { textContent: text }))));
    hintBox.hidden = false;
    $('cal-hint-ok').focus();
}

/** «Понятно»: отметка сохраняется в настройках аккаунта; если запрос не прошёл — в браузере. */
function closeHint() {
    hintBox.hidden = true;
    if (state.hintsSeen) return;
    api('hint_seen', { body: { name: HINT_NAME } })
        .then(data => { state.hintsSeen = data.hints_seen?.[HINT_NAME] ?? Date.now() / 1000; })
        .catch(() => writeStore(HINT_KEY, true));
}

// ---- Инициализация ----

export function initializeCalendarTab() {
    window.addEventListener('pagehide', flushUnschedule);
    document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'hidden') flushUnschedule(); });
    body = $('cal-body');
    panel = $('cal-panel');
    title = $('cal-title');
    zoomLabel = $('cal-zoom-label');
    statusEl = $('cal-status');
    main = $('cal-main');
    poolCol = $('cal-pool-col');
    empty = $('cal-empty');
    hintBox = $('cal-hint');
    fullBtn = $('cal-full');
    axisBtn = $('cal-axis');
    state.view = readStore(VIEW_KEY, 'grid') === 'list' ? 'list' : 'grid';
    const savedDays = readStore(ZOOM_KEY, null);
    state.zoom = { startMs: midnight(Date.now()), days: clampDays(typeof savedDays === 'number' ? savedDays : (PHONE.matches ? 1 : 7)) };
    state.fullDay = readStore(FULL_KEY, false) === true;
    state.axis = readStore(AXIS_KEY, 'days-cols') === 'days-rows' ? 'days-rows' : 'days-cols';
    axisBtn.addEventListener('click', () => {
        stopAnim();
        state.axis = state.axis === 'days-rows' ? 'days-cols' : 'days-rows';
        writeStore(AXIS_KEY, state.axis);
        state.hourShift = 0;   // окно часов другой оси — с начала
        renderAll();
    });
    $('cal-prev').addEventListener('click', () => step(-1));
    $('cal-next').addEventListener('click', () => step(1));
    $('cal-today').addEventListener('click', () => tweenTo({ days: state.zoom.days, startMs: midnight(Date.now()) }, 0.5));
    $('cal-out').addEventListener('click', () => zoomBy(1.25));
    $('cal-in').addEventListener('click', () => zoomBy(0.8));
    document.querySelectorAll('[data-cal-zoom]').forEach(b => b.addEventListener('click', () => presetZoom(Number(b.dataset.calZoom))));
    document.querySelectorAll('[data-cal-view]').forEach(b => b.addEventListener('click', () => setView(b.dataset.calView)));
    fullBtn.addEventListener('click', () => {
        state.fullDay = !state.fullDay;
        // «Круглые сутки»: окно 0–24 по центру; выключили — сдвиг сбрасывается
        if (state.fullDay) recenterHours(state.zoom.days, state.zoom.startMs); else state.hourShift = 0;
        writeStore(FULL_KEY, state.fullDay);
        scheduleRender();
    });
    $('cal-help').addEventListener('click', showHint);
    $('cal-hint-ok').addEventListener('click', closeHint);
    body.addEventListener('wheel', onWheel, { passive: false });
    bindGestures(body, host);
    main.addEventListener('keydown', event => {
        if (event.target.closest('input, textarea, select, .cal-panel')) return;
        if (event.key === 'ArrowLeft') step(-1);
        if (event.key === 'ArrowRight') step(1);
    });
    document.addEventListener('keydown', event => {
        if (event.key === 'Escape' && !isDragging() && !hintBox.hidden) closeHint();
        else if (event.key === 'Escape' && state.panel && !isDragging()) closePanel();
    });
    window.addEventListener('resize', scheduleRender);
    // Наблюдаем только то, что над сеткой: шапку приложения, плашку «вкладка неактивна» и полосу календаря.
    // Сама сетка и прокрутка страницы сюда не входят, поэтому петли «высота → страница → высота» нет.
    const above = new ResizeObserver(scheduleRender);
    for (const el of [document.querySelector('.app-top'), $('tab-inactive'), $('calendar-tab').querySelector('.cal-bar')]) if (el) above.observe(el);
    onTabOpen('calendar-tab', () => loadCalendar());
    setInterval(() => { if (isTabActive('calendar-tab') && !state.drag) loadCalendar(); }, 60000);
}
