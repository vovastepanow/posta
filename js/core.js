// core.js — общее: DOM-помощники, журнал, запросы к API, опрос парсера, вкладки и привязка процессов к вкладке.

export const DEBUG_MODE = new URLSearchParams(location.search).get('debug') === '1';
export const $ = id => document.getElementById(id);
const node = target => (typeof target === 'string' ? $(target) : target);
export const show = (target, visible = true) => { node(target).hidden = !visible; };
export const hide = target => show(target, false);
/** Текст в блоке статуса или ошибки; пустой текст прячет блок. */
export function setMessage(target, text) { const n = node(target); n.textContent = text || ''; n.hidden = !text; }

/** el('div', { className: 'x' }, child1, child2) */
export function el(tag, props = {}, ...children) {
    const element = Object.assign(document.createElement(tag), props);
    element.append(...children.filter(child => child !== null && child !== undefined && child !== false));
    return element;
}

export const COUNT = new Intl.NumberFormat('ru-RU', { notation: 'compact', maximumFractionDigits: 1 });
export const chip = (label, value, title = '', className = '') => el('span', { className: `eng-chip ${className}`.trim(), title }, `${label} `, el('b', { textContent: value }));

/** Метки записи от ИИ (tags_info с сервера) в контейнер; нет меток — контейнер скрыт. */
export function renderTags(container, tags) {
    container.replaceChildren(...(tags || []).map(t => el('span', { className: `tag-chip${t.nsfw ? ' is-nsfw' : ''}`, title: t.path, textContent: t.title })));
    container.hidden = !tags?.length;
}
export const VIDEO_ICON = '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="2" y="6" width="13" height="12" rx="3" fill="currentColor"/><path d="M16.5 10.2 21 7.6v8.8l-4.5-2.6z" fill="currentColor"/></svg>';
export const NO_TEXT = ['Текст к посту отсутствует', 'Пост без текста'];

/** Ссылка на исходный пост — только HTTPS и известные площадки (та же проверка, что на сервере). */
export function safeSourcePostUrl(record) {
    try {
        const url = new URL(record?.source_url);
        if (url.protocol !== 'https:') return '';
        // RSS: адрес уже проверен сервером (source_post_url) — хост совпадает с хостом ленты
        return String(record?.source_key || '').startsWith('rss:') || ['2ch.hk', 't.me', 'vk.com'].includes(url.hostname) ? url.href : '';
    } catch { return ''; }
}

export function pluralRu(n, one, few, many) {
    const [m10, m100] = [n % 10, n % 100];
    return m100 >= 11 && m100 <= 14 ? many : m10 === 1 ? one : m10 >= 2 && m10 <= 4 ? few : many;
}

/** «только что», «5 мин назад», «3 ч назад», «2 дн назад». */
export function formatAgo(seconds) {
    const minutes = Math.max(0, Math.round((Date.now() / 1000 - seconds) / 60));
    if (minutes < 1) return 'только что';
    if (minutes < 60) return `${minutes} мин назад`;
    const hours = Math.round(minutes / 60);
    return hours < 48 ? `${hours} ч назад` : `${Math.round(hours / 24)} дн назад`;
}

/** Выпадающее меню у кнопки: клик по пункту → onPick(пункт), закрывается кликом мимо и Esc. */
export function bindMenu(button, menu, onPick) {
    const setOpen = open => { menu.hidden = !open; button.setAttribute('aria-expanded', String(open)); };
    button.addEventListener('click', event => { event.stopPropagation(); setOpen(menu.hidden); });
    menu.addEventListener('click', event => {
        const item = event.target.closest('button');
        if (item) { setOpen(false); onPick(item); }
    });
    document.addEventListener('click', event => { if (!menu.hidden && !menu.parentElement.contains(event.target)) setOpen(false); });
    document.addEventListener('keydown', event => { if (event.key === 'Escape') setOpen(false); });
    return setOpen;
}

// ---- Текущая тема поиска: одно место на всё приложение ----
// Сама настройка живёт на сервере (settings.topic); здесь — её последняя копия. Записывает только refreshTopicLabel,
// остальные (подбор, «Предложка», «Дерево», «Базы данных») читают currentTopic() и перерисовываются по onTopicChange.
const topicState = { topic: '', topic2: '' };
const topicListeners = new Set();
export const currentTopic = () => ({ ...topicState });
export const onTopicChange = fn => topicListeners.add(fn);
export function setCurrentTopic(topic, topic2 = '') {
    if (topic === topicState.topic && topic2 === topicState.topic2) return;
    const first = topicState.topic === '';
    Object.assign(topicState, { topic, topic2 });
    if (!first) topicListeners.forEach(fn => fn(currentTopic()));   // первая загрузка — не смена
}

