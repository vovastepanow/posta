// Проверка модели календаря (js/calendar-model.js) без браузера. Запуск: node tools/test_calendar.mjs
// Часовой пояс — Europe/Berlin: последнее воскресенье октября 2026 (25.10) — переход на зимнее время.
process.env.TZ = 'Europe/Berlin';
const { dayAgenda, summary, weekStart, addDays, slotTimes, snapTarget, shiftLabel, layoutDay, hourWindow, zoomAround, wheelPixels, wheelFactor, zoomLimitReached, snapDays, snapView, viewGapPx, easeView, wheelZoom, WHEEL_STEP_MAX, hourSpan, zoomHoursAround, centeredHourShift, zoomT, VZOOM_MAX, MIN_SPAN_H, snapGestureDays, panAxis, HOUR, DAY, MIN } = await import('../js/calendar-model.js');

let failures = 0;
const check = (name, ok) => {
    console.log(`${ok ? 'OK ' : 'FAIL'} ${name}`);
    if (!ok) failures++;
};
const local = (y, m, d, h = 0, min = 0) => new Date(y, m, d, h, min).getTime();
const sec = ms => Math.floor(ms / 1000);

// Неделя с переходом на зимнее время: 25 окт — 25 часов
const nowInWeek = local(2026, 9, 28, 12);
check('неделя: понедельник 00:00 местного времени', weekStart(nowInWeek) === local(2026, 9, 26));
check('неделя: воскресенье 25 окт ещё в прошлой неделе', weekStart(local(2026, 9, 25, 23, 30)) === local(2026, 9, 19));
check('неделя: день перехода на зимнее время — 25 часов', addDays(local(2026, 9, 25), 1) - local(2026, 9, 25) === 25 * HOUR);

const changeDay = local(2026, 9, 25);
const transitionPosts = [
    { id: 'a', channel: 'c1', at: sec(local(2026, 9, 25, 0, 30)), status: 'queued', text: 'ранний' },
    { id: 'b', channel: 'c1', at: sec(local(2026, 9, 25, 23, 30)), status: 'queued', text: 'поздний' },
];
const transitionRows = dayAgenda(transitionPosts, [{ id: 'c1', schedule: null }], changeDay, local(2026, 9, 1));
check('день перехода: оба поста внутри дня (00:30 и 23:30)', transitionRows.length === 2);

// Слоты: 10:00 и 14:00 по будням (пояс канала — Москва, UTC+3)
const moscow = { id: 'c1', title: 'Москва', schedule: { tz: 'Europe/Moscow', slots: { 1: ['10:00', '14:00'], 2: ['10:00'], 3: [], 4: [], 5: [], 6: [], 7: [] } } };
const monday = local(2026, 9, 26);    // понедельник 26 окт (в Берлине), в Москве тот же день
const slotsMon = slotTimes(moscow.schedule, monday - HOUR * 3, monday + DAY);
check('слоты: пояс канала — 10:00 MSK = 08:00 CET', slotsMon.length > 0 && slotsMon[0] === Date.UTC(2026, 9, 26, 7));
const agenda = dayAgenda([], [moscow], monday, local(2026, 9, 20));
check('повестка: свободные слоты появляются строками slot', agenda.length >= 2 && agenda.every(r => r.kind === 'slot' && r.channel.id === 'c1'));

// Слот, занятый постом (±1 минута): слот пропадает
const busy = [{ id: 'x', channel: 'c1', at: sec(agenda[0].at + 30000), status: 'queued', text: '' }];
const withPost = dayAgenda(busy, [moscow], monday, local(2026, 9, 20));
check('слот, занятый постом, не показывается', !withPost.some(r => r.kind === 'slot' && r.at === agenda[0].at));

// Прошедший слот не показывается
const later = dayAgenda([], [moscow], monday, agenda[1].at + 1000);
check('прошедший слот не показывается', later.every(r => r.at > agenda[1].at));

// Выключенный канал: ни постов, ни слотов
const off = dayAgenda([{ id: 'p', channel: 'c2', at: sec(monday + HOUR), status: 'queued', text: '' }], [moscow], monday, local(2026, 9, 20));
check('выключенный канал: ни постов, ни слотов', off.every(r => r.kind === 'slot' && r.channel.id === 'c1'));

