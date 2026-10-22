// editor.js — окно выбора даты и времени и модальный редактор поста (порядок медиа, текст, отправка через onSubmit).
import { el, pluralRu } from './core.js';

// ---- Дата и время ----

const MONTHS = ['Январь', 'Февраль', 'Март', 'Апрель', 'Май', 'Июнь', 'Июль', 'Август', 'Сентябрь', 'Октябрь', 'Ноябрь', 'Декабрь'];
const MIN_LEAD_MS = 60000;   // как на сервере: минимум минута вперёд
const pad = n => String(n).padStart(2, '0');
const sameDay = (a, b) => a.toDateString() === b.toDateString();
const button = (className, textContent, onclick, extra = {}) => el('button', { type: 'button', className, textContent, onclick, ...extra });

/** «Сегодня, 20:30», «Завтра, 10:00», «25 октября, 10:00». */
export function formatWhen(date) {
    const time = `${pad(date.getHours())}:${pad(date.getMinutes())}`;
    const today = new Date();
    if (sameDay(date, today)) return `Сегодня, ${time}`;
    if (sameDay(date, new Date(today.getFullYear(), today.getMonth(), today.getDate() + 1))) return `Завтра, ${time}`;
    return `${date.getDate()} ${MONTHS[date.getMonth()].toLowerCase().replace(/[ьй]$/, 'я').replace(/т$/, 'та')}, ${time}`;
}

/** → Promise: Date — выбрано время, null — «Сразу», undefined — закрыто без выбора. */
export function pickDateTime({ value = null } = {}) {
    return new Promise(resolve => {
        const now = new Date();
        const roundHour = () => new Date(Math.ceil((now.getTime() + 3600000) / 300000) * 300000);
        const selected = new Date(value && value > now ? value : roundHour());
        const view = new Date(selected.getFullYear(), selected.getMonth(), 1);
        const focus = document.activeElement;
        const [title, grid, summary] = [el('span', { className: 'dtp-title' }), el('div', { className: 'dtp-grid' }), el('div', { className: 'dtp-summary' })];
        const select = (label, n) => el('select', { className: 'dtp-select', ariaLabel: label }, ...Array.from({ length: n }, (_, i) => el('option', { value: i, textContent: pad(i) })));
        const [hours, minutes] = [select('Часы', 24), select('Минуты', 60)];
        const ok = () => selected.getTime() >= Date.now() + MIN_LEAD_MS;
        const close = result => { document.removeEventListener('keydown', onKey, true); root.remove(); focus?.focus?.(); resolve(result); };
        const onKey = e => { if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); close(undefined); } };
        const done = button('dtp-done', 'Готово', () => ok() && close(new Date(selected)));
        const refresh = () => {
            title.textContent = `${MONTHS[view.getMonth()]} ${view.getFullYear()}`;
            const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
            const days = new Date(view.getFullYear(), view.getMonth() + 1, 0).getDate();
            grid.replaceChildren(...['Пн', 'Вт', 'Ср', 'Чт', 'Пт', 'Сб', 'Вс'].map(d => el('span', { className: 'dtp-weekday', textContent: d })),
                ...Array.from({ length: (view.getDay() + 6) % 7 }, () => el('span')),
                ...Array.from({ length: days }, (_, i) => {
                    const date = new Date(view.getFullYear(), view.getMonth(), i + 1);
                    return button(`dtp-day${sameDay(date, today) ? ' is-today' : ''}${sameDay(date, selected) ? ' is-selected' : ''}`, String(i + 1),
                        () => { selected.setFullYear(date.getFullYear(), date.getMonth(), date.getDate()); refresh(); }, { disabled: date < today });
                }));
            hours.value = String(selected.getHours());
            minutes.value = String(selected.getMinutes());
            summary.textContent = ok() ? formatWhen(selected) : 'Выберите время хотя бы на минуту вперёд';
            summary.classList.toggle('is-error', !ok());
            done.disabled = !ok();
        };
        const month = delta => { view.setMonth(view.getMonth() + delta); refresh(); };
        const at = (days, hour) => new Date(now.getFullYear(), now.getMonth(), now.getDate() + days, hour, 0);
        const presets = [['Через час', roundHour()], ['Сегодня 20:00', at(0, 20)], ['Завтра 10:00', at(1, 10)], ['Завтра 20:00', at(1, 20)]].filter(([, d]) => d.getTime() >= now.getTime() + MIN_LEAD_MS);
        hours.onchange = () => { selected.setHours(Number(hours.value)); refresh(); };
        minutes.onchange = () => { selected.setMinutes(Number(minutes.value)); refresh(); };
        const root = el('div', { className: 'dtp-overlay', role: 'dialog', ariaModal: 'true', ariaLabel: 'Выбор даты и времени' }, el('div', { className: 'dtp-window' },
            el('div', { className: 'dtp-head' }, button('dtp-nav', '‹', () => month(-1), { ariaLabel: 'Предыдущий месяц' }), title, button('dtp-nav', '›', () => month(1), { ariaLabel: 'Следующий месяц' })),
            grid,
            presets.length ? el('div', { className: 'dtp-presets' }, ...presets.map(([label, d]) => button('dtp-chip', label, () => { selected.setTime(d.getTime()); view.setFullYear(d.getFullYear(), d.getMonth(), 1); refresh(); }))) : null,
            el('div', { className: 'dtp-time' }, el('span', { textContent: 'Время' }), hours, el('b', { textContent: ':' }), minutes),
            summary,
            el('div', { className: 'dtp-actions' }, button('dtp-now', 'Сразу', () => close(null)), button('dtp-cancel', 'Отмена', () => close(undefined)), done)));
        root.addEventListener('click', e => { e.stopPropagation(); if (e.target === root) close(undefined); });   // окно внутри панели публикации: клик её не закрывает
        document.addEventListener('keydown', onKey, true);
        document.body.append(root);
        refresh();
        done.focus();
    });
}

