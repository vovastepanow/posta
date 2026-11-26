// editor.js — окно выбора даты и времени и модальный редактор поста (порядок медиа, текст, отправка через onSubmit).
import { VIDEO_ICON, api, el, pluralRu } from './core.js';
import { commonCapabilities, commonPollLimits, mediaKind, validateTarget, attachmentError, extraActive, extraAvailability, extraError, EXTRA_KEYS, standaloneExtra } from './editor-capabilities.js';

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
const POLL_MAX_OPTIONS = 10;   // как в Telegram: от двух до десяти вариантов
const drafts = new Map();   // черновики правок по записи, пока страница открыта
let ui = null;
let session = null;
document.addEventListener('click', () => { if (ui?.targetMenu) ui.targetMenu.hidden = true; });   // меню «Куда» закрывается кликом в стороне

const textLimit = (target, mediaCount) => (session.options.targets.find(t => t.value === target)?.textLimit || TEXT_LIMITS[String(target).split(':')[0]] || TEXT_LIMITS.tg)[mediaCount ? 0 : 1];
const selectedTargets = () => session.options.picked.map(v => session.options.targets.find(t => t.value === v));
const currentCapabilities = () => commonCapabilities(selectedTargets());
const hasExtras = () => EXTRA_KEYS.some(k=>extraActive(session.extras,k));
const botOnlyMedia = () => session.media.some(m => ['audio','document','voice'].includes(mediaKind(m)));
const clonePoll = p => p ? {question:p.question || '',options:[...(p.options || [])],multiple:Boolean(p.multiple),duration_minutes:p.duration_minutes || 1440,quiz:!!p.quiz,correct_answer:p.correct_answer||'',explanation:p.explanation||'',close_minutes:p.close_minutes||0,public:!!p.public} : null;
const fileKind = f => /\.(pdf|txt|csv|zip|docx|xlsx|pptx)$/i.test(f.name) || f.type==='application/pdf' ? 'document' : /\.(ogg|opus)$/i.test(f.name) ? 'voice' : /^(audio\/mpeg|audio\/mp4|audio\/x-m4a)$/i.test(f.type) || /\.(mp3|m4a)$/i.test(f.name) ? 'audio' : f.type.startsWith('video') ? 'video' : f.type.startsWith('image') ? 'image' : '';
const cloneExtras = e => ({buttons:(e?.buttons || []).map(b=>({...b})),silent:!!e?.silent,protect:!!e?.protect,spoiler:!!e?.spoiler,preview:e?.preview || 'default',location:e?.location?{...e.location}:null,contact:e?.contact?{...e.contact}:null});
const EXTRA_LABELS = {silent:'Без звукового уведомления',protect:'Запретить пересылку и сохранение',spoiler:'Скрыть медиа под спойлером'};
const KIND_LABELS = {photo:'Фото', animation:'GIF', video:'Видео', audio:'Аудио', document:'Документ', voice:'Голосовое'};
const fileAccept = {photo:'image/jpeg,image/png,image/webp,image/bmp,image/heic,image/avif', animation:'image/gif', video:'video/*', audio:'.mp3,.m4a,audio/mpeg,audio/mp4', document:'.pdf,.txt,.csv,.zip,.docx,.xlsx,.pptx', voice:'.ogg,.opus,audio/ogg'};
const chip = (label, value) => el('span', { className: 'pe-chip' }, el('span', { className: 'pe-chip-label', textContent: label }), el('strong', { textContent: value }));

