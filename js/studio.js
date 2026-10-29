// studio.js — главная «Студии»: лента контента, источники, работа модели, быстрые действия. Данные — тот же API, что у приложения.
const $ = id => document.getElementById(id);
const COUNT = new Intl.NumberFormat('ru-RU', { notation: 'compact', maximumFractionDigits: 1 });

function el(tag, props = {}, ...children) {
    const node = Object.assign(document.createElement(tag), props);
    node.append(...children.filter(c => c !== null && c !== undefined && c !== false));
    return node;
}

/** «5 мин назад» по unix-времени. */
function formatAgo(ts) {
    const minutes = Math.max(0, Math.round((Date.now() / 1000 - ts) / 60));
    if (minutes < 1) return 'только что';
    if (minutes < 60) return `${minutes} мин назад`;
    const hours = Math.round(minutes / 60);
    return hours < 48 ? `${hours} ч назад` : `${Math.round(hours / 24)} дн назад`;
}

/** Запрос к API приложения (POST — с заголовком X-Posta). */
async function api(action, { body } = {}) {
    const opts = body === undefined ? {} : { method: 'POST', headers: { 'X-Posta': '1', 'Content-Type': 'application/json' }, body: JSON.stringify(body) };
    const r = await fetch(`index.php?action=${action}`, opts);
    const data = await r.json().catch(() => null);
    if (!r.ok) throw Object.assign(new Error(data?.message || `HTTP ${r.status}`), { status: r.status });
    return data;
}

const PLATFORMS = { YouTube: '▶', Telegram: '✈', 'ВКонтакте': 'VK', RSS: '◉', 'Двач': '2ch', '4chan': '4ch', 'Одноклассники': 'OK', TikTok: '♪' };
const state = { items: [], filter: 'all', sort: 'new', q: '' };

const platformOf = item => {
    const s = String(item.source || '');
    if (/youtu\.?be/.test(String(item.source_url || item.image || ''))) return 'YouTube';
    if (item.board && /^[a-z0-9]+$/.test(item.board)) return item.source_key?.startsWith('fourchan') ? '4chan' : 'Двач';
    const p = s.split(' | ')[0];
    return PLATFORMS[p] ? p : (s.includes('Двач') ? 'Двач' : 'Сайт');
};

const FILTERS = [
    ['all', 'Всё', () => true],
    ['images', 'Картинки', i => !i.display_video && !i.video],
    ['videos', 'Видео', i => !!(i.display_video || i.video)],
    ['text', 'Текст', i => !i.display_image],
    ['telegram', 'Из Telegram', i => platformOf(i) === 'Telegram'],
    ['boards', 'С имиджбордов', i => ['Двач', '4chan'].includes(platformOf(i))],
];

function visible() {
    const f = FILTERS.find(x => x[0] === state.filter)[2];
    const q = state.q.toLowerCase();
    const list = state.items.filter(i => f(i) && (!q || [i.text, i.source, ...(i.hashtags || [])].join(' ').toLowerCase().includes(q)));
    return list.sort(state.sort === 'rel' ? (a, b) => (b.relevance || 0) - (a.relevance || 0) : (a, b) => (b.timestamp || 0) - (a.timestamp || 0));
}