// ---- Редактор поста ----

const TEXT_LIMITS = { tg: [1024, 4096], vk: [16384, 16384] };   // [подпись к медиа, пост без медиа]
const MAX_MEDIA = 10;
const POLL_MAX_OPTIONS = 10;   // как в Telegram: от двух до десяти вариантов
const drafts = new Map();   // черновики правок по записи, пока страница открыта
let ui = null;
let session = null;
document.addEventListener('click', () => { if (ui?.targetMenu) ui.targetMenu.hidden = true; });   // меню «Куда» закрывается кликом в стороне

const textLimit = (target, mediaCount) => (TEXT_LIMITS[String(target).split(':')[0]] || TEXT_LIMITS.tg)[mediaCount ? 0 : 1];
const chip = (label, value) => el('span', { className: 'pe-chip' }, el('span', { className: 'pe-chip-label', textContent: label }), el('strong', { textContent: value }));

function buildUi() {
    const u = {
        title: el('h2', { id: 'post-editor-title', textContent: 'Новый пост' }), chips: el('div', { className: 'pe-chips' }),
        close: button('pe-close', '✕', closeEditor, { ariaLabel: 'Закрыть' }), count: el('span', { className: 'pe-count' }), grid: el('div', { className: 'pe-grid' }),
        file: el('input', { type: 'file', accept: 'image/*,video/*', multiple: true, hidden: true }), url: el('input', { type: 'url', className: 'pe-input', placeholder: 'Ссылка на медиа: https://…' }),
        note: el('p', { className: 'pe-note' }), text: el('textarea', { className: 'pe-text', rows: 8, placeholder: 'Текст поста…', spellcheck: true }),
        counter: el('span', { className: 'pe-counter' }), summary: el('span', { className: 'pe-summary' }), warning: el('p', { className: 'pe-warning', role: 'alert', hidden: true }),
        original: button('pe-chip-btn', 'Текст оригинала', () => setText(session.options.original)),
        description: button('pe-chip-btn', 'Описание ИИ', () => setText(session.options.description)),
        progress: el('div', { className: 'pe-progress-wrap', hidden: true }, el('span', { className: 'pe-progress-label' }),
            el('div', { className: 'pe-progress-bar' }, el('div', { className: 'pe-progress-fill' })), el('span', { className: 'pe-progress-text' })),
        question: el('input', { type: 'text', className: 'pe-input', maxLength: 300, placeholder: 'Вопрос опроса' }),
        pollOptions: el('div', { className: 'pe-poll-options' }), addOption: button('pe-btn pe-btn-ghost', 'Добавить вариант', addPollOption),
        cancel: button('pe-btn', 'Отмена', closeEditor), submit: el('button', { type: 'submit', className: 'pe-btn pe-btn-primary', textContent: 'Запостить' })
    };
    const reset = button('pe-btn pe-btn-ghost', 'Сбросить правки', () => {
        drafts.delete(session.options.key);
        session.media = session.options.media.map(m => ({ ...m }));
        u.text.value = session.options.original || session.options.description || '';
        u.note.textContent = '';
        render();
    });
    u.mediaSection = el('section', { className: 'pe-section' }, el('div', { className: 'pe-section-head' }, el('h3', { textContent: 'Медиа' }), u.count), u.grid,
        el('div', { className: 'pe-url-row' }, u.url, button('pe-btn pe-btn-ghost', 'Добавить по ссылке', addByUrl)), u.note, u.file);
    u.textSection = el('section', { className: 'pe-section' }, el('div', { className: 'pe-section-head' }, el('h3', { textContent: 'Текст' }), u.counter), u.text,
        el('div', { className: 'pe-chip-row' }, u.original, u.description, button('pe-chip-btn', 'Очистить', () => setText(''))));
    u.pollSection = el('section', { className: 'pe-section', hidden: true }, el('div', { className: 'pe-section-head' }, el('h3', { textContent: 'Опрос' })), u.question, u.pollOptions, u.addOption);
    u.form = el('form', { className: 'pe-window', role: 'dialog', ariaModal: 'true', ariaLabelledby: 'post-editor-title' },
        el('header', { className: 'pe-header' }, el('div', { className: 'pe-heading' }, el('span', { className: 'pe-icon', textContent: '🚀' }), u.title), u.close),
        u.chips,
        el('div', { className: 'pe-body' }, u.mediaSection, u.textSection, u.pollSection),
        u.warning,
        u.progress,
        el('footer', { className: 'pe-footer' }, u.summary, el('div', { className: 'pe-actions' }, reset, u.cancel, u.submit)));
    u.overlay = el('div', { className: 'pe-overlay', hidden: true }, u.form);
    u.overlay.addEventListener('mousedown', e => { if (e.target === u.overlay) closeEditor(); });
    document.addEventListener('keydown', e => { if (e.key === 'Escape' && session) { e.preventDefault(); closeEditor(); } });
    u.text.addEventListener('input', () => session && render());
    u.question.addEventListener('input', () => { if (session?.poll) { session.poll.question = u.question.value; render(); } });
    u.url.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); addByUrl(); } });
    u.file.addEventListener('change', () => {
        const files = [...u.file.files].filter(f => /^(image|video)\//.test(f.type));
        u.file.value = '';
        addMedia(files.map(file => {
            const src = URL.createObjectURL(file);
            const kind = file.type.startsWith('video') ? 'video' : 'image';
            return { kind, src, thumb: kind === 'image' ? src : '', name: file.name, file };
        }));
    });
    u.form.addEventListener('submit', submit);
    document.body.append(u.overlay);
    return u;
}