function buildUi() {
    const u = {
        title: el('h2', { id: 'post-editor-title', textContent: 'Новый пост' }), chips: el('div', { className: 'pe-chips' }),
        close: button('pe-close', '✕', closeEditor, { ariaLabel: 'Закрыть' }), count: el('span', { className: 'pe-count' }), grid: el('div', { className: 'pe-grid' }),
        file: el('input', { type: 'file', accept: 'image/*,video/*', multiple: true, hidden: true }), url: el('input', { type: 'url', className: 'pe-input', placeholder: 'Ссылка на медиа: https://…' }),
        note: el('p', { className: 'pe-note' }), text: el('textarea', { className: 'pe-text', rows: 8, placeholder: 'Текст поста…', spellcheck: true }),
        counter: el('span', { className: 'pe-counter' }), summary: el('span', { className: 'pe-summary' }), extras: el('div',{className:'pe-extras-fields'}), buttons: el('div',{className:'pe-button-rows'}), cards:el('div',{className:'pe-extra-cards'}), checks: el('div', { className: 'pe-checks', role: 'status' }), warning: el('p', { className: 'pe-warning', role: 'alert', hidden: true }),
        original: button('pe-chip-btn', 'Текст оригинала', () => setText(session.options.original)),
        description: button('pe-chip-btn', 'Описание ИИ', () => setText(session.options.description)),
        aiButton: button('pe-chip-btn pe-chip-ai', '✨ Подпись от ИИ', aiCaption, { title: 'ИИ напишет подпись в стиле канала; если в поле уже есть ваш текст — улучшит его' }),
        excerptButton: button('pe-chip-btn pe-chip-ai', '✂ Сделать выжимку', makeExcerpt, { title: 'Выжимка 2–3 предложения из статьи, своими словами; ссылка на источник добавится' }),
        aiNote: el('p', { className: 'pe-ai-note', role: 'status' }),
        tags: el('div', { className: 'pe-tags', hidden: true }),
        styleText: el('textarea', { className: 'pe-style-text', rows: 5, maxLength: 1200, spellcheck: true,
            placeholder: 'Опишите, как пишут в канале: тон, длина, эмодзи, хештеги… Или нажмите «Изучить по постам».' }),
        styleNote: el('p', { className: 'pe-ai-note' }),
        progress: el('div', { className: 'pe-progress-wrap', hidden: true }, el('span', { className: 'pe-progress-label' }),
            el('div', { className: 'pe-progress-bar' }, el('div', { className: 'pe-progress-fill' })), el('span', { className: 'pe-progress-text' })),
        question: el('input', { type: 'text', className: 'pe-input', maxLength: 300, placeholder: 'Вопрос опроса' }),
        pollOptions: el('div', { className: 'pe-poll-options' }), attachments: el('div', { className: 'pe-attachments' }), multiple: el('input', {type:'checkbox'}), duration: el('select',{className:'pe-input'}, ...[[5,'5 минут'],[60,'1 час'],[360,'6 часов'],[1440,'1 сутки'],[4320,'3 суток'],[10080,'7 суток']].map(([value,label])=>el('option',{value:String(value),textContent:label}))), addOption: button('pe-btn pe-btn-ghost', 'Добавить вариант', addPollOption), quiz:el('input',{type:'checkbox'}), publicPoll:el('input',{type:'checkbox'}), closePoll:el('select',{className:'pe-input'},...[[0,'Без ограничения'],[5,'5 минут'],[60,'1 час'],[360,'6 часов'],[1440,'1 сутки'],[4320,'3 суток'],[10080,'7 суток']].map(([value,textContent])=>el('option',{value:String(value),textContent}))), correct:el('select',{className:'pe-input',ariaLabel:'Правильный ответ'}), explanation:el('textarea',{className:'pe-input',rows:3,maxLength:200,placeholder:'Пояснение после ответа'}),
        cancel: button('pe-btn', 'Отмена', closeEditor), submit: el('button', { type: 'submit', className: 'pe-btn pe-btn-primary', textContent: 'Запостить' })
    };
    const reset = button('pe-btn pe-btn-ghost', 'Сбросить правки', () => {
        drafts.delete(session.options.key);
        session.media = session.options.media.map(m => ({ ...m }));
        session.tags = [...(session.options.hashtags || [])];
        session.poll = clonePoll(session.options.poll);
        session.extras = cloneExtras(session.options.extras);
        renderExtras();
        u.text.value = session.options.original || '';
        renderPoll(); renderWhen();
        u.note.textContent = '';
        render();
    });
    u.mediaSection = el('section', { className: 'pe-section' }, el('div', { className: 'pe-section-head' }, el('h3', { textContent: 'Медиа' }), u.count), u.attachments, u.grid,
        el('div', { className: 'pe-url-row' }, u.url, button('pe-btn pe-btn-ghost', 'Добавить по ссылке', addByUrl)), u.note, u.file);
    u.learnButton = button('pe-chip-btn', 'Изучить по постам', learnStyle, { title: 'ИИ прочитает последние посты канала и опишет манеру письма (один раз, пара минут)' });
    u.styleBox = el('details', { className: 'pe-style' }, el('summary', { textContent: 'Стиль канала' }), u.styleText,
        el('div', { className: 'pe-chip-row' }, button('pe-chip-btn', 'Сохранить стиль', saveStyle), u.learnButton), u.styleNote);
    u.textSection = el('section', { className: 'pe-section' }, el('div', { className: 'pe-section-head' }, el('h3', { textContent: 'Текст' }), u.counter), u.text,
        el('div', { className: 'pe-chip-row' }, u.original, u.description, u.aiButton, u.excerptButton, button('pe-chip-btn', 'Очистить', () => setText(''))), u.aiNote, u.tags, u.styleBox);
    u.pollSection = el('section', { className: 'pe-section', hidden: true }, el('div', { className: 'pe-section-head' }, el('h3', { textContent: 'Опрос' })), el('p',{className:'pe-poll-help',textContent:'Опрос публикуется вместо текста и медиа. При выключении опроса ваши вложения и текст вернутся.'}), u.question, u.pollOptions, u.addOption, el('label', {className:'pe-poll-setting'}, u.multiple, el('span', {textContent:'Разрешить несколько вариантов ответа'})), el('label',{className:'pe-poll-duration',hidden:true},el('span',{textContent:'Длительность опроса в X'}),u.duration),
        el('label',{className:'pe-poll-setting'},u.publicPoll,el('span',{textContent:'Публичное голосование'})),
        el('label',{className:'pe-poll-duration'},el('span',{textContent:'Закрыть голосование через'}),u.closePoll),
        el('label',{className:'pe-poll-setting'},u.quiz,el('span',{textContent:'Викторина'})),
        el('label',{className:'pe-poll-duration'},el('span',{textContent:'Правильный ответ'}),u.correct),u.explanation);
    u.extraSection=el('section',{className:'pe-section pe-extra-section'},el('div',{className:'pe-section-head'},el('h3',{textContent:'Дополнения и настройки публикации'})),u.extras,u.buttons,u.cards);
    u.form = el('form', { className: 'pe-window', role: 'dialog', ariaModal: 'true', ariaLabelledby: 'post-editor-title' },
        el('header', { className: 'pe-header' }, el('div', { className: 'pe-heading' }, el('span', { className: 'pe-icon', textContent: '🚀' }), u.title), u.close),
        u.chips,
        el('div', { className: 'pe-body' }, u.textSection, u.mediaSection, u.pollSection, u.extraSection, el('section', { className: 'pe-section pe-compatibility' }, el('div', { className: 'pe-section-head' }, el('h3', { textContent: 'Проверка площадок' })), u.checks)),
        u.warning,
        u.progress,
        el('footer', { className: 'pe-footer' }, u.summary, el('div', { className: 'pe-actions' }, reset, u.cancel, u.submit)));
    u.overlay = el('div', { className: 'pe-overlay', hidden: true }, u.form);
    u.overlay.addEventListener('mousedown', e => { if (e.target === u.overlay) closeEditor(); });
    document.addEventListener('keydown', e => { if (e.key === 'Escape' && session) { e.preventDefault(); closeEditor(); } });
    u.text.addEventListener('input', () => session && render());
    u.quiz.onchange=()=>{if(session?.poll){session.poll.quiz=u.quiz.checked;renderPoll();render();}};
    u.publicPoll.onchange=()=>{if(session?.poll){session.poll.public=u.publicPoll.checked;render();}};
    u.closePoll.onchange=()=>{if(session?.poll){session.poll.close_minutes=Number(u.closePoll.value);render();}};
    u.correct.onchange=()=>{if(session?.poll){session.poll.correct_answer=u.correct.value;render();}};
    u.explanation.oninput=()=>{if(session?.poll){session.poll.explanation=u.explanation.value;render();}};
    u.duration.addEventListener('change', () => { if (session?.poll) { session.poll.duration_minutes=Number(u.duration.value); render(); } });
    u.multiple.addEventListener('change', () => { if (session?.poll) { session.poll.multiple = u.multiple.checked; render(); } });
    u.question.addEventListener('input', () => { if (session?.poll) { session.poll.question = u.question.value; render(); } });
    u.url.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); addByUrl(); } });
    u.file.addEventListener('change', () => {
        const files = [...u.file.files];
        u.file.value = '';
        addFiles(files);
    });
    u.grid.addEventListener('dragover', e => {
        if (session && !session.busy && e.dataTransfer?.types.includes('Files')) { e.preventDefault(); u.grid.classList.add('is-file-drop'); }
    });
    u.grid.addEventListener('dragleave', e => { if (!u.grid.contains(e.relatedTarget)) u.grid.classList.remove('is-file-drop'); });
    u.grid.addEventListener('drop', e => {
        u.grid.classList.remove('is-file-drop');
        if (session && !session.busy && e.dataTransfer?.files.length) { e.preventDefault(); addFiles([...e.dataTransfer.files]); }
    });
    u.form.addEventListener('submit', submit);
    document.body.append(u.overlay);
    return u;
}