// Канал без расписания: только посты
const noSchedule = dayAgenda([{ id: 'p', channel: 'c3', at: sec(monday + HOUR), status: 'queued', text: '' }], [{ id: 'c3', schedule: null }], monday, local(2026, 9, 20));
check('канал без расписания: только его посты', noSchedule.length === 1 && noSchedule[0].kind === 'post');

// Сводка: очередь (queued, sending, telegram) и свободные слоты недели по включённым каналам
const weekPosts = [
    { id: '1', channel: 'c1', at: sec(monday + HOUR), status: 'queued', text: '' },
    { id: '2', channel: 'c1', at: sec(monday + 2 * HOUR), status: 'telegram', text: '' },
    { id: '3', channel: 'c1', at: sec(monday + 3 * HOUR), status: 'sending', text: '' },
    { id: '4', channel: 'c1', at: sec(monday + 4 * HOUR), status: 'sent', text: '' },
    { id: '5', channel: 'c2', at: sec(monday + 5 * HOUR), status: 'queued', text: '' },
];
const sum = summary(weekPosts, [moscow], weekStart(monday), local(2026, 9, 20));
check('сводка: в очереди — queued + telegram + sending включённых каналов', sum.queued === 3);
check('сводка: свободные слоты недели считаются', sum.freeThisWeek > 0);
check('сводка: выключенный канал не учитывается', summary(weekPosts, [], weekStart(monday), 0).queued === 0);

// ---- Календарь v3: прилипание, сдвиг, раскладка, окно часов, масштаб ----
const day0 = local(2026, 9, 20);                 // вторник 20 окт
const PPM = 1;                                   // 1 px на минуту: 10 мин = 10 px, час = 60 px
const at = (h, m = 0) => day0 + h * HOUR + m * MIN;

// Прилипание: слот важнее часа, час важнее 10 минут, пороги в пикселях
const slotAt = at(10, 0);
check('прилипание: слот в пределах 16 px — кнопка «слот»', (() => { const r = snapTarget(at(10, 12), { slots: [slotAt], pxPerMinute: PPM, dayStartMs: day0 }); return r.kind === 'slot' && r.at === slotAt; })());
check('прилипание: слот дальше 16 px — не прилипает к нему', snapTarget(at(10, 20), { slots: [slotAt], pxPerMinute: PPM, dayStartMs: day0 }).kind !== 'slot');
check('прилипание: слот важнее часа (слот 10:00, час тот же — выигрывает слот)', snapTarget(at(10, 3), { slots: [slotAt], pxPerMinute: PPM, dayStartMs: day0 }).kind === 'slot');
check('прилипание: час в пределах 10 px', (() => { const r = snapTarget(at(15, 7), { slots: [], pxPerMinute: PPM, dayStartMs: day0 }); return r.kind === 'hour' && r.at === at(15); })());
check('прилипание: 10 минут в пределах 4 px', (() => { const r = snapTarget(at(12, 33), { slots: [], pxPerMinute: PPM, dayStartMs: day0 }); return r.kind === '10min' && r.at === at(12, 30); })());
check('прилипание: 10 минут дальше 4 px — свободное время (минута)', (() => { const r = snapTarget(at(12, 25), { slots: [], pxPerMinute: PPM, dayStartMs: day0 }); return r.kind === 'round' && r.at === at(12, 25); })());
check('прилипание: узкая колонка — округление до 10 минут', (() => { const r = snapTarget(at(12, 25), { slots: [], pxPerMinute: PPM, dayStartMs: day0, roundTo: 10 }); return r.at === at(12, 30); })());
check('прилипание: слитые 10-минутные отметки (tenMin: false) — не прилипают, округление до 10 минут', (() => { const r = snapTarget(at(12, 33), { slots: [], pxPerMinute: PPM, dayStartMs: day0, roundTo: 10, tenMin: false }); return r.kind === 'round' && r.at === at(12, 30); })());
check('прилипание: при 0.1 px/мин 10-минутные отметки не прилипают', snapTarget(at(12, 33), { slots: [], pxPerMinute: 0.1, dayStartMs: day0 }).kind !== '10min');

