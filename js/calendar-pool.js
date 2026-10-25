// calendar-pool.js — «Предложка» и «Заполнить дыры» в календаре (v4): план из лайкнутых постов на свободные слоты,
// постановка в очередь той же логикой, что обычная публикация, отмена на 30 секунд. Панели строит calendar.js.
import { api, cleanCaption, el } from './core.js';
import { shortText } from './card.js';

export const UNDO_SECONDS = 30;
const pad = n => String(n).padStart(2, '0');
const when = sec => { const d = new Date(sec * 1000); return `${pad(d.getDate())}.${pad(d.getMonth() + 1)} ${pad(d.getHours())}:${pad(d.getMinutes())}`; };
// Дата как в календаре: «Пт 23 окт, 11:30»
const PLAN_WEEK = ['пн', 'вт', 'ср', 'чт', 'пт', 'сб', 'вс'];
const PLAN_MONTHS = ['янв', 'фев', 'мар', 'апр', 'май', 'июн', 'июл', 'авг', 'сен', 'окт', 'ноя', 'дек'];
/** Короткая метка для плашки «в расписании»: «пт 14:00». */
const planShort = sec => { const d = new Date(sec * 1000); return `${PLAN_WEEK[(d.getDay() + 6) % 7]} ${pad(d.getHours())}:${pad(d.getMinutes())}`; };
const planWhen = sec => { const d = new Date(sec * 1000); const day = `${PLAN_WEEK[(d.getDay() + 6) % 7]} ${d.getDate()} ${PLAN_MONTHS[d.getMonth()]}`; return `${day[0].toUpperCase()}${day.slice(1)}, ${pad(d.getHours())}:${pad(d.getMinutes())}`; };

/** Панель плана «Заполнить дыры»: строки «время · канал · текст», у каждой — «Убрать»; внизу — «Поставить в очередь». */
export async function planPanel(ctx) {
    const rows = await fetchPlan(ctx.targets?.());
    const { items: pool = [] } = await api('proposals').catch(() => ({ items: [] }));
    const list = el('ul', { className: 'cal-plan' });
    const redraw = () => {
        ctx.showGhosts?.(rows);
        list.replaceChildren(...(rows.length ? rows.map((row, i) => planRow(row, {
            replace: () => { if (replaceRow(row, rows, pool)) redraw(); else note.textContent = 'В Предложке больше нет подходящих постов.'; },
            remove: () => { rows.splice(i, 1); redraw(); }
        }, ctx.best)) : [el('li', { className: 'account-muted', textContent: 'Свободных дыр в ближайшую неделю нет.' })]));
        apply.disabled = !rows.length;
        count.textContent = rows.length ? `План: ${rows.length} ${rows.length < 5 ? 'поста' : 'постов'}` : 'План пуст';
    };
    const count = el('p', { className: 'cal-plan-count' });
    const note = el('p', { className: 'cal-plan-note', role: 'status' });
    const apply = el('button', { type: 'button', className: 'primary-button', textContent: 'Поставить в очередь', onclick: async () => {
        apply.disabled = true;
        note.textContent = 'Ставлю в очередь…';
        try {
            const items = rows.map(r => ({ slot_at: r.slot_at, channel: r.channel, image: r.image, text: cleanCaption(r.text) }));
            const result = await api('calendar_fill_apply', { body: { items } });
            const errors = (result.errors || []).map(e => `${when(e.slot_at)}: ${e.error}`);
            note.textContent = `Поставлено ${result.posted.length} ${result.posted.length === 1 ? 'пост' : 'постов'}${errors.length ? '. Не вышло: ' + errors.join('; ') : ''}`;
            if (result.posted.length) undoable(result.posted, note, ctx);
            await ctx.reload();
        } catch (error) { note.textContent = error.message; apply.disabled = false; }
    } });
    redraw();
    return [count, list, el('div', { className: 'cal-pop-actions' }, apply), note];
}