// ---- Подпись от ИИ и стиль канала (сервер: lib/channel_style.php; options.ai задаёт content.js) ----

const styleTarget = () => session.options.picked[0];

/** Стиль канала первой выбранной цели → поля блока «Стиль канала». */
function renderStyle() {
    const ai = session.options.ai;
    ui.aiButton.hidden = ui.styleBox.hidden = !ai;
    ui.excerptButton.hidden = !(ai && session.options.newsUrl);   // выжимка — только для новости со ссылкой на источник
    if (!ai) return;
    const st = ai.style(styleTarget());
    ui.styleText.value = st.summary || '';
    ui.learnButton.hidden = !st.can_learn;
    const when = st.updated_at ? new Date(st.updated_at * 1000).toLocaleDateString('ru-RU') : '';
    ui.styleNote.textContent = st.busy ? 'Изучаю стиль канала…'
        : st.summary ? (st.source === 'manual' ? 'Описано вручную' : `Изучено по постам канала ${when}${st.stale ? ' — давно не обновлялось, нажмите «Изучить по постам»' : ''}`)
        : (st.error || 'Стиль не задан: подписи пишутся в нейтральной манере');
}

function setAiBusy(message) {
    ui.aiBusy = Boolean(message);
    ui.aiButton.disabled = ui.aiBusy;
    ui.aiNote.textContent = message;
}