function setText(value) { ui.text.value = value; render(); ui.text.focus(); }

function addByUrl() {
    const value = ui.url.value.trim();
    if (!value) return;
    let url;
    try { url = new URL(value); } catch { ui.note.textContent = 'Это не ссылка.'; return; }
    if (!/^https?:$/.test(url.protocol)) { ui.note.textContent = 'Нужна ссылка http или https.'; return; }
    ui.url.value = ui.note.textContent = '';
    const kind = /\.(mp4|webm|mov|m4v)(\?|#|$)/i.test(url.href) ? 'video' : 'image';
    addMedia([{ kind, src: url.href, thumb: kind === 'image' ? url.href : '', name: url.pathname.split('/').pop() || url.hostname }]);
}

function addMedia(items) {
    for (const item of items) {
        if (session.media.length >= MAX_MEDIA) { ui.note.textContent = `В пост помещается не больше ${MAX_MEDIA} медиа.`; break; }
        session.media.push(item);
    }
    render();
}

function move(from, to) {
    if (to < 0 || to >= session.media.length) return;
    session.media.splice(to, 0, session.media.splice(from, 1)[0]);
    render();
}

function mediaCard(item, i, total) {
    const preview = item.kind === 'video' ? el('video', { src: item.src, poster: item.thumb || '', muted: true, preload: 'metadata', playsInline: true }) : el('img', { src: item.thumb || item.src, alt: '', draggable: false });
    const card = el('div', { className: 'pe-card', draggable: true }, preview, el('span', { className: 'pe-order', textContent: String(i + 1) }),
        item.kind === 'video' ? el('span', { className: 'pe-play', textContent: '▶' }) : null,
        el('div', { className: 'pe-card-tools' }, button('pe-tool', '‹', () => move(i, i - 1), { ariaLabel: 'Сдвинуть влево', disabled: i === 0 }),
            button('pe-tool pe-tool-danger', '✕', () => { session.media.splice(i, 1); render(); }, { ariaLabel: 'Убрать из поста' }),
            button('pe-tool', '›', () => move(i, i + 1), { ariaLabel: 'Сдвинуть вправо', disabled: i === total - 1 })));
    card.addEventListener('dragstart', e => { session.dragFrom = i; card.classList.add('is-dragging'); e.dataTransfer.effectAllowed = 'move'; });
    card.addEventListener('dragend', () => { session.dragFrom = null; card.classList.remove('is-dragging'); });
    card.addEventListener('dragover', e => { if (session.dragFrom !== null) { e.preventDefault(); card.classList.add('is-over'); } });
    card.addEventListener('dragleave', () => card.classList.remove('is-over'));
    card.addEventListener('drop', e => { e.preventDefault(); const from = session.dragFrom; session.dragFrom = null; if (from !== null && from !== i) move(from, i); });
    return card;
}

/** Медиа, счётчики и сводка; черновик сохраняется. */
function render() {
    const { media, poll, options } = session;
    ui.grid.replaceChildren(...media.map((m, i) => mediaCard(m, i, media.length)),
        media.length ? '' : el('div', { className: 'pe-empty', textContent: 'Медиа нет — пост уйдёт только с текстом' }),
        el('button', { type: 'button', className: 'pe-add', ariaLabel: 'Добавить файл', onclick: () => ui.file.click() }, el('span', { className: 'pe-add-plus', textContent: '+' }), el('span', { textContent: 'Файл' })));
    ui.count.textContent = `${media.length} / ${MAX_MEDIA}`;
    if (media.length < MAX_MEDIA && ui.note.textContent.startsWith('В пост помещается')) ui.note.textContent = '';
    ui.mediaSection.hidden = ui.textSection.hidden = Boolean(poll);
    if (poll) return renderPollState(poll);
    const text = ui.text.value;
    const limit = Math.min(...options.picked.map(v => textLimit(v, media.length)));   // лимит — самый строгий из выбранных ресурсов
    const [empty, over] = [!media.length && !text.trim(), text.length > limit];
    ui.counter.textContent = `${text.length} / ${limit}`;
    ui.counter.classList.toggle('is-over', over);
    ui.submit.disabled = empty || over;
    ui.warning.hidden = !(empty || over);
    ui.warning.textContent = empty ? 'Добавьте медиа или текст.' : `Текст длиннее лимита: ${text.length} из ${limit}. Сократите его или уберите медиа.`;
    ui.summary.textContent = `${media.length} медиа · ${text.length} ${pluralRu(text.length, 'символ', 'символа', 'символов')} · ${options.picked.length} ${pluralRu(options.picked.length, 'ресурс', 'ресурса', 'ресурсов')}`;
    ui.original.disabled = !options.original;
    ui.description.disabled = !options.description;
    drafts.set(options.key, { media: media.map(m => ({ ...m })), text });
}

/** Подпись выбранных ресурсов: имя одного или «N из M». */
const targetLabel = o => {
    const picked = o.targets.filter(t => o.picked.includes(t.value));
    return picked.length === 1 ? picked[0].label : `${picked.length} из ${o.targets.length}`;
};

/** Чип «Куда»: один ресурс — подпись; несколько — меню с галочками, отметить можно любые, хотя бы один остаётся. */
function targetChip(o) {
    if (o.targets.length < 2) return chip('Куда', targetLabel(o));
    const label = el('strong', { textContent: `${targetLabel(o)} ▾` });
    const boxes = o.targets.map(t => {
        const box = el('input', { type: 'checkbox', checked: o.picked.includes(t.value) });
        box.addEventListener('change', () => togglePicked(t.value, box));
        return el('label', { className: 'pe-target-option' }, box, el('span', { textContent: t.label }));
    });
    const menu = el('div', { className: 'pe-target-menu', role: 'menu', hidden: true }, ...boxes);
    menu.addEventListener('click', e => e.stopPropagation());   // клик по галочке не закрывает меню
    const button = el('button', { type: 'button', className: 'pe-chip pe-chip-target', title: 'Выбрать ресурсы' },
        el('span', { className: 'pe-chip-label', textContent: 'Куда' }), label);
    button.onclick = e => { e.stopPropagation(); menu.hidden = !menu.hidden; ui.targetMenu = menu; };
    ui.targetLabel = label;
    return el('span', { className: 'pe-target' }, button, menu);
}

/** Галочка в меню «Куда»: меню остаётся открытым; последний выбранный ресурс снять нельзя. */
function togglePicked(value, box) {
    const o = session.options;
    const picked = box.checked ? [...o.picked, value] : o.picked.filter(v => v !== value);
    if (!picked.length) { box.checked = true; return; }
    o.picked = o.targets.map(t => t.value).filter(v => picked.includes(v));
    ui.targetLabel.textContent = `${targetLabel(o)} ▾`;
    render();
}

/** Опрос вместо поста: медиа и текст скрываются, открываются вопрос и варианты. Через «Отложенные» Telegram опрос не ставится. */
function togglePoll() {
    const o = session.options;
    session.poll = session.poll ? null : { question: '', options: ['', ''] };
    if (session.poll && o.mode === 'native') o.mode = 'queue';
    renderPoll();
    renderWhen();
    render();
}

/** Строки вариантов перестраиваются только при открытии, добавлении и удалении: при вводе фокус остаётся в поле. */
function renderPoll() {
    const poll = session?.poll;
    ui.pollSection.hidden = !poll;
    if (!poll) return;
    ui.question.value = poll.question;
    ui.pollOptions.replaceChildren(...poll.options.map((value, i) => {
        const input = el('input', { type: 'text', className: 'pe-input', maxLength: 100, value, placeholder: `Вариант ${i + 1}` });
        input.addEventListener('input', () => { poll.options[i] = input.value; render(); });
        return el('div', { className: 'pe-poll-row' }, input,
            poll.options.length > 2 ? button('pe-chip-btn', '✕', () => { poll.options.splice(i, 1); renderPoll(); render(); }, { ariaLabel: 'Убрать вариант' }) : null);
    }));
    ui.addOption.disabled = poll.options.length >= POLL_MAX_OPTIONS;
}

function addPollOption() {
    session.poll.options.push('');
    renderPoll();
    render();
    ui.pollOptions.lastElementChild?.querySelector('input')?.focus();
}

/** Кнопка отправки для опроса: вопрос и минимум два заполненных варианта. */
function renderPollState(poll) {
    const filled = poll.options.map(v => v.trim()).filter(Boolean);
    const ready = poll.question.trim() !== '' && filled.length >= 2;
    ui.submit.disabled = !ready;
    ui.warning.hidden = ready;
    ui.warning.textContent = 'Напишите вопрос и минимум два варианта ответа.';
    ui.summary.textContent = `Опрос · ${filled.length} ${pluralRu(filled.length, 'вариант', 'варианта', 'вариантов')}`;
}

/** Переключатель «Опрос» в строке чипов. */
function pollChip() {
    const on = Boolean(session.poll);
    return el('button', { type: 'button', className: `pe-chip pe-chip-poll${on ? ' is-on' : ''}`, title: 'Опрос вместо текста и медиа', textContent: on ? '📊 Опрос ✕' : '📊 Опрос', onclick: togglePoll });
}

/** «Куда / Когда»; «Когда» открывает то же окно выбора времени. */
/** Подпись «Когда»: сразу, слот канала или дата и способ — через «Отложенные» Telegram или через очередь Posta. */
function whenLabel(o) {
    if (o.mode === 'slot') return o.picked.length > 1 ? 'в ближайший слот каждого канала' : `в слот · ${formatWhen(new Date(o.slotAt * 1000))}`;
    if (!o.scheduledTs) return 'сразу';
    return `${formatWhen(new Date(o.scheduledTs * 1000))} · ${o.mode === 'native' ? '«Отложенные» Telegram' : 'очередь Posta'}`;
}

function setWhen(o, patch) {
    Object.assign(o, patch);
    renderWhen();
    render();
}

async function pickDate(o) {
    const chosen = await pickDateTime({ value: o.scheduledTs ? new Date(o.scheduledTs * 1000) : null });
    if (chosen === undefined || !session) return;
    if (!chosen) return setWhen(o, { mode: 'now', scheduledTs: 0, scheduledAt: '' });
    const mode = o.mode === 'native' || o.mode === 'queue' ? o.mode : (o.schedule?.native ? 'native' : 'queue');
    setWhen(o, { mode, scheduledTs: Math.floor(chosen.getTime() / 1000), scheduledAt: formatWhen(chosen) });
}

/** Ближайший свободный слот: у каждого канала свой, сервер выберет при публикации; здесь показываем слот первого канала с расписанием. */
async function pickSlot(o) {
    const first = o.picked.find(v => o.schedule.slots(v));
    if (!first) { ui.note.textContent = 'У выбранных ресурсов нет расписания: «Соцсети» → канал → «Расписание».'; return; }
    const slot = await o.schedule.nextSlot(first).catch(() => null);
    if (!session) return;
    if (!slot || slot.none) { ui.note.textContent = 'В расписании канала нет свободного слота в ближайшие 60 дней.'; return; }
    setWhen(o, { mode: 'slot', slotAt: slot.slot_at, scheduledTs: slot.slot_at, scheduledAt: formatWhen(new Date(slot.slot_at * 1000)) });
}

/** Чип «Когда»: меню — сразу, ближайший слот канала (если расписание есть), дата; при личном аккаунте — выбор способа. */
function whenChip(o) {
    const items = [
        { label: 'Сразу', run: () => setWhen(o, { mode: 'now', scheduledTs: 0, scheduledAt: '' }) },
        ...(o.schedule ? [{ label: 'В ближайший слот', run: () => pickSlot(o) }] : []),
        { label: 'Выбрать дату и время…', run: () => pickDate(o) },
        ...(o.scheduledTs && o.schedule?.native && o.mode !== 'slot' && !session.poll ? [
            { label: o.mode === 'native' ? '✓ Через «Отложенные» Telegram' : 'Через «Отложенные» Telegram', run: () => setWhen(o, { mode: 'native' }) },
            { label: o.mode === 'queue' ? '✓ Через очередь Posta' : 'Через очередь Posta', run: () => setWhen(o, { mode: 'queue' }) },
        ] : []),
    ];
    const menu = el('div', { className: 'pe-target-menu', role: 'menu', hidden: true }, ...items.map(item => {
        const button = el('button', { type: 'button', role: 'menuitem', textContent: item.label });
        button.onclick = () => { menu.hidden = true; item.run(); };
        return button;
    }));
    const toggle = el('button', { type: 'button', className: 'pe-chip pe-chip-when', title: 'Выбрать время публикации' },
        el('span', { className: 'pe-chip-label', textContent: 'Когда' }), el('strong', { textContent: whenLabel(o) }));
    toggle.onclick = e => { e.stopPropagation(); menu.hidden = !menu.hidden; ui.targetMenu = menu; };
    return el('span', { className: 'pe-target' }, toggle, menu);
}

function renderWhen() {
    const o = session.options;
    ui.chips.replaceChildren(targetChip(o), whenChip(o), pollChip());
    ui.title.textContent = o.scheduledTs ? 'Отложенный пост' : 'Новый пост';
    ui.submit.textContent = o.scheduledTs ? 'Запланировать' : 'Запостить';
}

async function submit(e) {
    e.preventDefault();
    if (!session || ui.submit.disabled) return;
    const { options, media } = session;
    const label = ui.submit.textContent;
    Object.assign(ui.submit, { disabled: true, textContent: 'Отправляю…' });
    ui.cancel.disabled = true;
    ui.warning.hidden = true;
    // Этап и процент; percent null — полоса бежит без процентов
    const onProgress = ({ stage, percent }) => {
        const known = typeof percent === 'number';
        ui.progress.classList.toggle('is-indeterminate', !known);
        ui.progress.querySelector('.pe-progress-label').textContent = stage;
        ui.progress.querySelector('.pe-progress-fill').style.width = known ? `${percent}%` : '';
        ui.progress.querySelector('.pe-progress-text').textContent = known ? `${percent}%` : '';
    };
    onProgress({ stage: 'Готовлю пост…', percent: null });
    ui.progress.hidden = false;
    try {
        const { poll } = session;
        const post = { targets: [...options.picked], mode: options.mode, scheduledAt: options.scheduledAt, scheduledTs: options.mode === 'now' ? 0 : options.scheduledTs || 0,
            media: poll ? [] : media.map(({ kind, src, name, file }) => ({ kind, src, name, file })), text: poll ? '' : ui.text.value.trim(),
            poll: poll ? { question: poll.question.trim(), options: poll.options.map(v => v.trim()).filter(Boolean) } : null };
        await options.onSubmit(post, onProgress);
        drafts.delete(options.key);
        closeEditor();
    } catch (error) {
        if (session) { ui.warning.hidden = false; ui.warning.textContent = error.message || String(error); }
    } finally {
        ui.submit.textContent = label;
        ui.cancel.disabled = false;
        ui.progress.hidden = true;
        if (session) ui.submit.disabled = false;
    }
}

function closeEditor() {
    if (!session) return;
    ui.overlay.hidden = true;
    document.body.classList.remove('post-editor-open');
    session = null;
}

/** options: { key, targets: [{value, label}], picked: [value], scheduledAt, scheduledTs, media: [{kind, src, thumb, name}], original, description, schedule, onSubmit(post) → Promise }.
 *  post.targets — значения выбранных ресурсов; post.poll — {question, options} вместо текста и медиа. */
export function openPostEditor(options) {
    ui ||= buildUi();
    options.mode ??= options.scheduledTs ? (options.schedule?.native ? 'native' : 'queue') : 'now';
    options.picked = [...options.picked];
    const draft = drafts.get(options.key);
    session = { options, media: (draft ? draft.media : options.media).map(m => ({ ...m })), poll: null, dragFrom: null };
    renderPoll();
    renderWhen();
    ui.text.value = draft ? draft.text : (options.original || options.description || '');
    ui.note.textContent = ui.url.value = '';
    ui.overlay.hidden = false;
    document.body.classList.add('post-editor-open');
    render();
    ui.text.focus();
    ui.text.setSelectionRange(ui.text.value.length, ui.text.value.length);
}