/** Заменить пост строки следующим подходящим из Предложки, которого нет в плане. */
function replaceRow(row, rows, pool) {
    const used = new Set(rows.map(r => r.image));
    const next = pool.find(item => item.image && !used.has(item.image));
    if (!next) return false;
    Object.assign(row, { image: next.image, display: next.display_image || next.image, text: next.text, article: next.article });
    return true;
}

/** Строка плана: превью и время, канал, текст; справа — кнопки «Заменить» и «Убрать». «Лучшее время» — только у слотов канала. */
function planRow(row, { replace, remove }, best = []) {
    const buttons = el('span', { className: 'cal-plan-actions' },
        el('button', { type: 'button', className: 'account-secondary', textContent: 'Заменить', onclick: replace }),
        el('button', { type: 'button', className: 'account-secondary', textContent: 'Убрать', 'aria-label': `Убрать пост ${planWhen(row.slot_at)}`, onclick: remove }));
    return el('li', { className: 'cal-plan-row' },
        el('img', { className: 'cal-plan-thumb', src: row.display, alt: '', loading: 'lazy' }),
        el('span', { className: 'cal-plan-when', textContent: planWhen(row.slot_at) + (row.best && best.includes(new Date(row.slot_at * 1000).getHours()) ? ' · лучшее время' : '') }),
        el('span', { className: 'cal-plan-chan', textContent: row.channel_title || '' }),
        el('span', { className: 'cal-plan-text', textContent: shortText(row.article?.title || row.text || 'Без текста', 90) }),   // у статьи — заголовок, по границе слова
        buttons);
}

/** Отмена созданных постов на UNDO_SECONDS: пока окно открыто, кнопка доступна. */
export function undoable(ids, note, ctx) {
    const undo = el('button', { type: 'button', className: 'account-secondary', textContent: `Отменить (${UNDO_SECONDS} с)` });
    let left = UNDO_SECONDS;
    const timer = setInterval(() => {
        left -= 1;
        undo.textContent = `Отменить (${left} с)`;
        if (left <= 0) { clearInterval(timer); undo.remove(); }
    }, 1000);
    undo.addEventListener('click', async () => {
        clearInterval(timer);
        undo.disabled = true;
        for (const id of ids) await api('telegram_cancel', { body: { id } }).catch(() => null);
        note.textContent = 'Отменено.';
        undo.remove();
        await ctx.reload();
    });
    note.append(' ', undo);
}

/** Элемент Предложки: клик — просмотрщик постов (листание по видимым записям); перетаскивание мышью — на день и время в сетке (ctx.startDrag). */
function poolItemNode(item, ctx, visible = []) {
    const queued = ctx.scheduledFor?.(item) ?? null;   // пост с той же картинкой уже в очереди
    const node = el('button', { type: 'button', className: `cal-pool-item${queued ? ' is-scheduled' : ''}`, onclick: () => {
        if (ctx.justDropped?.()) return;   // после переноса клик не открывает черновик
        if (ctx.openView) ctx.openView(visible, Math.max(0, visible.indexOf(item)));
        else if (ctx.pickSlot) ctx.pickSlot(item);
        else ctx.draft(item);
    } },
        el('img', { src: item.display_image || item.image || '', alt: '', loading: 'lazy', draggable: false }),
        el('span', { className: 'cal-plan-text', textContent: (item.text || 'Без текста').slice(0, 90) }),
        el('span', { className: 'cal-pool-meta', textContent: `по теме ${item.relevance ?? '—'} · ${item.source || ''}` }),
        ...(queued ? [el('span', { className: 'cal-pool-sched', textContent: `в расписании · ${planShort(queued.at)}` })] : []));
    node.dataset.image = item.image || '';   // по картинке находим элемент после снятия с расписания
    if (ctx.startDrag && !ctx.pickSlot) node.addEventListener('pointerdown', event => ctx.startDrag(event, item, node));   // телефон — без переноса
    return node;
}

