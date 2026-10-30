// studio.js — главная «Студии»: смотровое окно (глобус с только что отобранными постами), лента «Мой контент», источники, события.
// Данные — тот же API, что у приложения.
import { createCity } from './studio-city.js';
import { showPlan, showProfile } from './profile.js';

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

/** Запрос к API; без входа — на страницу входа приложения. */
async function apiOrLogin(action, opts) {
    try { return await api(action, opts); }
    catch (e) { if (e.status === 401) location.replace('index.html'); throw e; }
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

const PLATFORM_COLORS = { Telegram: '#4fd1ff', YouTube: '#ff5a7a', 'ВКонтакте': '#6f9bff', 'Двач': '#ffd166', '4chan': '#9be15d', RSS: '#c86bff', 'Сайт': '#c86bff' };
const colorOf = p => PLATFORM_COLORS[p] || '#e06bff';

const firstLine = i => String(i.text || i.description || '').split('\n')[0].replace(/#\S+/g, '').trim().slice(0, 90) || 'Без подписи';

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
    const title = firstLine(i);
    return el('article', { className: 'tile', onclick: e => { if (!e.target.closest('a')) openPreview(i); } },
        el('div', { className: 'tile-media' }, media,
            el('span', { className: 'badge', textContent: `${PLATFORMS[platformOf(i)] || '◌'} ${platformOf(i)}` }),
            ago ? el('span', { className: 'ago', textContent: ago }) : null, stats),
        el('div', { className: 'tile-body' },
            el('b', { textContent: title }),
            el('small', { textContent: (i.hashtags || []).slice(0, 4).join(' ') || i.source || '' }),
            i.source_url ? el('a', { href: i.source_url, target: '_blank', rel: 'noopener', textContent: '↗', title: 'Открыть у источника' }) : null));
}

// ---- просмотр поста ----
function openPreview(i) {
    const dlg = $('preview');
    const media = i.display_video
        ? el('video', { src: i.display_video, poster: i.display_image || '', controls: true, autoplay: true, playsInline: true })
        : i.display_image ? el('img', { src: i.display_image, alt: '' }) : null;
    const p = platformOf(i);
    dlg.replaceChildren(
        el('button', { type: 'button', className: 'preview-close', textContent: '×', ariaLabel: 'Закрыть', onclick: () => dlg.close() }),
        media ? el('div', { className: 'preview-media' }, media) : null,
        el('div', { className: 'preview-body' },
            el('p', { className: 'preview-meta' }, el('span', { className: 'badge-inline', style: `--c:${colorOf(p)}`, textContent: `${PLATFORMS[p] || '◌'} ${p}` }),
                el('span', { textContent: String(i.source || '').split(' | ').pop() }),
                i.relevance ? el('span', { textContent: `★ ${i.relevance}` }) : null,
                i.timestamp ? el('span', { className: 'muted', textContent: formatAgo(i.timestamp) }) : null),
            el('p', { className: 'preview-text', textContent: String(i.text || i.description || '').slice(0, 1200) }),
            i.why_relevant ? el('p', { className: 'preview-why' }, el('b', { textContent: 'Почему отобран: ' }), i.why_relevant) : null,
            el('div', { className: 'preview-actions' },
                i.source_url ? el('a', { className: 'ghost', href: i.source_url, target: '_blank', rel: 'noopener', textContent: 'У источника ↗' }) : null,
                el('a', { className: 'cta', href: 'index.html#content', textContent: 'Открыть в «Подборе»' }))));
    dlg.onclose = () => dlg.querySelector('video')?.pause();
    dlg.showModal();
}

// ---- смотровое окно ----
const live = { timer: 0 };

// строка «Только что отобрано» в правой панели: миниатюра, первая строка текста, источник · балл
function feedItem(i) {
    const open = () => openPreview(i);
    return el('li', { tabIndex: 0, title: firstLine(i), onclick: open, onkeydown: e => { if (e.key === 'Enter') open(); } },
        el('span', { className: 'th' }, i.display_image ? el('img', { src: i.display_image, alt: '', loading: 'lazy' }) : null),
        el('span', {}, el('span', { className: 'fline', textContent: firstLine(i) }),
            el('small', { textContent: `${String(i.source || '').split(' | ').pop()}${i.relevance ? ` · ★${i.relevance}` : ''}` })));
}

const cleanLog = l => l.replace(/[╔╗╚╝║═─│┌┐└┘]+/g, ' ').replace(/\s+/g, ' ').trim();

/** Журнал: новые строки дописываются, перерисовки целиком нет (мигания не будет). */
function renderJournal(lines) {
    $('journal').replaceChildren(...lines.map(t => el('div', { className: 'ln', textContent: t })));
}

// ---- город и тепловая карта (studio_city) ----
const city = { view: null, src: 'pool', data: null, cells: [], hot: -1, idle: '', running: false };
const dayFmt = new Intl.DateTimeFormat('ru-RU', { day: 'numeric', month: 'long', weekday: 'short' });
const plural = v => v % 10 === 1 && v % 100 !== 11 ? 'пост' : v % 10 > 1 && v % 10 < 5 && (v % 100 < 10 || v % 100 > 20) ? 'поста' : 'постов';
const level = v => v === 0 ? 0 : v < 3 ? 1 : v < 5 ? 2 : v < 7 ? 3 : 4;
const valuesOf = () => (city.src === 'pub' ? city.data.published : city.data.pool);
const dayText = k => {
    const d = new Date(city.start.getFullYear(), city.start.getMonth(), city.start.getDate() + k), v = valuesOf()[k];
    return `${dayFmt.format(d)} — ${v} ${plural(v)}`;
};

async function loadCity() {
    let c;
    try { c = await apiOrLogin('studio_city'); } catch { return; }
    city.data = c;
    const [y, m, d] = c.start.split('-').map(Number);
    city.start = new Date(y, m - 1, d);
    city.cells = c.pool.map((_, k) => {
        const i = el('i', { onmouseenter: () => setHot(k) });
        return i;
    });
    $('heat').replaceChildren(...city.cells);
    $('heat').onmouseleave = () => setHot(-1);
    city.view?.setData(c.pool, c.published);
    paintHeat();
}

function paintHeat() {
    if (!city.data) return;
    const vals = valuesOf();
    city.cells.forEach((c, k) => { c.style.background = `var(--heat${level(vals[k])})`; });
    const total = vals.reduce((a, v) => a + v, 0).toLocaleString('ru-RU');
    city.idle = `${total} ${city.src === 'pool' ? 'в пул' : 'опубликовано'} за год`;
    let streak = 0;
    for (let k = vals.length - 1; k >= 0 && vals[k] > 0; k--) streak++;
    $('heatStreak').textContent = String(streak);
    markHeat(city.hot);
}

/** Подсветка дня: клетка тепловой карты и подпись над ней. */
function markHeat(k) {
    city.hot = k;
    city.cells.forEach((c, j) => c.classList.toggle('on', j === k));
    $('heatTip').textContent = k >= 0 && city.data ? dayText(k) : city.idle;
}

/** Наведение на клетку подсвечивает дом в городе; и обратно — дом подсвечивает клетку. */
function setHot(k) {
    markHeat(k);
    city.view?.highlight(k);
}

function onCityHover(k, x, y) {
    const tip = $('cityTip');
    markHeat(k);
    tip.hidden = k < 0;
    if (k >= 0) { tip.textContent = dayText(k); tip.style.left = `${x}px`; tip.style.top = `${y}px`; }
}

async function loadLive() {
    clearTimeout(live.timer);
    let r;
    try { r = await apiOrLogin('studio_live'); } catch { live.timer = setTimeout(loadLive, 15000); return; }
    const items = r.items || [];

    $('win-topic').textContent = r.topic || 'контент';
    $('live-label').textContent = r.running ? 'В эфире · идёт подбор' : 'Сборщик отдыхает';
    $('kicker').classList.toggle('is-on', !!r.running);
    $('ai-phase').textContent = r.running ? (r.phase || 'Работает') : (items.length ? 'Подборка ждёт ваших решений' : 'Пока ничего: нажмите «Найти контент»');
    $('stat-pool').textContent = String(r.pool ?? items.length);
    $('stat-added').textContent = r.running ? String(r.progress?.added ?? 0) : '—';
    const hour = items.filter(i => i.timestamp > Date.now() / 1000 - 3600).length;
    $('stat-hour').textContent = hour >= items.length && items.length >= 30 ? '30+' : String(hour);
    $('nav-pool').textContent = r.pool || '';
    $('act-search').disabled = !!r.running;
    $('act-search').textContent = r.running ? '✦ Идёт поиск…' : '✦ Найти контент';
    $('model-pill').classList.toggle('is-busy', !!r.running);
    // проход закончился — город пересчитывается по новым постам
    if (city.running && !r.running) loadCity();
    city.running = !!r.running;

    renderJournal((r.activity || []).map(cleanLog).filter(l => l.length > 3).slice(-8));
    $('feed').replaceChildren(...(items.length ? items.slice(0, 5).map(feedItem)
        : [el('li', { className: 'muted', textContent: 'Пока ничего: подборка пуста.' })]));
    live.timer = setTimeout(loadLive, r.running ? 4000 : 15000);
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
    const [proposals, info] = await Promise.all([apiOrLogin('proposals'), api('account_info')]);
    state.items = proposals.items || [];
    $('nav-proposals').textContent = state.items.length || '';
    showProfile(info);
    render();
}

async function loadSources() {
    const r = await api('topic_sources');
    const counts = {};
    for (const s of r.sources || []) counts[s.platform] = (counts[s.platform] || 0) + 1;
    $('side-sources').replaceChildren(...(Object.keys(counts).length ? Object.entries(counts).sort((a, b) => b[1] - a[1]).map(([p, n]) =>
        el('li', {}, el('i', { textContent: PLATFORMS[p] || '◌' }), el('span', { textContent: p }), el('em', { textContent: String(n) })))
        : [el('li', { className: 'muted', textContent: 'Источников темы пока нет' })]));
}

async function loadModel() {
    const m = await api('model_status');
    $('pill-model').textContent = m.roles?.analysis?.split('/').pop() || 'Модели';
    $('pill-state').textContent = m.ollama ? `Ollama ${m.version || ''} · набор «${m.llm?.preset || ''}»` : 'Ollama не отвечает';
}

async function action(name, done) {
    $('act-note').textContent = '…';
    try { const r = await api(name, { body: {} }); $('act-note').textContent = r.message || done; setTimeout(loadLive, 800); }
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
    city.view = createCity($('sky'), { onHover: onCityHover });
    if (!city.view) $('glfail').hidden = false;
    document.querySelectorAll('.datasrc button').forEach(b => b.addEventListener('click', () => {
        document.querySelectorAll('.datasrc button').forEach(x => x.setAttribute('aria-pressed', String(x === b)));
        city.src = b.dataset.src;
        city.view?.setSource(city.src);
        paintHeat();
    }));
    loadCity();
    loadLive();
    loadModel().catch(() => {});
    api('billing_info').then(showPlan).catch(() => {});
    $('preview').addEventListener('click', e => { if (e.target === $('preview')) $('preview').close(); });
}

init();
