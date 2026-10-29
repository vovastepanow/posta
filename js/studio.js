// studio.js — главная «Студии»: смотровое окно (глобус с только что отобранными постами), лента «Мой контент», источники, события.
// Данные — тот же API, что у приложения.
import { createGlobe } from './studio-globe.js';

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

// Регион источника: точной геометки у постов нет, поэтому — по площадке и языку текста; город выбирается стабильно по источнику.
const CITIES = {
    ru: [['Москва', 55.75, 37.62], ['Санкт-Петербург', 59.94, 30.31], ['Казань', 55.79, 49.12], ['Екатеринбург', 56.84, 60.6], ['Новосибирск', 55.03, 82.92],
        ['Краснодар', 45.04, 38.98], ['Нижний Новгород', 56.33, 44.0], ['Самара', 53.2, 50.15], ['Ростов-на-Дону', 47.23, 39.72], ['Владивосток', 43.12, 131.89],
        ['Минск', 53.9, 27.56], ['Алматы', 43.24, 76.89], ['Красноярск', 56.01, 92.85], ['Пермь', 58.01, 56.25]],
    boards: [['Москва', 55.75, 37.62], ['Санкт-Петербург', 59.94, 30.31]],
    us: [['Нью-Йорк', 40.71, -74.0], ['Лос-Анджелес', 34.05, -118.24], ['Чикаго', 41.88, -87.63], ['Сиэтл', 47.6, -122.33], ['Остин', 30.27, -97.74], ['Майами', 25.76, -80.19]],
    en: [['Нью-Йорк', 40.71, -74.0], ['Лос-Анджелес', 34.05, -118.24], ['Лондон', 51.5, -0.12], ['Торонто', 43.65, -79.38], ['Сидней', -33.87, 151.21], ['Дублин', 53.35, -6.26]],
    eu: [['Берлин', 52.52, 13.4], ['Париж', 48.86, 2.35], ['Мадрид', 40.42, -3.7], ['Рим', 41.9, 12.5], ['Варшава', 52.23, 21.01], ['Амстердам', 52.37, 4.9]],
    ja: [['Токио', 35.68, 139.69], ['Осака', 34.69, 135.5]],
    zh: [['Шанхай', 31.23, 121.47], ['Тайбэй', 25.03, 121.56]],
    ko: [['Сеул', 37.57, 126.98]],
};
const hash = s => { let h = 2166136261; for (const ch of String(s)) h = Math.imul(h ^ ch.codePointAt(0), 16777619); return h >>> 0; };