function card(i) {
    const media = i.display_video
        ? el('video', { src: i.display_video, poster: i.display_image || '', muted: true, loop: true, playsInline: true, preload: 'none',
            onmouseenter: e => e.target.play().catch(() => {}), onmouseleave: e => e.target.pause() })
        : i.display_image ? el('img', { src: i.display_image, alt: '', loading: 'lazy' }) : el('div', { className: 'text-only', textContent: (i.text || '').slice(0, 220) });
    const eng = i.engagement || {};
    const stats = el('div', { className: 'card-stats' },
        eng.views ? el('span', { textContent: `👁 ${COUNT.format(eng.views)}` }) : null,
        i.relevance ? el('span', { textContent: `★ ${i.relevance}`, title: 'Оценка модели: насколько пост по теме' }) : null,
        el('span', { className: 'vote', textContent: i.vote === 'liked' ? '♥' : '?', title: i.vote === 'liked' ? 'Лайк' : 'Без решения' }));
    const ago = i.timestamp ? formatAgo(i.timestamp) : '';
    const title = String(i.text || i.description || '').split('\n')[0].slice(0, 90) || 'Без подписи';
    return el('article', { className: 'tile' },
        el('div', { className: 'tile-media' }, media,
            el('span', { className: 'badge', textContent: `${PLATFORMS[platformOf(i)] || '◌'} ${platformOf(i)}` }),
            ago ? el('span', { className: 'ago', textContent: ago }) : null, stats),
        el('div', { className: 'tile-body' },
            el('b', { textContent: title }),
            el('small', { textContent: (i.hashtags || []).slice(0, 4).join(' ') || i.source || '' }),
            i.source_url ? el('a', { href: i.source_url, target: '_blank', rel: 'noopener', textContent: '↗', title: 'Открыть у источника' }) : null));
}

function render() {
    const list = visible();
    $('grid').replaceChildren(...(list.length ? list.map(card) : [el('p', { className: 'muted', textContent: 'Здесь пока пусто: лайкните посты в «Подборе» — они появятся в студии.' })]));
    $('chips').replaceChildren(...FILTERS.map(([id, label, f]) => el('button', {
        type: 'button', className: state.filter === id ? 'is-active' : '', onclick: () => { state.filter = id; render(); },
    }, label, el('em', { textContent: String(state.items.filter(f).length) }))));
    $('side-filters').replaceChildren(...FILTERS.slice(0, 4).map(([id, label, f]) => el('li', { className: state.filter === id ? 'is-active' : '', onclick: () => { state.filter = id; render(); } },
        el('span', { textContent: label }), el('em', { textContent: String(state.items.filter(f).length) }))));
}

async function loadContent() {
    const [proposals, info] = await Promise.all([api('proposals'), api('account_info')]);
    state.items = proposals.items || [];
    $('nav-proposals').textContent = state.items.length || '';
    $('nav-pool').textContent = info.pool || '';
    const c = info.counts || {};
    const total = Object.values(c).reduce((a, b) => a + b, 0);
    $('stat-total').textContent = COUNT.format(total);
    const voted = (c.liked || 0) + (c.disliked || 0) + (c.complaints || 0);
    $('stat-accuracy').textContent = voted ? `${Math.round(100 * (c.liked || 0) / voted)}%` : '—';
    const channel = info.channels?.[0];
    $('profile-name').textContent = info.auth?.tg_name || info.auth?.login || channel?.title || 'Владелец';
    $('profile-avatar').textContent = ($('profile-name').textContent[0] || 'P').toUpperCase();
    render();
}

async function loadSources() {
    const r = await api('topic_sources');
    const counts = {};
    for (const s of r.sources || []) counts[s.platform] = (counts[s.platform] || 0) + 1;
    $('ai-topic').textContent = `Тема: ${r.topic}`;
    $('side-sources').replaceChildren(...(Object.keys(counts).length ? Object.entries(counts).sort((a, b) => b[1] - a[1]).map(([p, n]) =>
        el('li', {}, el('i', { textContent: PLATFORMS[p] || '◌' }), el('span', { textContent: p }), el('em', { textContent: String(n) })))
        : [el('li', { className: 'muted', textContent: 'Источников темы пока нет' })]));
}