/** Панель «Предложка»: лайкнутые посты; клик — черновик с проверкой повтора и постановкой в лучшее время. */
export async function poolPanel(ctx) {
    const { items = [] } = await api('proposals');
    if (!items.length) return [el('p', { className: 'account-muted', textContent: 'В Предложке пусто: лайкните посты на вкладке «Подбор».' })];
    const visible = items.slice(0, 40);
    return [el('ul', { className: 'cal-pool' }, ...visible.map(item => el('li', { className: 'cal-pool-row' },
        el('button', { type: 'button', className: 'cal-pool-item', onclick: () => (ctx.openView ? ctx.openView(visible, visible.indexOf(item)) : ctx.pickSlot ? ctx.pickSlot(item) : ctx.draft(item)) },
            el('img', { className: 'cal-plan-thumb', src: item.display_image || item.image || '', alt: '', loading: 'lazy' }),
            el('span', { className: 'cal-plan-text', textContent: (item.text || 'Без текста').slice(0, 90) }),
            el('span', { className: 'cal-pool-meta', textContent: `по теме ${item.relevance ?? '—'}` })))))];
}

/** Черновик из Предложки: предупреждение о повторе для канала и «В лучшее время» (редактор с ближайшим лучшим слотом). */
export async function draftChildren(item, channel, openEditor) {
    const out = [el('img', { className: 'cal-pop-thumb', src: item.display_image || item.image || '', alt: '', loading: 'lazy' }),
        el('p', { className: 'cal-pop-text', textContent: item.text || 'Без текста' })];
    if (!channel) return out;
    const dup = await api('calendar_check', { body: { image: item.image || item.display_image || '', text: item.text || '', channel: channel.id } }).catch(() => null);
    if (dup?.duplicate) out.push(el('p', { className: 'cal-dup-warning', role: 'alert', textContent: `Похожий пост уже выходил в этом канале ${when(dup.duplicate.at)}.` + (dup.duplicate.url ? ' ' : '') }, dup.duplicate.url ? el('a', { href: dup.duplicate.url, target: '_blank', rel: 'noopener noreferrer', textContent: 'Показать' }) : ''));
    const now = Math.floor(Date.now() / 1000);
    const { plan = [] } = await api('calendar_fill_plan', { body: { channels: [channel.id], from: now, to: now + 7 * 86400, include_rest: false } }).catch(() => ({ plan: [] }));
    const slot = plan[0]?.slot_at;
    out.push(el('div', { className: 'cal-pop-actions' }, el('button', { type: 'button', className: 'primary-button', textContent: 'В лучшее время', disabled: !slot,
        onclick: () => openEditor(item, slot, channel.id) })));
    if (!slot) out.push(el('p', { className: 'account-muted', textContent: 'Свободного слота в лучшее время на неделе нет — выберите время в редакторе.' }));
    return out;
}

/** План на ближайшую неделю (лучшие часы каналов): строки для колонки, панели и «Заполнить». channels — только отмеченные ресурсы. */
export async function fetchPlan(channels = null) {
    const now = Math.floor(Date.now() / 1000);
    const body = { from: now, to: now + 7 * 86400, include_rest: false, ...(channels ? { channels } : {}) };
    const { plan = [] } = await api('calendar_fill_plan', { body }).catch(() => ({ plan: [] }));
    return [...plan];
}

const holeWord = n => (n % 10 === 1 && n % 100 !== 11 ? 'дыру' : n % 10 >= 2 && n % 10 <= 4 && (n % 100 < 12 || n % 100 > 14) ? 'дыры' : 'дыр');
const COLLAPSED_KEY = 'postaPoolCollapsed';
let poolFilter = 'all';
const POOL_FILTERS = [['all', 'Все', () => true], ['video', 'Видео', p => p.format === 'video'], ['content', 'Мемы', p => p.format === 'content'], ['text', 'Текст', p => p.format === 'text']];