async function aiCaption() {
    const o = session.options;
    if (!o.ai || ui.aiBusy) return;
    const target = styleTarget();
    let warning = '';
    try {
        setAiBusy('ИИ пишет подпись…');
        let st = o.ai.style(target);
        if (!st.summary && st.can_learn && !st.error) {   // стиль ещё не изучен: один раз по постам канала, дальше он хранится в профиле
            setAiBusy('Изучаю стиль канала по постам — это один раз, около двух минут…');
            st = await o.ai.learn(target);
            renderStyle();
            if (!st.summary && st.error) warning = `Стиль изучить не вышло: ${st.error}`;
            setAiBusy('ИИ пишет подпись…');
        }
        const typed = ui.text.value.trim();
        const mine = typed && typed !== (o.original || '').trim() && typed !== (o.description || '').trim() ? typed : '';   // чужой текст по умолчанию черновиком не считаем
        ui.text.value = await o.ai.generate(target, { description: o.description || '', original: o.original || '', current: mine });
        render();
        setAiBusy('');
        ui.aiNote.textContent = warning ? `${warning} Подпись написана в нейтральной манере.` : '';
    } catch (error) {
        setAiBusy('');
        ui.aiNote.textContent = error.message;
    }
}

/** Выжимка из статьи новости: заголовок, 2–3 предложения и ссылка на источник; текст поля заменяется. */
async function makeExcerpt() {
    const o = session.options;
    if (!o.ai || !o.newsUrl || ui.aiBusy) return;
    try {
        setAiBusy('Читаю статью и делаю выжимку…');
        const excerpt = await o.ai.excerpt(o.newsUrl);
        const title = (o.original || '').split('\n')[0].replace(/^\*\*|\*\*$/g, '').trim();
        ui.text.value = [title, excerpt, `Источник: ${o.newsUrl}`].filter(Boolean).join('\n\n');
        render();
        setAiBusy('');
        ui.aiNote.textContent = 'Выжимка вставлена в текст. Проверьте её перед публикацией.';
    } catch (error) { setAiBusy(''); ui.aiNote.textContent = error.message; }
}

async function learnStyle() {
    const o = session.options;
    if (!o.ai || ui.aiBusy) return;
    try {
        setAiBusy('Изучаю стиль канала по постам — около двух минут…');
        await o.ai.learn(styleTarget());
        setAiBusy('');
    } catch (error) { setAiBusy(''); ui.aiNote.textContent = error.message; }
    renderStyle();
}

async function saveStyle() {
    const o = session.options;
    if (!o.ai || ui.aiBusy) return;
    try {
        await o.ai.save(styleTarget(), ui.styleText.value);
        ui.aiNote.textContent = '';
    } catch (error) { ui.aiNote.textContent = error.message; }
    renderStyle();
}

function setText(value) { ui.text.value = value; render(); ui.text.focus(); }

function addByUrl() {
    const value = ui.url.value.trim();
    if (!value) return;
    let url;
    try { url = new URL(value); } catch { ui.note.textContent = 'Это не ссылка.'; return; }
    if (!/^https?:$/.test(url.protocol)) { ui.note.textContent = 'Нужна ссылка http или https.'; return; }
    ui.url.value = ui.note.textContent = '';
    const kind = /\.(mp3|m4a)$/i.test(url.pathname) ? 'audio' : /\.(pdf|txt|csv|zip|docx|xlsx|pptx)$/i.test(url.pathname) ? 'document' : /\.(ogg|opus)$/i.test(url.pathname) ? 'voice' : /\.(mp4|webm|mov|m4v)$/i.test(url.pathname) ? 'video' : 'image';
    addMedia([{ kind, src: url.href, thumb: kind === 'image' ? url.href : '', name: url.pathname.split('/').pop() || url.hostname }]);
}

function addFiles(files) {
    const items = [], errors = [];
    for (const file of files) {
        const kind = fileKind(file);
        if (!kind) { errors.push(file.name + ': неподдерживаемый формат.'); continue; }
        const src = URL.createObjectURL(file);
        items.push({kind, src, thumb:kind === 'image' ? src : '', name:file.name, file});
    }
    addMedia(items, errors);
}

function addMedia(items, errors = []) {
    for (const item of items) {
        const error = attachmentError(selectedTargets(), session.media, item);
        if (error) {
            errors.push((item.name || 'Файл') + ': ' + error);
            if (item.file && item.src.startsWith('blob:')) URL.revokeObjectURL(item.src);
            continue;
        }
        session.media.push(item);
    }
    ui.note.textContent = errors.join(' ');
    render();
}

function move(from, to) {
    if (to < 0 || to >= session.media.length) return;
    session.media.splice(to, 0, session.media.splice(from, 1)[0]);
    render();
}