async function loadQueue() {
    const q = await api('telegram_queue');
    const day = Date.now() / 1000 - 86400;
    $('stat-today').textContent = String((q.published || []).filter(p => (p.at || 0) > day).length);
    const events = [
        ...(q.scheduled || []).slice(0, 3).map(p => ({ at: p.publish_at, title: p.status === 'failed' ? 'Пост не вышел' : 'Запланирован пост', sub: p.channel_title || p.text, kind: p.status === 'failed' ? 'bad' : 'plan' })),
        ...(q.published || []).slice(0, 4).map(p => ({ at: p.at, title: p.status === 'ok' ? 'Опубликовано' : 'Ошибка публикации', sub: p.channel_title || p.text, kind: p.status === 'ok' ? 'ok' : 'bad', url: p.url })),
    ].sort((a, b) => b.at - a.at).slice(0, 5);
    $('activity').replaceChildren(...(events.length ? events.map(e => el('li', { className: e.kind },
        el('i', { textContent: e.kind === 'ok' ? '✓' : e.kind === 'bad' ? '!' : '▦' }),
        el('span', {}, el('b', { textContent: e.title }), el('small', { textContent: `${e.sub || ''} · ${e.at > Date.now() / 1000 ? new Date(e.at * 1000).toLocaleString('ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }) : formatAgo(e.at)}` })),
        e.url ? el('a', { href: e.url, target: '_blank', rel: 'noopener', textContent: '›' }) : null)) : [el('li', { className: 'muted', textContent: 'Событий пока нет' })]));
}

const STEPS = [['сбор', 'Сбор постов из источников'], ['отсев', 'Быстрый отсев'], ['анализ', 'Анализ моделью'], ['подбор', 'Подбор источников']];

async function loadParser() {
    const [p, m] = await Promise.all([api('parser_status'), api('model_status').catch(() => null)]);
    const progress = p.progress || {};
    const pct = progress.total ? Math.round(100 * (progress.done || 0) / progress.total) : (p.running ? 10 : 0);
    $('ai-phase').textContent = p.running ? (p.phase || 'Работает') : 'Ничего не делается';
    $('ai-bar').style.width = `${pct}%`;
    $('ai-pct').textContent = p.running ? `${pct}%` : '';
    const phase = String(p.phase || '').toLowerCase();
    const at = STEPS.findIndex(([k]) => phase.includes(k));
    $('ai-steps').replaceChildren(...STEPS.map(([, label], i) => el('li', { className: p.running && (at === -1 ? i === 0 : i < at) ? 'done' : p.running && i === at ? 'now' : '' }, label)));
    if (m) {
        $('pill-model').textContent = m.roles?.analysis?.split('/').pop() || 'Модели';
        $('pill-state').textContent = p.running ? `${p.phase || 'работает'}${pct ? ` · ${pct}%` : ''}` : m.ollama ? `Ollama ${m.version || ''} · набор «${m.llm?.preset || ''}»` : 'Ollama не отвечает';
        $('model-pill').classList.toggle('is-busy', !!p.running);
    }
}

async function action(name, done) {
    $('act-note').textContent = '…';
    try { const r = await api(name, { body: {} }); $('act-note').textContent = r.message || done; loadParser(); }
    catch (e) { $('act-note').textContent = `Ошибка: ${e.message}`; }
}

function init() {
    $('sort').onchange = e => { state.sort = e.target.value; render(); };
    $('search').oninput = e => { state.q = e.target.value; render(); };
    document.addEventListener('keydown', e => { if (e.key === '/' && document.activeElement !== $('search')) { e.preventDefault(); $('search').focus(); } });
    $('act-search').onclick = () => action('refresh_content', 'Сборщик запущен.');
    $('act-discover').onclick = () => action('discover_sources', 'Автоподбор запущен.');
    const fail = e => { $('grid').replaceChildren(el('p', { className: 'muted', textContent: e.status === 401 ? 'Войдите в приложение, чтобы открыть студию.' : `Ошибка: ${e.message}` })); };
    loadContent().catch(fail);
    loadSources().catch(() => {});
    loadQueue().catch(() => {});
    loadParser().catch(() => {});
    api('billing_info').then(b => { $('profile-plan').textContent = `Тариф «${b.plan?.title || '—'}»`; }).catch(() => {});
    setInterval(() => loadParser().catch(() => {}), 5000);
}

init();