/** Колонка «Предложка» слева: заголовок, сворачивание (localStorage; по умолчанию свёрнута под 1200 px), красная кнопка, фильтры, список. */
/** Подсветка элемента Предложки с этой картинкой (после снятия с расписания); true, если нашли. */
export function flashPoolItem(image) {
    const node = image ? [...document.querySelectorAll('#cal-pool-col .cal-pool-item')].find(n => n.dataset.image === image) : null;
    if (!node) return false;
    node.classList.remove('is-returned');
    void node.offsetWidth;   // перезапуск анимации
    node.classList.add('is-returned');
    setTimeout(() => node.classList.remove('is-returned'), 600);
    return true;
}

export async function renderPoolColumn(ctx, root) {
    const stored = (() => { try { return localStorage.getItem(COLLAPSED_KEY); } catch (e) { return null; } })();
    const collapsed = stored === null ? window.innerWidth < 1200 : stored === '1';
    const [{ items = [] }, plan] = await Promise.all([api('proposals').catch(() => ({ items: [] })), fetchPlan(ctx.targets?.())]);
    const toggle = el('button', { type: 'button', className: 'cal-pool-toggle', textContent: collapsed ? '›' : '‹', ariaLabel: collapsed ? 'Развернуть Предложку' : 'Свернуть Предложку',
        onclick: () => { try { localStorage.setItem(COLLAPSED_KEY, collapsed ? '0' : '1'); } catch (e) { /* без хранилища состояние на сессию */ } renderPoolColumn(ctx, root); } });
    const head = el('div', { className: 'cal-pool-head' }, el('h2', { textContent: 'Предложка' }), el('span', { className: 'cal-pool-count', textContent: `${items.length} постов` }), toggle);
    const fill = el('button', { type: 'button', className: 'cal-pool-fill', disabled: !plan.length, textContent: plan.length ? `Заполнить ${plan.length} ${holeWord(plan.length)} недели` : 'Дыр недели нет',
        onclick: () => ctx.openFill() });
    const filters = el('div', { className: 'cal-pool-filters', role: 'group', ariaLabel: 'Фильтр Предложки' }, ...POOL_FILTERS.map(([key, label]) =>
        el('button', { type: 'button', textContent: label, 'aria-pressed': String(poolFilter === key), onclick: () => { poolFilter = key; renderPoolColumn(ctx, root); } })));
    const pass = POOL_FILTERS.find(([key]) => key === poolFilter)[2];
    const visible = items.filter(pass).slice(0, 40);
    const list = el('ul', { className: 'cal-pool-list' }, ...visible.map(item => el('li', {}, poolItemNode(item, ctx, visible))));
    root.classList.toggle('is-collapsed', collapsed);
    root.replaceChildren(head, el('p', { className: 'cal-pool-note', textContent: 'Перетащите пост на нужный день и время или заполните все дыры одной кнопкой.' }), fill, filters, list);
}

/** Пустое состояние панели: «Дыры недели» — сводка и первые строки плана; «Заполнить» открывает план в этой панели. */
export async function holesChildren(ctx, summary) {
    const plan = await fetchPlan(ctx.targets?.());
    const rows = plan.slice(0, 5).map(row => el('li', {}, el('span', { textContent: `${new Date(row.slot_at * 1000).toLocaleString('ru-RU', { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })} · ${row.channel_title || ''}` }),
        el('span', { className: 'cal-plan-text', textContent: (row.text || '').slice(0, 40) })));
    return [el('h3', { className: 'cal-panel-empty-title', textContent: 'Дыры недели' }),
        el('p', { className: 'cal-tg-note', textContent: `${summary.queued} в очереди · ${summary.free} свободно` }),
        plan.length ? el('ul', { className: 'cal-holes' }, ...rows) : el('p', { className: 'cal-tg-note', textContent: 'Дыр в лучшее время на неделе нет.' }),
        el('div', { className: 'cal-panel-grid' }, el('button', { type: 'button', className: 'primary-button', textContent: 'Заполнить', disabled: !plan.length, onclick: () => ctx.openFill() }))];
}