function regionOf(item, platform) {
    if (platform === '4chan') return 'us';
    if (platform === 'Двач') return 'boards';
    // ссылки и хэштеги (часто латиницей и у русских постов) язык не определяют
    const t = `${item.text || ''} ${String(item.source || '').split(' | ').pop()}`.replace(/https?:\/\/\S+|[\w.-]+\.(com|ru|tv|to|me|be)\S*|#\S+|@\S+/gi, ' ');
    if (/[\u3040-\u30ff]/.test(t)) return 'ja';
    if (/[\uac00-\ud7af]/.test(t)) return 'ko';
    if (/[\u4e00-\u9fff]/.test(t)) return 'zh';
    const cyr = (t.match(/[а-яё]/gi) || []).length, lat = (t.match(/[a-z]/gi) || []).length;
    if (cyr * 3 >= lat) return 'ru';
    return /[äöüßéèàñçãõąęłśźż]/i.test(t) ? 'eu' : 'en';
}

function geoOf(item) {
    const platform = platformOf(item);
    const list = CITIES[regionOf(item, platform)];
    const [city, lat, lon] = list[hash(item.source_key || item.source) % list.length];
    const j = hash(item.key || item.display_image || item.text);   // небольшой разброс, чтобы посты одного города не слипались
    return { city, lat: (lat + ((j & 255) / 255 - 0.5) * 1.2) * Math.PI / 180, lon: (lon + (((j >> 8) & 255) / 255 - 0.5) * 1.6) * Math.PI / 180, platform };
}

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
const live = { keys: null, timer: 0, globe: null };

function globeCard({ item, city, platform, color }) {
    return el('article', { style: `--c:${color}` },
        item.display_image ? el('img', { src: item.display_image, alt: '', loading: 'lazy' }) : el('p', { className: 'gc-text', textContent: firstLine(item) }),
        el('div', { className: 'gc-body' },
            el('b', { textContent: firstLine(item) }),
            el('small', {}, el('i', { textContent: '◉' }), ` ≈ ${city} · ${platform}`, item.relevance ? ` · ★${item.relevance}` : '')));
}

function stripItem(i, isNew) {
    const p = platformOf(i);
    return el('button', { type: 'button', className: `strip-item${isNew ? ' is-new' : ''}`, style: `--c:${colorOf(p)}`, onclick: () => openPreview(i), title: firstLine(i) },
        i.display_image ? el('img', { src: i.display_image, alt: '', loading: 'lazy' }) : el('span', { className: 'strip-text', textContent: firstLine(i) }),
        i.display_video ? el('span', { className: 'strip-play', textContent: '▶' }) : null,
        el('span', { className: 'strip-meta', textContent: `${PLATFORMS[p] || '◌'} ${i.timestamp ? formatAgo(i.timestamp) : ''}` }));
}

const STEPS = [['сбор', 'Сбор постов'], ['отсев', 'Быстрый отсев'], ['анализ', 'Анализ моделью'], ['подбор', 'Подбор источников']];
const cleanLog = l => l.replace(/[╔╗╚╝║═─│┌┐└┘]+/g, ' ').replace(/\s+/g, ' ').trim();

async function loadLive() {
    clearTimeout(live.timer);
    let r;
    try { r = await apiOrLogin('studio_live'); } catch { live.timer = setTimeout(loadLive, 15000); return; }
    const items = r.items || [];
    const isNew = live.keys ? k => !live.keys.has(k) : () => false;
    const fresh = items.filter(i => isNew(i.key)).length;
    live.keys = new Set(items.map(i => i.key));

    $('win-topic').textContent = r.topic || 'Подбор контента';
    $('live-dot').classList.toggle('on', !!r.running);
    $('live-label').textContent = r.running ? 'В эфире · идёт подбор' : 'Смотровое окно';
    $('ai-phase').textContent = r.running ? (r.phase || 'Работает') : (items.length ? 'Сборщик отдыхает — подборка ждёт ваших решений' : 'Сборщик отдыхает');
    const share = r.progress?.share;
    $('ai-bar').style.width = r.running ? `${Math.round(100 * (share ?? 0.08))}%` : '0%';
    const phase = String(r.phase || '').toLowerCase();
    const at = STEPS.findIndex(([k]) => phase.includes(k));
    $('ai-steps').replaceChildren(...STEPS.map(([, label], i) => el('li', { className: r.running && i < at ? 'done' : r.running && i === Math.max(0, at) ? 'now' : '' }, label)));
    $('stat-pool').textContent = String(r.pool ?? items.length);
    $('stat-added').textContent = r.running ? String(r.progress?.added ?? 0) : '—';
    const hour = items.filter(i => i.timestamp > Date.now() / 1000 - 3600).length;
    $('stat-hour').textContent = hour >= items.length && items.length >= 30 ? '30+' : String(hour);
    $('nav-pool').textContent = r.pool || '';
    $('act-search').disabled = !!r.running;
    $('act-search').textContent = r.running ? '✦ Идёт поиск…' : '✦ Найти контент';
    $('model-pill').classList.toggle('is-busy', !!r.running);

    const lines = (r.activity || []).map(cleanLog).filter(l => l.length > 3).slice(-3);
    $('ticker').replaceChildren(...lines.map(l => el('li', { textContent: l })));

    const pins = items.map(i => { const g = geoOf(i); return { key: i.key, lat: g.lat, lon: g.lon, color: colorOf(g.platform), city: g.city, platform: g.platform, item: i }; });
    live.globe?.setItems(pins);
    const empty = $('globe-empty');
    empty.hidden = items.length > 0;
    empty.textContent = r.running ? 'Сборщик ищет посты — первые появятся на глобусе.' : 'Подборка пуста. Нажмите «Найти контент» — найденные посты всплывут на глобусе.';
    const used = [...new Set(pins.map(p => p.platform))];
    $('globe-legend').replaceChildren(...used.map(p => el('span', { style: `--c:${colorOf(p)}` }, p)));

    $('strip').replaceChildren(...(items.length ? items.map(i => stripItem(i, isNew(i.key)))
        : [el('p', { className: 'muted', textContent: 'Пока ничего: подборка пуста.' })]));
    if (fresh) $('strip').scrollTo({ left: 0, behavior: 'smooth' });
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
    const channel = info.channels?.[0];
    $('profile-name').textContent = info.auth?.tg_name || info.auth?.login || channel?.title || 'Владелец';
    $('profile-avatar').textContent = ($('profile-name').textContent[0] || 'P').toUpperCase();
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

async function loadQueue() {
    const q = await api('telegram_queue');
    const events = [
        ...(q.scheduled || []).slice(0, 3).map(p => ({ at: p.publish_at, title: p.status === 'failed' ? 'Пост не вышел' : 'Запланирован пост', sub: p.channel_title || p.text, kind: p.status === 'failed' ? 'bad' : 'plan' })),
        ...(q.published || []).slice(0, 4).map(p => ({ at: p.at, title: p.status === 'ok' ? 'Опубликовано' : 'Ошибка публикации', sub: p.channel_title || p.text, kind: p.status === 'ok' ? 'ok' : 'bad', url: p.url })),
    ].sort((a, b) => b.at - a.at).slice(0, 5);
    $('activity').replaceChildren(...(events.length ? events.map(e => el('li', { className: e.kind },
        el('i', { textContent: e.kind === 'ok' ? '✓' : e.kind === 'bad' ? '!' : '▦' }),
        el('span', {}, el('b', { textContent: e.title }), el('small', { textContent: `${e.sub || ''} · ${e.at > Date.now() / 1000 ? new Date(e.at * 1000).toLocaleString('ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }) : formatAgo(e.at)}` })),
        e.url ? el('a', { href: e.url, target: '_blank', rel: 'noopener', textContent: '›' }) : null)) : [el('li', { className: 'muted', textContent: 'Событий пока нет' })]));
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
    loadQueue().catch(() => {});
    live.globe = createGlobe($('globe'), { cardFor: globeCard, onOpen: openPreview });
    loadLive();
    loadModel().catch(() => {});
    api('billing_info').then(b => { $('profile-plan').textContent = `Тариф «${b.plan?.title || '—'}»`; }).catch(() => {});
    $('preview').addEventListener('click', e => { if (e.target === $('preview')) $('preview').close(); });
}

init();