// Подписи сдвига
check('сдвиг: +10 мин', shiftLabel(at(10), at(10, 10)) === '+10 мин');
check('сдвиг: +2 ч 10 мин', shiftLabel(at(10), at(12, 10)) === '+2 ч 10 мин');
check('сдвиг: −1 день, +30 мин', shiftLabel(at(10), day0 - DAY + at(10, 30) - day0) === '−1 день, +30 мин');
check('сдвиг: 0 — без изменений', shiftLabel(at(10), at(10)) === 'без изменений');
check('сдвиг: 23:50 → 00:10 следующего дня — +20 мин, без «−23 ч»', shiftLabel(at(23, 50), at(24, 10)) === '+20 мин');
check('сдвиг: −40 мин', shiftLabel(at(10), at(9, 20)) === '−40 мин');
check('сдвиг: +5 дней (склонение)', shiftLabel(at(10), at(10) + 5 * DAY) === '+5 дней');

// Раскладка дня: 2, 3 и 9 пересекающихся постов при разной ширине колонки
const cardH = 64;
const mk = (h, m, id) => ({ id, at: at(h, m), text: id });
const originMs = at(0);
const lay2 = layoutDay([mk(10, 0, 'a'), mk(10, 5, 'b')], { originMs, pxPerMinute: PPM, colWidth: 300, cardHeight: cardH });
check('раскладка: 2 пересекающихся — делят колонку пополам', lay2.length === 2 && lay2.every(i => i.type === 'card' && i.width === 150) && lay2[0].left !== lay2[1].left);
const lay3 = layoutDay([mk(10, 0, 'a'), mk(10, 5, 'b'), mk(10, 10, 'c')], { originMs, pxPerMinute: PPM, colWidth: 300, cardHeight: cardH });
check('раскладка: 3 пересекающихся в широкой колонке — три карточки рядом (стопка только от четырёх)', lay3.length === 3 && lay3.every(i => i.type === 'card' && i.width === 100));
const lay2narrow = layoutDay([mk(10, 0, 'a'), mk(10, 5, 'b')], { originMs, pxPerMinute: PPM, colWidth: 110, cardHeight: cardH, minWidth: 60 });
check('раскладка: 2 поста в одно время в узкой колонке — карточка целиком и бейдж «+1»', lay2narrow.length === 1 && lay2narrow[0].type === 'cover' && lay2narrow[0].more === 1 && lay2narrow[0].width === 110);
const lay9wide = layoutDay(Array.from({ length: 9 }, (_, i) => mk(10, i * 13, 'w' + i)), { originMs, pxPerMinute: PPM, colWidth: 1000, cardHeight: cardH });
check('раскладка: 9 постов за 2 часа — одна стопка на все девять (макет v4)', lay9wide.length === 1 && lay9wide[0].type === 'stack' && lay9wide[0].items.length === 9);
const lay9narrow = layoutDay(Array.from({ length: 9 }, (_, i) => mk(10, i * 13, 'n' + i)), { originMs, pxPerMinute: PPM, colWidth: 250, cardHeight: cardH });
check('раскладка: 9 постов при узкой колонке — одна стопка с периодом', lay9narrow.length === 1 && lay9narrow[0].type === 'stack' && lay9narrow[0].items.length === 9 && lay9narrow[0].from === at(10) && lay9narrow[0].to === at(10, 104));
const laySingle = layoutDay([mk(10, 0, 'solo')], { originMs, pxPerMinute: PPM, colWidth: 27, cardHeight: cardH });
check('раскладка: одиночный пост в узкой колонке — карточка, не стопка', laySingle.length === 1 && laySingle[0].type === 'card' && laySingle[0].width === 27);
const lay2sep = layoutDay([mk(10, 0, 'x'), mk(12, 0, 'y')], { originMs, pxPerMinute: PPM, colWidth: 300, cardHeight: cardH });
check('раскладка: непересекающиеся — каждый на всю колонку', lay2sep.every(i => i.width === 300));

// Окно часов
check('окно часов: весь день — 0–24', JSON.stringify(hourWindow([at(10)], [], day0, day0 + DAY, true)) === '[0,24]');
check('окно часов: пусто — 8–20', JSON.stringify(hourWindow([], [], day0, day0 + DAY, false)) === '[8,20]');
check('окно часов: 10:00–11:00 — не меньше 8 часов, ±1 час', (() => { const [s, e] = hourWindow([at(10), at(11)], [], day0, day0 + DAY, false); return e - s >= 8 && s <= 9 && e >= 12; })());
check('окно часов: ночной пост 02:30 — окно начинается с 1', hourWindow([at(2, 30), at(18)], [], day0, day0 + DAY, false)[0] === 1);
check('окно часов: пост 23:30 — окно до 24', hourWindow([at(23, 30)], [], day0, day0 + DAY, false)[1] === 24);
check('окно часов: посты вне видимых дней не учитываются', JSON.stringify(hourWindow([at(3) + 5 * DAY], [], day0, day0 + DAY, false)) === '[8,20]');