function mediaCard(item, i, total) {
    const preview = ['audio','voice'].includes(item.kind) ? el('div',{className:'pe-file-preview'}, el('strong',{textContent:item.kind==='voice'?'Голосовое':'Аудио'}), el('span',{textContent:item.name || 'Аудиофайл'}), el('audio',{src:item.src, controls:true, preload:'none'})) :
        item.kind === 'document' ? el('div',{className:'pe-file-preview'}, el('strong',{textContent:(item.name?.split('.').pop() || 'Файл').toUpperCase()}), el('span',{textContent:item.name || 'Документ'})) :
        item.kind === 'video' ? el('video', { src: item.src, poster: item.thumb || '', muted: true, preload: 'metadata', playsInline: true }) : el('img', { src: item.thumb || item.src, alt: '', draggable: false });
    const card = el('div', { className: 'pe-card', draggable: true }, preview, el('span', { className: 'pe-order', textContent: String(i + 1) }),
        item.kind === 'video' ? el('span', { className: 'video-mark', innerHTML: VIDEO_ICON }) : null,
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
    const pollLimits = commonPollLimits(selectedTargets());
    const caps = currentCapabilities();
    if (options.mode === 'native' && (botOnlyMedia() || (!poll && hasExtras()))) { options.mode = 'queue'; renderWhen(); }
    const allowed = caps.kinds.filter(kind => !attachmentError(selectedTargets(), media, {kind}));
    const accept = allowed.map(k => fileAccept[k]).filter(Boolean);
    ui.file.accept = accept.join(',');
    const mediaChanged = session.renderedMedia?.length !== media.length || media.some((m,i) => m !== session.renderedMedia?.[i]) || session.renderedAllowed !== accept.join(',');
    if (mediaChanged) ui.attachments.replaceChildren(...Object.entries(KIND_LABELS).map(([kind,label]) => {
        const enabled = allowed.includes(kind) && media.length < caps.mediaMax;
        return button('pe-btn pe-attachment', '+ ' + label, () => { ui.file.accept = fileAccept[kind]; ui.file.click(); }, {disabled:!enabled,title:enabled ? 'Добавить: ' + label : 'Недоступно для выбранных ресурсов или состава поста'});
    }));
    const canAdd = allowed.length > 0 && media.length < caps.mediaMax;
    ui.url.disabled = !canAdd;
    ui.mediaSection.querySelector('.pe-url-row button').disabled = !canAdd;
    const pollToggle = ui.chips.querySelector('.pe-chip-poll');
    if (pollToggle) {
        pollToggle.disabled = !poll && !caps.poll;
        pollToggle.title = caps.poll ? 'Опрос вместо текста и медиа' : 'Опрос доступен только при поддержке всеми выбранными ресурсами';
    }
    const standalone=!poll&&standaloneExtra(session.extras);
    const problems = selectedTargets().map(t => ({ label: t?.label || 'Ресурс', error: standalone ? extraError([t],[], '',null,session.extras) : validateTarget(t, media, ui.text.value, poll) }));
    ui.checks.replaceChildren(...problems.map(p => el('div', { className: 'pe-check' + (p.error ? ' is-error' : '') }, el('strong', { textContent: p.label }), el('span', { textContent: p.error || '✓ Формат совместим' }))));
    const extrasError = poll ? '' : extraError(selectedTargets(), standalone?[]:media, standalone?'':ui.text.value, null, session.extras);
    if(extrasError) ui.checks.append(el('div',{className:'pe-check is-error'},el('strong',{textContent:'Дополнения'}),el('span',{textContent:extrasError})));
    updateExtraAvailability();
    const blocked = problems.some(p => p.error) || !problems.length || Boolean(extrasError);
    const snapshot = { media: media.map(m => ({ ...m })), text: ui.text.value, tags: [...session.tags], poll: clonePoll(poll), extras:cloneExtras(session.extras) };
    drafts.set(options.key, snapshot);
    options.onDraftChange?.(snapshot);
    if (mediaChanged) { ui.grid.replaceChildren(...media.map((m, i) => mediaCard(m, i, media.length)),
        media.length ? '' : el('div', { className: 'pe-empty', textContent: 'Перетащите файлы сюда или выберите тип вложения выше' }),
        el('button', { type: 'button', className: 'pe-add', ariaLabel: 'Добавить файл', disabled: !canAdd, onclick: () => { ui.file.accept = accept.join(','); ui.file.click(); } }, el('span', { className: 'pe-add-plus', textContent: '+' }), el('span', { textContent: 'Файл' })));
        session.renderedMedia = [...media]; session.renderedAllowed = accept.join(',');
    }
    ui.count.textContent = `${media.length} / ${caps.mediaMax}`;
    ui.mediaSection.hidden = ui.textSection.hidden = Boolean(poll)||standalone;
    ui.extraSection.hidden = Boolean(poll);
    if (poll) {
        ui.addOption.disabled = !pollLimits || poll.options.length >= pollLimits.options_max;
        ui.multiple.disabled = !poll.multiple && (!pollLimits?.multiple||poll.quiz);
        ui.duration.closest('label').hidden = !pollLimits?.duration;
        ui.quiz.disabled=!poll.quiz&&(!pollLimits?.quiz||poll.multiple);
        ui.publicPoll.disabled=!poll.public&&!pollLimits?.public;
        ui.closePoll.disabled=!poll.close_minutes&&!pollLimits?.close;
        ui.correct.closest('label').hidden=ui.explanation.hidden=!poll.quiz;
        if(poll.quiz){
            const choices=poll.options.map(o=>o.trim()).filter(Boolean);
            const key=JSON.stringify(choices);
            if(session.correctKey!==key){ui.correct.replaceChildren(el('option',{value:'',textContent:'Выберите ответ'}),...choices.map(o=>el('option',{value:o,textContent:o})));session.correctKey=key;}
            ui.correct.value=poll.correct_answer||'';
        }
        return renderPollState(poll, blocked);
    }
    const text = ui.text.value;
    const limit = Math.min(...options.picked.map(v => textLimit(v, media.length)));   // лимит — самый строгий из выбранных ресурсов
    const length = [...text].length;
    const [empty, over] = [!standalone&&!media.length&&!text.trim(), !standalone&&length>limit];
    ui.counter.textContent = `${length} / ${limit}`;
    ui.counter.classList.toggle('is-over', over);
    ui.submit.disabled = empty || over || blocked || session.busy;
    ui.warning.hidden = !(empty || over || blocked);
    ui.warning.textContent = empty ? 'Добавьте медиа или текст.' : over ? `Текст длиннее лимита: ${length} из ${limit}.` : 'Исправьте несовместимость в блоке проверки площадок.';
    if(standalone){ui.summary.textContent=session.extras.location?'Геолокация':'Карточка контакта';return;}
    ui.summary.textContent = `${media.length} медиа · ${text.length} ${pluralRu(text.length, 'символ', 'символа', 'символов')} · ${options.picked.length} ${pluralRu(options.picked.length, 'ресурс', 'ресурса', 'ресурсов')}`;
    ui.original.disabled = !options.original;
    ui.description.disabled = !options.description;
    renderTags();
}

function renderExtras(){
    const extra=session.extras;
    ui.extras.replaceChildren(...Object.entries(EXTRA_LABELS).map(([key,label])=>{
        const input=el('input',{type:'checkbox',checked:extra[key],onchange:()=>{extra[key]=input.checked;render();}});
        return el('label',{className:'pe-extra-control'},input,el('span',{textContent:label}));
    }));
    const preview=el('select',{className:'pe-input',ariaLabel:'Превью ссылки'},...[['default','По умолчанию'],['on','Показывать'],['off','Не показывать']].map(([value,textContent])=>el('option',{value,textContent})));
    preview.value=extra.preview;preview.onchange=()=>{extra.preview=preview.value;render();};
    ui.extras.append(el('label',{className:'pe-extra-preview'},el('span',{textContent:'Предпросмотр ссылки'}),preview));
    renderExtraButtons();
    renderExtraCards();
}
function renderExtraCards(){
    const extra=session.extras;
    const actions=el('div',{className:'pe-chip-row'},...Object.entries({location:'Геолокация',contact:'Контакт'}).map(([key,label])=>{
        const b=button('pe-btn pe-card-toggle',extra[key]?'✕ '+label:'+ '+label,()=>{
            if(extra[key])extra[key]=null;
            else {
                if(standaloneExtra(extra))return;
                extra[key]=key==='location'?{latitude:'',longitude:''}:{first_name:'',last_name:'',phone:''};
            }
            renderExtraCards();render();
        });
        b.dataset.extraCard=key;return b;
    }));
    const fields=[];
    if(extra.location)fields.push(['latitude','Широта',extra.location],['longitude','Долгота',extra.location]);
    if(extra.contact)fields.push(['first_name','Имя',extra.contact],['last_name','Фамилия (необязательно)',extra.contact],['phone','Телефон +…',extra.contact]);
    ui.cards.replaceChildren(actions,...(fields.length?[el('p',{className:'pe-poll-help',textContent:'Карточка будет опубликована отдельно. Исходные текст и медиа сохраняются в редакторе до выключения карточки.'})]:[]),
        ...fields.map(([key,label,object])=>el('label',{className:'pe-card-field'},el('span',{textContent:label}),el('input',{className:'pe-input',value:String(object[key]??''),oninput:e=>{object[key]=e.target.value;render();}}))));
}
function renderExtraButtons(){
    ui.buttons.replaceChildren(el('div',{className:'pe-section-head'},el('h3',{textContent:'Кнопки-ссылки'}),button('pe-chip-btn pe-add-link','+ Кнопка',()=>{session.extras.buttons.push({text:'',url:''});renderExtraButtons();render();})),
        ...session.extras.buttons.map((b,i)=>{
            const text=el('input',{className:'pe-input',placeholder:'Название',value:b.text,maxLength:64,ariaLabel:'Название кнопки'});
            const url=el('input',{className:'pe-input',placeholder:'https://…',value:b.url,maxLength:2048,ariaLabel:'Ссылка кнопки'});
            text.oninput=()=>{b.text=text.value;render();};url.oninput=()=>{b.url=url.value;render();};
            return el('div',{className:'pe-button-row'},text,url,button('pe-chip-btn','✕',()=>{session.extras.buttons.splice(i,1);renderExtraButtons();render();},{ariaLabel:'Удалить кнопку'}));
        }));
}
function updateExtraAvailability(){
    if(!session)return;
    const card=standaloneExtra(session.extras);
    const availability=key=>card&&['buttons','spoiler','preview'].includes(key)?'Недоступно для отдельной карточки.':extraAvailability(selectedTargets(),card?[]:session.media,card?'':ui.text.value,session.poll,key);
    const inputs=[...ui.extras.querySelectorAll('input')];
    Object.keys(EXTRA_LABELS).forEach((key,i)=>{const reason=availability(key);inputs[i].disabled=!!reason&&!session.extras[key];inputs[i].closest('label').title=reason;});
    const preview=ui.extras.querySelector('select');const reason=availability('preview');preview.disabled=!!reason&&!extraActive(session.extras,'preview');preview.closest('label').title=reason;
    const add=ui.buttons.querySelector('.pe-add-link');add.disabled=!!availability('buttons')||standaloneExtra(session.extras)||session.extras.buttons.length>=8;add.title=availability('buttons');
    for(const b of ui.cards.querySelectorAll('[data-extra-card]')){
        const key=b.dataset.extraCard;const reason=availability(key);
        b.disabled=!session.extras[key]&&(!!reason||standaloneExtra(session.extras));b.title=reason;
    }
}

const HASHTAG_MODE_NOTE = { rubric: 'только рубрика', full: 'рубрика, имя и формат' };

/** Режим хэштегов выбранных каналов: самый полный из них; off — строки нет. */
const tagMode = o => (o.targets.some(t => o.picked.includes(t.value) && t.hashtags === 'full') ? 'full'
    : o.targets.some(t => o.picked.includes(t.value) && t.hashtags === 'rubric') ? 'rubric' : 'off');

/** Строка хэштегов под текстом: чипы с ✕ и поле «+ тег» с подсказками словаря. Теги допишет сервер последней строкой. */
function renderTags() {
    const mode = tagMode(session.options);
    ui.tags.hidden = mode === 'off' || Boolean(session.poll);
    if (ui.tags.hidden) return;
    const tagKey = JSON.stringify([mode, session.tags]);
    if (session.renderedTags === tagKey) return;
    session.renderedTags = tagKey;
    const input = el('input', { type: 'text', className: 'pe-tag-input', placeholder: '+ тег', maxLength: 17, autocomplete: 'off', ariaLabel: 'Добавить хэштег' });
    const hints = el('div', { className: 'pe-tag-hints' });
    const add = tag => {
        const clean = `#${String(tag).replace(/^#+/, '').trim().toLowerCase().replaceAll('ё', 'е')}`;
        if (clean.length > 1 && !session.tags.includes(clean) && session.tags.length < 3) session.tags.push(clean);
        render();
    };
    let timer = 0;
    input.oninput = () => {
        clearTimeout(timer);
        const q = input.value.replace(/^#+/, '').trim();
        if (!q) { hints.replaceChildren(); return; }
        timer = setTimeout(async () => {
            try {
                const { tags } = await api('hashtag_suggest', { params: { q } });
                hints.replaceChildren(...tags.map(t => button('pe-tag-hint', `#${t.tag}`, () => add(t.tag))));
            } catch { hints.replaceChildren(); }
        }, 200);
    };
    input.onkeydown = e => { if (e.key === 'Enter') { e.preventDefault(); add(input.value); } };
    ui.tags.replaceChildren(
        el('span', { className: 'pe-tags-label', textContent: 'Хэштеги', title: `В канал: ${HASHTAG_MODE_NOTE[mode]}. Допишутся последней строкой; если подпись не влезает в лимит, лишние снимутся с конца.` }),
        ...session.tags.map(tag => el('span', { className: 'pe-tag' }, tag,
            button('pe-tag-del', '✕', () => { session.tags = session.tags.filter(t => t !== tag); render(); }, { ariaLabel: `Убрать ${tag}` }))),
        session.tags.length < 3 ? input : null, hints);
}

/** Подпись выбранных ресурсов: имя одного или «N из M». */
const targetLabel = o => {
    const picked = o.targets.filter(t => o.picked.includes(t.value));
    return picked.length === 1 ? picked[0].label : `${picked.length} из ${o.targets.length}`;
};

/** Чип «Куда»: один ресурс — подпись; несколько — меню с галочками, отметить можно любые, хотя бы один остаётся. */
function targetChip(o) {
    if (!o.targets.length) return chip('Куда', 'Нет ресурсов');
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
    if (o.picked.some(v => !v.startsWith('tg:')) && o.mode === 'native') o.mode = 'queue';
    if (o.mode === 'slot' && !o.picked.every(v => o.schedule?.slots(v))) o.mode = 'queue';
    ui.targetLabel.textContent = `${targetLabel(o)} ▾`;
    const whenText = ui.chips.querySelector('.pe-chip-when strong');
    if (whenText) whenText.textContent = whenLabel(o);
    render();
    renderStyle();
}

/** Опрос вместо поста: медиа и текст скрываются, открываются вопрос и варианты. Через «Отложенные» Telegram опрос не ставится. */
function togglePoll() {
    const o = session.options;
    if (!session.poll && !currentCapabilities().poll) return;
    session.poll = session.poll ? null : { question: '', options: ['', ''], multiple:false };
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
    ui.multiple.checked = Boolean(poll.multiple);
    ui.quiz.checked=!!poll.quiz;
    ui.publicPoll.checked=!!poll.public;
    ui.closePoll.value=String(poll.close_minutes||0);
    ui.explanation.value=poll.explanation||'';
    ui.duration.value = String(poll.duration_minutes || 1440);
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
function renderPollState(poll, blocked) {
    const filled = poll.options.map(v => v.trim()).filter(Boolean);
    const ready = poll.question.trim() !== '' && filled.length >= 2;
    ui.submit.disabled = !ready || blocked || session.busy;
    ui.warning.hidden = ready && !blocked;
    ui.warning.textContent = blocked ? 'Проверьте ограничения опроса для выбранных площадок.' : 'Напишите вопрос и минимум два варианта ответа.';
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
    const mode = o.mode === 'native' || o.mode === 'queue' ? o.mode : 'queue';
    setWhen(o, { mode, scheduledTs: Math.floor(chosen.getTime() / 1000), scheduledAt: formatWhen(chosen) });
}

/** Ближайший свободный слот: у каждого канала свой, сервер выберет при публикации; здесь показываем слот первого канала с расписанием. */
async function pickSlot(o) {
    if (!o.picked.every(v => o.schedule.slots(v))) { ui.note.textContent = 'Расписание нужно настроить для каждого выбранного ресурса.'; return; }
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
        ...(o.scheduledTs && o.schedule?.native && o.picked.every(v => v.startsWith('tg:')) && o.mode !== 'slot' && !session.poll && !botOnlyMedia() && !hasExtras() ? [
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
    ui.chips.replaceChildren(targetChip(o), o.editing ? chip('Когда', whenLabel(o)) : whenChip(o), pollChip());
    ui.title.textContent = o.editing ? 'Редактирование поста' : o.scheduledTs ? 'Отложенный пост' : 'Новый пост';
    ui.submit.textContent = o.editing ? 'Сохранить' : o.scheduledTs ? 'Запланировать' : 'Запостить';
}

async function submit(e) {
    e.preventDefault();
    if (!session || ui.submit.disabled) return;
    const { options, media } = session;
    session.busy = true;
    ui.chips.inert = true; ui.form.querySelector('.pe-body').inert = true;
    ui.close.disabled = true; ui.form.querySelector('.pe-actions .pe-btn-ghost').disabled = true;
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
            media: poll || standaloneExtra(session.extras) ? [] : media.map(({ kind, src, name, file, existing }) => ({ kind, src, name, file, existing })), text: poll || standaloneExtra(session.extras) ? '' : ui.text.value.trim(),
            poll: poll ? {...clonePoll(poll), question:poll.question.trim(),options:poll.options.map(v=>v.trim()).filter(Boolean)} : null,
            hashtags: poll || standaloneExtra(session.extras) || ui.tags.hidden ? [] : [...session.tags], extras:poll ? null : cloneExtras(session.extras) };
        await options.onSubmit(post, onProgress);
        drafts.delete(options.key);
        session.busy = false;
        closeEditor();
    } catch (error) {
        if (session) { if (error.failedTargets?.length) { options.picked = error.failedTargets; renderWhen(); render(); } ui.warning.hidden = false; ui.warning.textContent = error.message || String(error); }
    } finally {
        ui.submit.textContent = label;
        ui.cancel.disabled = false;
        ui.progress.hidden = true;
        ui.chips.inert = false; ui.form.querySelector('.pe-body').inert = false;
        ui.close.disabled = false; ui.form.querySelector('.pe-actions .pe-btn-ghost').disabled = false;
        if (session) { session.busy = false; const message = ui.warning.textContent; render(); ui.warning.hidden = false; ui.warning.textContent = message; }
    }
}

function closeEditor() {
    if (!session || session.busy) return;
    ui.overlay.hidden = true;
    document.body.classList.remove('post-editor-open');
    const onClose = session.options.onClose;
    session = null;
    onClose?.();
}

/** options: { key, targets: [{value, label, hashtags}], picked: [value], hashtags: ['#рубрика', …], scheduledAt, scheduledTs, media: [{kind, src, thumb, name}], original, description, schedule, onSubmit(post) → Promise }.
 *  post.targets — значения выбранных ресурсов; post.poll — {question, options} вместо текста и медиа. */
export function openPostEditor(options) {
    ui ||= buildUi();
    options.mode ??= options.scheduledTs ? 'queue' : 'now';
    options.picked = [...options.picked];
    const draft = options.savedDraft || drafts.get(options.key);
    session = { options, media: (draft ? draft.media : options.media).map(m => ({ ...m })), poll: clonePoll(draft && Object.hasOwn(draft,'poll') ? draft.poll : options.poll), extras:cloneExtras(draft && Object.hasOwn(draft,'extras') ? draft.extras : options.extras), dragFrom: null, tags: [...(draft?.tags || options.hashtags || [])] };
    if (session.poll && options.mode === 'native') options.mode = 'queue';
    renderPoll();
    renderExtras();
    renderWhen();
    ui.text.value = draft ? draft.text : (options.original || '');
    ui.note.textContent = ui.url.value = '';
    ui.aiNote.textContent = '';
    ui.overlay.hidden = false;
    document.body.classList.add('post-editor-open');
    render();
    renderStyle();
    if (session.poll) ui.question.focus();
    else { ui.text.focus(); ui.text.setSelectionRange(ui.text.value.length, ui.text.value.length); }
}