// ---- Журнал ----
// Строки обмена с моделью: «[ИИ → модель] запрос», «[ИИ ← модель] ответ», «[ИИ мысли модель] …», «[ИИ мысли+ модель] продолжение».
const AI_LINE = /^\[ИИ (→|←|мысли\+?) ([^\]]+)\] ?([\s\S]*)$/;
const AI_KIND = { '→': ['запрос →', 'ai-request'], '←': ['ответ ←', 'ai-response'], 'мысли': ['размышления', 'ai-thinking'] };
// Ссылки: «[текст](url)», голые адреса, доски/треды/посты Двача, @каналы, паблики VK, «/b/» в строках про Двач
const LINK = new RegExp([
    String.raw`\[(?<mdText>[^\]\n]+)\]\((?<mdUrl>https?:\/\/[^\s)]+)\)`,
    String.raw`(?<url>https?:\/\/[^\s<>"«»]+)`,
    String.raw`(?<dvach>Двача? )\/(?<board>[a-z0-9]{1,10})\/(?: · тред #(?<thread>\d+)(?: · пост #(?<post>\d+))?)?`,
    String.raw`(?<![\w@/])@(?<tg>[A-Za-z][A-Za-z0-9_]{4,31})\b`,
    String.raw`(?<vkPrefix>Парсинг ВКонтакте )(?<vk>[A-Za-z0-9_.]{2,64}?)(?=\.\.\.|[\s,]|$)`,
    String.raw`(?<=^|[\s,(:])\/(?<boardOnly>[a-z0-9]{1,10})\/(?=[\s,.;)]|$)`
].join('|'), 'gu');
const console_ = { lines: 0, parserLines: [], waitingShown: false, followsTail: true, live: null };

const logLink = (href, textContent) => el('a', { className: 'console-link', href, textContent, target: '_blank', rel: 'noopener noreferrer', onclick: e => e.stopPropagation() });

function linkify(text) {
    const nodes = [];
    let last = 0;
    for (const m of text.matchAll(LINK)) {
        const g = m.groups;
        if (g.boardOnly && !text.includes('Двач')) continue;
        nodes.push(text.slice(last, m.index));
        last = m.index + m[0].length;
        const board = `https://2ch.hk/${g.board || g.boardOnly}/`;
        if (g.mdUrl) nodes.push(logLink(g.mdUrl, `[${g.mdText}]`));
        else if (g.url) { const url = g.url.replace(/[.,;:!?)\]]+$/, ''); nodes.push(logLink(url, url), g.url.slice(url.length)); }
        else if (g.board) {
            nodes.push(g.dvach, logLink(board, `/${g.board}/`));
            if (g.thread) nodes.push(' · тред ', logLink(`${board}res/${g.thread}.html`, `#${g.thread}`));
            if (g.post) nodes.push(' · пост ', logLink(`${board}res/${g.thread}.html#${g.post}`, `#${g.post}`));
        } else if (g.tg) nodes.push(logLink(`https://t.me/${g.tg}`, `@${g.tg}`));
        else if (g.vk) nodes.push(g.vkPrefix, logLink(`https://vk.com/${g.vk}`, g.vk));
        else nodes.push(logLink(board, `/${g.boardOnly}/`));
    }
    nodes.push(text.slice(last));
    return nodes.filter(n => n !== '');
}

function finishLiveThinking() { console_.live?.classList.remove('is-live'); console_.live = null; }

function appendConsole(entry) {
    const track = $('activity-track');
    track.append(entry);
    for (console_.lines++; console_.lines > 200 && track.firstElementChild !== console_.live; console_.lines--) track.firstElementChild.remove();
    if (console_.followsTail) track.scrollTop = track.scrollHeight;
}

export function log(message) {
    const time = new Date().toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    const ai = AI_LINE.exec(message);
    if (!ai) return appendConsole(el('p', { className: 'activity-console-line' }, el('span', { className: 'console-time', textContent: time }), ' ', ...linkify(message)));
    const [, direction, model, text] = ai;
    if (direction === 'мысли+' && console_.live) {
        console_.live.querySelector('.ai-message-text').append(` ${text}`);
        if (console_.followsTail) $('activity-track').scrollTop = $('activity-track').scrollHeight;
        return;
    }
    if (direction !== 'мысли+') finishLiveThinking();
    const [label, cls] = AI_KIND[direction.replace('+', '')];
    const entry = el('p', { className: `activity-console-line ai-message ${cls}` },
        el('span', { className: 'ai-message-meta', textContent: `${time} · ${label} ${model}` }), el('span', { className: 'ai-message-text' }, ...linkify(text)));
    entry.addEventListener('click', () => entry.classList.toggle('is-expanded'));
    if (direction.startsWith('мысли')) { entry.classList.add('is-live'); console_.live = entry; }
    appendConsole(entry);
}