// Масштаб у курсора
const view = { startMs: day0, days: 7, width: 1000 };
const zc = zoomAround(view, 300, 0.5);
const pointBefore = view.startMs + 0.3 * view.days * DAY;
check('масштаб: день под курсором остаётся под курсором', Math.abs((zc.startMs + 0.3 * zc.days * DAY) - pointBefore) < 1);
check('масштаб: приближение уменьшает дни вдвое', zc.days === 3.5);
check('масштаб: нижняя граница — 1 день', zoomAround({ startMs: day0, days: 1.2, width: 900 }, 100, 0.1).days === 1);
check('масштаб: верхняя граница — 31 день', zoomAround({ startMs: day0, days: 25, width: 900 }, 100, 3).days === 31);
check('масштаб: курсор у левого края — левый край не уходит', zoomAround(view, 0, 2).startMs === view.startMs);
check('масштаб: курсор у правого края — правый край на месте', (() => { const z = zoomAround(view, 1000, 2); return Math.abs((z.startMs + z.days * DAY) - (view.startMs + view.days * DAY)) < 1; })());
// Слоты в общей раскладке дня: пост 14:00 и слот 15:00 при недельном масштабе не перекрываются (FIXES_ROUND2, часть C.1)
{
    const origin = local(2026, 9, 28, 0);
    const pxPerMinute = 0.5;   // недельный масштаб: час — 30 px, карточка 44 px
    const posts = [{ kind: 'post', at: local(2026, 9, 28, 14), post: { id: 'p1' } }, { kind: 'slot', at: local(2026, 9, 28, 15), channel: { id: 'c1' } }];
    const laid = layoutDay(posts, { originMs: origin, pxPerMinute, colWidth: 200, cardHeight: 44 });
    const boxes = laid.flatMap(e => (e.type === 'stack' ? [{ top: e.top, bottom: e.top + 20, left: 0, right: 200 }] : [{ top: e.top, bottom: e.top + 44, left: e.left, right: e.left + e.width }]));
    const overlap = boxes.some((a, i) => boxes.some((b, j) => j > i && a.top < b.bottom && b.top < a.bottom && a.left < b.right && b.left < a.right));
    check('слоты: пост 14:00 и слот 15:00 при недельном масштабе не перекрываются', laid.length >= 1 && !overlap);
}
check('масштаб: минимум 3 дня на телефоне', zoomAround({ startMs: day0, days: 2, width: 360 }, 100, 1, { min: 1, max: 3 }).days === 2);