/** Сервер присылает хвост журнала парсера целиком: выводим только новые строки (по перекрытию с прошлой порцией). */
function appendParserActivity(activity) {
    const incoming = (activity || []).filter(line => typeof line === 'string' && line.trim());
    const previous = console_.parserLines;
    let overlap = Math.min(previous.length, incoming.length);
    while (overlap > 0 && !previous.slice(-overlap).every((line, i) => line === incoming[i])) overlap--;
    console_.parserLines = incoming;
    const fresh = incoming.slice(overlap);
    if (!previous.length && !incoming.length && !console_.waitingShown) { console_.waitingShown = true; fresh.push('Запуск парсера и загрузка источников...'); }
    fresh.forEach(log);
}

function initializeConsole() {
    const [track, follow] = [$('activity-track'), $('activity-console-follow')];
    const update = () => { console_.followsTail = track.scrollHeight - track.scrollTop - track.clientHeight <= 24; follow.hidden = console_.followsTail; };
    track.addEventListener('scroll', update, { passive: true });
    follow.addEventListener('click', () => { console_.followsTail = true; track.scrollTop = track.scrollHeight; follow.hidden = true; });
    update();
}

// ---- API ----

const ACTION_LABELS = {
    telegram_publish: 'Отправка поста в Telegram…', refresh_content: 'Запущен поиск контента', save_topic: 'Сохранение темы поиска', save_filters: 'Сохранение фильтров подбора',
    topic_parent: 'Сохранение общей темы', topic_reprofile: 'Пересоставление профиля темы', control_search: 'Поиск контента', control_sources: 'Обновление базы источников',
    app_stop: 'Остановка приложения', analyze_image: 'Запущен анализ изображения', suggest_sources: 'Поиск нового источника', vote: 'Сохранение оценки',
    source_vote: 'Сохранение решения по источнику', database_add: 'Добавление записи в базу', database_update: 'Обновление записи в базе', database_delete: 'Удаление записи из базы',
    database_read: 'Загрузка записей базы', database_options: 'Загрузка списка баз', database_reset: 'Очистка всех баз', discover_sources: 'Запуск автоподбора источников', debug_reset: 'Сброс демо-данных'
};

/** Идентификатор этой вкладки (новый при каждой загрузке): к нему привязаны фоновые процессы. */
export const TAB_ID = Array.from(crypto.getRandomValues(new Uint8Array(16)), b => b.toString(16).padStart(2, '0')).join('');

export function apiUrl(action, params = {}) {
    const query = new URLSearchParams({ action, ...params });
    if (DEBUG_MODE) query.set('debug', '1');
    return `index.php?${query}`;
}

/** Запрос к API. body: объект (JSON), FormData или null (пустой POST); без body — GET. Ошибка HTTP — исключение с текстом сервера. */
export async function api(action, { body, params } = {}) {
    if (ACTION_LABELS[action]) log(ACTION_LABELS[action]);
    const options = { method: body === undefined ? 'GET' : 'POST', headers: {} };
    if (options.method === 'POST') {
        options.headers['X-Posta'] = '1';
        if (body instanceof FormData) options.body = body;
        else if (body !== null) { options.headers['Content-Type'] = 'application/json'; options.body = JSON.stringify(body); }
    }
    const response = await fetch(apiUrl(action, params), options);
    const data = await response.json().catch(() => null);
    (data?.ai_log || []).forEach(log);
    if (!response.ok) throw Object.assign(new Error(data?.message || `HTTP ${response.status}`), { status: response.status, data });
    if (data === null) throw new Error('Сервер вернул некорректный ответ.');
    return data;
}

// ---- Опрос парсера: часто, пока работает (размышления идут «вживую»), иначе редко ----

const parser = { timer: null, running: false, busy: false, listeners: [], statusListeners: [] };
export const onParserStateChange = listener => parser.listeners.push(listener);
/** Каждый ответ parser_status (running, phase, runtime, progress) — без отдельного опроса. */
export const onParserStatus = listener => parser.statusListeners.push(listener);

async function pollParser() {
    clearTimeout(parser.timer);
    if (parser.busy) return;
    parser.busy = true;
    let running = parser.running;
    let phase = 'ничего не делается';
    let runtime = 0;
    try {
        const data = await api('parser_status');
        running = Boolean(data.running);
        phase = data.phase || (running ? 'парсер работает' : 'ничего не делается');
        runtime = data.runtime || 0;
        if (running && !parser.running) { console_.parserLines = []; console_.waitingShown = false; }
        if (running || data.activity?.length) appendParserActivity(data.activity);
        parser.statusListeners.forEach(listener => listener(data));
    } catch { /* сервер временно недоступен */ }
    if (running !== parser.running) {
        parser.running = running;
        if (!running) finishLiveThinking();
        parser.listeners.forEach(listener => listener(running));
    }
    // Показываем статус парсера с временем работы если оно есть
    let statusText = running ? phase : 'ничего не делается';
    if (running && runtime > 0) {
        const runtimeText = runtime < 60 ? `${Math.floor(runtime)}с` : `${Math.floor(runtime / 60)}м ${runtime % 60}с`;
        statusText = `${phase} (${runtimeText})`;
    }
    $('activity-console-status').textContent = statusText;
    $('activity-console').classList.toggle('is-active', running);
    parser.busy = false;
    parser.timer = setTimeout(pollParser, running ? 2000 : 15000);
}

/** Запускает опрос; повторный вызов опрашивает сразу. */
export function watchParser() { if (!DEBUG_MODE) pollParser(); }

// ---- Вкладки ----

const tabHandlers = {};
export const onTabOpen = (tab, handler) => (tabHandlers[tab] ||= []).push(handler);
export const isTabActive = tab => $(tab).classList.contains('active');

export function switchTab(tab) {
    const button = document.querySelector(`.tab-btn[data-tab="${tab}"]`);
    if (!button || button.hidden) return;
    log(`Открыта вкладка: ${button.textContent.trim()}`);
    document.querySelectorAll('.view-panel').forEach(panel => panel.classList.toggle('active', panel.id === tab));
    document.querySelectorAll('.tab-btn[data-tab]').forEach(b => { b.classList.toggle('active', b === button); b.setAttribute('aria-selected', String(b === button)); });
    tabHandlers[tab]?.forEach(handler => handler());
}

// ---- Сессия вкладки: открытие занимает её (парсер не прерывается, а продолжает работу), сигнал раз в 5 с, закрытие освобождает ----

const session = { timer: null, inactive: false };

function setInactive(value) {
    session.inactive = value;
    document.body.classList.toggle('tab-inactive', value);
    show('tab-inactive', value);
    if (value) { log('Приложение открыто в другой вкладке: поиск и обновления идут там, эта вкладка неактивна.'); clearInterval(session.timer); }
}

async function claimSession() {
    setInactive(false);
    const data = await api('tab_open', { body: { tab: TAB_ID } });
    log(data.parser === 'started' ? 'Вкладка открыта: поиск запущен.' : 'Вкладка открыта: фоновые процессы продолжают работу.');
    clearInterval(session.timer);
    session.timer = setInterval(async () => {
        try { if (!(await api('tab_heartbeat', { body: { tab: TAB_ID } })).owner) setInactive(true); } catch { /* повторим со следующим сигналом */ }
    }, 5000);
}

/** Вызывать до первого запроса пула: он может запустить парсер. Парсер вкладкой не управляется: переключение и закрытие его не останавливают. */
export async function startTabSession() {
    if (DEBUG_MODE) return;
    $('tab-inactive-reload').addEventListener('click', () => location.reload());
    addEventListener('pagehide', () => {   // keepalive: запрос дойдёт и после закрытия; не дошёл — сервер остановит сам по таймауту
        if (!session.inactive) fetch(apiUrl('tab_close'), { method: 'POST', keepalive: true, headers: { 'Content-Type': 'application/json', 'X-Posta': '1' }, body: JSON.stringify({ tab: TAB_ID }) }).catch(() => {});
        clearInterval(session.timer);
    });
    addEventListener('pageshow', event => { if (event.persisted) claimSession().catch(() => {}); });
    await claimSession().catch(error => log(`Не удалось привязать процессы к вкладке: ${error.message}`));
}

export function initializeCore() {
    initializeConsole();
    document.querySelectorAll('.tab-btn[data-tab]').forEach(button => button.addEventListener('click', () => switchTab(button.dataset.tab)));
}