// ---- Колесо и зум: нормализация, пределы, прилипание, доводка ----
check('колесо: строки (deltaMode 1) ×16 px', wheelPixels(3, 1) === 48);
check('колесо: страницы (deltaMode 2) — высота сетки', wheelPixels(1, 2, 90) === 90);
check('колесо: одна порция ограничена WHEEL_STEP_MAX по модулю', wheelPixels(1000, 0) === WHEEL_STEP_MAX && wheelPixels(-1000, 0) === -WHEEL_STEP_MAX);
const click = wheelFactor(wheelPixels(100, 0));
check('щелчок колеса Chrome (100 px) — примерно на 12–15 %', click > 1.12 && click < 1.15);
check('колесо и щипок — одна чувствительность: коэффициент один', Math.abs(wheelFactor(50) * wheelFactor(50) - wheelFactor(100)) < 1e-12);
check('предел: колесо наружу на 31 дне не перехватывается', zoomLimitReached(31, 100) === true && zoomLimitReached(31, -100) === false);
check('предел: колесо внутрь на трёх днях не перехватывается (компьютер)', zoomLimitReached(3, -100, { min: 3, max: 31 }) === true && zoomLimitReached(3, 100, { min: 3, max: 31 }) === false);
check('прилипание: 2,4 дня → 3 на компьютере', snapDays(2.4, { min: 3, max: 31 }) === 3);
check('прилипание: 6,4 дня → 6', snapDays(6.4) === 6);
check('прилипание: 6,8 дня → 7', snapDays(6.8) === 7);
check('прилипание: 6,6–7,4 → 7', [6.6, 7, 7.4].every(d => snapDays(d) === 7));
check('прилипание: 30,2 → 31 (липкость месяца)', snapDays(30.2) === 31);
check('прилипание: 0,7 → 1', snapDays(0.7) === 1);
check('прилипание на телефоне: не больше трёх дней', snapDays(2.6, { min: 1, max: 3 }) === 3 && snapDays(9, { min: 1, max: 3 }) === 3);
const dayKey = ms => { const d = new Date(ms); return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`; };
const isMidnight = ms => { const d = new Date(ms); return d.getHours() === 0 && d.getMinutes() === 0; };
{
    const view = { startMs: local(2026, 9, 23, 9), days: 6.37 };
    const s = snapView(view, 300, 1000, { min: 1, max: 31 });
    const cursorBefore = view.startMs + 0.3 * view.days * DAY, cursorAfter = s.startMs + 0.3 * s.days * DAY;
    check('прилипание: начало — полночь, дней 6', isMidnight(s.startMs) && s.days === 6);
    check('прилипание: день под курсором тот же', dayKey(cursorBefore) === dayKey(cursorAfter));
}
{
    const view = { startMs: local(2026, 9, 24, 15), days: 3.4 };   // курсор 25 окт — день перехода на зимнее время
    const cursorX = 620, width = 1000;
    const s = snapView(view, cursorX, width, { min: 1, max: 31 });
    const cursorBefore = view.startMs + (cursorX / width) * view.days * DAY, cursorAfter = s.startMs + (cursorX / width) * s.days * DAY;
    check('через 25.10: начало — полночь, а не 23:00 или 01:00', isMidnight(s.startMs) && new Date(s.startMs).getHours() === 0);
    check('через 25.10: день под курсором тот же', dayKey(cursorBefore) === dayKey(cursorAfter));
}
check('доводка: одинаковые виды — нулевой зазор', viewGapPx({ startMs: day0, days: 7 }, { startMs: day0, days: 7 }, 1000) === 0);
{
    const target = { days: 10, startMs: local(2026, 9, 26) };
    let v = { days: 7, startMs: local(2026, 9, 23) };
    const first = easeView(v, target, 16);
    check('доводка: первый кадр не прыгает целиком к цели', first.days > 7 && first.days < 8);
    for (let i = 0; i < 80; i++) v = easeView(v, target, 16);
    check('доводка: до цели меньше 0,5 px за 80 кадров по 16 мс', viewGapPx(v, target, 1000) < 0.5);
}
{
    // Накопление: щелчки строятся от цели, поэтому кадры доводки между ними итог не меняют
    const limits = { min: 1, max: 31 };
    const t0 = { days: 7, startMs: local(2026, 9, 23) };
    let a = t0;
    for (let i = 0; i < 3; i++) a = wheelZoom(a, 300, 1000, 100, limits);
    let b = t0, frame = { ...t0 };
    for (let i = 0; i < 3; i++) {
        b = wheelZoom(b, 300, 1000, 100, limits);
        for (let f = 0; f < 5; f++) frame = easeView(frame, b, 16);
    }
    check('накопление: три щелчка подряд — тот же итог, что с доводкой между ними', Math.abs(a.days - b.days) < 1e-9 && a.startMs === b.startMs);
    check('накопление: три щелчка по 100 px — масштаб 7 · e^0,375', Math.abs(a.days - 7 * Math.exp(0.375)) < 1e-9);
}


// ---- Вертикальный масштаб часов (CALENDAR_ZOOM_V2) ----
check('часы: месяц — видно всё окно (12 ч)', hourSpan(12, 31) === 12);
check('часы: три дня — 12 / VZOOM_MAX', Math.abs(hourSpan(12, 3) - 12 / VZOOM_MAX) < 1e-9);
check('часы: при уменьшении дней окно только короче (монотонно)', [31, 20, 12, 7, 5, 3].every((d, i, a) => i === 0 || hourSpan(12, d) <= hourSpan(12, a[i - 1]) + 1e-9));
check('часы: круглые сутки — месяц 24 ч', hourSpan(24, 31) === 24);
check('часы: не короче MIN_SPAN_H', hourSpan(6, 3) >= Math.min(MIN_SPAN_H, 6) - 1e-9);
check('часы: равномерность — отношение соседних шагов одинаково (4, 8, 16 дней)', [4, 8, 16].every(d => Math.abs(hourSpan(12, d) / hourSpan(12, d * 1.13) - hourSpan(12, 4) / hourSpan(12, 4 * 1.13)) < 0.01));
check('шкала: t = 0 на месяце, 1 на трёх днях', zoomT(31) === 0 && zoomT(3) === 1);
check('часы под курсором: до и после зума совпадают', (() => {
    const before = [8, 20]; const frac = 0.3; const hour = before[0] + frac * 12;
    const after = zoomHoursAround(before, frac, 5);
    return Math.abs(after[0] + frac * 5 - hour) < 1 / 60;
})());
check('часы под курсором: у края окно зажато в [0, 24] и сохраняет длину', (() => {
    const [a, b] = zoomHoursAround([0, 12], 0, 5);
    const [c, d] = zoomHoursAround([13, 24], 1, 5);
    return a === 0 && b === 5 && d === 24 && c === 19;
})());
check('часы: пресет центрируется на базовом окне', centeredHourShift([8, 20], 5) === 3.5);

check('прилипание после жеста: 4 дня и щелчок внутрь → 3 (не возврат на 4)', snapGestureDays(3.54, 4, { min: 3, max: 31 }) === 3);
check('прилипание после жеста: 4 дня и щелчок наружу → 5', snapGestureDays(4.52, 4, { min: 3, max: 31 }) === 5);
check('прилипание после жеста: без движения — прежнее целое', snapGestureDays(7, 7, { min: 3, max: 31 }) === 7);
check('прилипание после жеста: не ниже предела', snapGestureDays(2.9, 3, { min: 3, max: 31 }) === 3);

// Фон: мёртвая зона по каждой оси
check('фон: дрожь в мёртвой зоне не двигает вид', panAxis(5, 12) === 0 && panAxis(12, 12) === 0 && panAxis(-12, 12) === 0);
check('фон: за мёртвой зоной — от её края, без скачка', panAxis(-30, 12) === -18 && panAxis(30, 12) === 18);
check('фон: монотонно и симметрично по знаку', [-40, -12, -5, 0, 5, 12, 13, 40].map(v => panAxis(v, 12)).every((v, i, a) => i === 0 || v >= a[i - 1]) && panAxis(-20, 12) === -panAxis(20, 12));

// Поздние карточки у низа колонки: поднимаются, засечка на настоящем времени, стопка поднятых не наезжает
const colH = 613;
const tailLay = layoutDay([mk(17, 55, 'late'), mk(17, 50, 'late2')], { originMs: at(8), pxPerMinute: 1, colWidth: 300, cardHeight: 58, colHeight: colH });
const late = tailLay.find(e => e.item.id === 'late');
const late2 = tailLay.find(e => e.item.id === 'late2');
check('низ колонки: карточка 17:55 поднята до низа, настоящее время — в timeTop', late.top + 58 <= colH && late.timeTop === 595 && late.top < late.timeTop);
check('низ колонки: поднятая карточка не наезжает на соседнюю (17:50 выше 17:55)', late2 !== undefined && late2.top + 58 <= late.top);
const inside = layoutDay([mk(9, 0, 'mid')], { originMs: at(8), pxPerMinute: 1, colWidth: 300, cardHeight: 58, colHeight: colH });
check('низ колонки: карточка, которая помещается, не сдвигается', inside[0].top === 60 && inside[0].timeTop === 60);
const beyond = layoutDay([mk(20, 0, 'after')], { originMs: at(8), pxPerMinute: 1, colWidth: 300, cardHeight: 58, colHeight: colH });
check('низ колонки: карточка ниже видимого окна не трогается', beyond[0].top === 720 && beyond[0].timeTop === 720);
const rawLay = layoutDay([mk(17, 55, 'raw')], { originMs: at(8), pxPerMinute: 1, colWidth: 300, cardHeight: 58 });
check('низ колонки: без colHeight раскладка прежняя', rawLay[0].top === 595 && rawLay[0].timeTop === 595);

console.log(failures ? `\nПровалено: ${failures}` : '\nВсе проверки прошли');
process.exit(failures ? 1 : 0);
