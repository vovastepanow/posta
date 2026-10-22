// sources.js — вкладка «Источники»: кандидат карточкой (мозаика свежих медиа, сводка, панель решений), автоподбор
// и уже добавленные источники темы карточками в стиле «Предложки».
import { $, COUNT, DEBUG_MODE, VIDEO_ICON, api, bindMenu, chip, el, formatAgo, hide, isTabActive, log, onParserStateChange, onTabOpen, pluralRu, safeSourcePostUrl, setMessage, show, watchParser } from './core.js';
import { openViewerGallery, registerViewerGallery } from './viewer.js';

const REASONS = { off_topic: 'не по теме', dead: 'мёртвый', other: 'другое' };
const state = { source: null, skipped: [], loading: false, voting: false, discovering: false, mosaic: null, poll: null };
const topicSources = { sources: [], topic: '', filter: 'all', loading: false };

// ---- Мозаика: шаблоны сетки [колонка, строка, ширина, высота] для 1–6 плиток; выбирается тот, где плитки ближе к пропорциям картинок ----

const T = (cols, rows, ...tiles) => ({ cols, rows, tiles });
const MOSAIC_TEMPLATES = {
    1: [T(1, 1, [1, 1, 1, 1])],
    2: [T(2, 1, [1, 1, 1, 1], [2, 1, 1, 1]), T(1, 2, [1, 1, 1, 1], [1, 2, 1, 1])],
    3: [T(3, 1, [1, 1, 1, 1], [2, 1, 1, 1], [3, 1, 1, 1]), T(1, 3, [1, 1, 1, 1], [1, 2, 1, 1], [1, 3, 1, 1]), T(2, 2, [1, 1, 1, 2], [2, 1, 1, 1], [2, 2, 1, 1]), T(2, 2, [1, 1, 2, 1], [1, 2, 1, 1], [2, 2, 1, 1])],
    4: [T(2, 2, [1, 1, 1, 1], [2, 1, 1, 1], [1, 2, 1, 1], [2, 2, 1, 1]), T(4, 1, [1, 1, 1, 1], [2, 1, 1, 1], [3, 1, 1, 1], [4, 1, 1, 1]),
        T(2, 3, [1, 1, 1, 3], [2, 1, 1, 1], [2, 2, 1, 1], [2, 3, 1, 1]), T(3, 2, [1, 1, 3, 1], [1, 2, 1, 1], [2, 2, 1, 1], [3, 2, 1, 1])],
    5: [T(3, 3, [1, 1, 2, 2], [3, 1, 1, 1], [3, 2, 1, 1], [1, 3, 1, 1], [2, 3, 2, 1]), T(6, 2, [1, 1, 2, 1], [3, 1, 2, 1], [5, 1, 2, 1], [1, 2, 3, 1], [4, 2, 3, 1]),
        T(6, 2, [1, 1, 3, 1], [4, 1, 3, 1], [1, 2, 2, 1], [3, 2, 2, 1], [5, 2, 2, 1])],
    6: [T(3, 2, [1, 1, 1, 1], [2, 1, 1, 1], [3, 1, 1, 1], [1, 2, 1, 1], [2, 2, 1, 1], [3, 2, 1, 1]), T(2, 3, [1, 1, 1, 1], [2, 1, 1, 1], [1, 2, 1, 1], [2, 2, 1, 1], [1, 3, 1, 1], [2, 3, 1, 1]),
        T(3, 3, [1, 1, 2, 2], [3, 1, 1, 1], [3, 2, 1, 1], [1, 3, 1, 1], [2, 3, 1, 1], [3, 3, 1, 1])]
};

/** Шаблон и раздача картинок плиткам (большие плитки — самым похожим картинкам) с минимальной ошибкой пропорций. */
function chooseMosaic(aspects, width, height) {
    let best = null;
    for (const t of MOSAIC_TEMPLATES[aspects.length] || []) {
        const area = i => t.tiles[i][2] * t.tiles[i][3];
        const free = new Set(aspects.keys());
        const assignment = [];
        let error = 0;
        for (const i of t.tiles.map((_, i) => i).sort((a, b) => area(b) - area(a))) {
            const ratio = (t.tiles[i][2] / t.cols * width) / (t.tiles[i][3] / t.rows * height);
            const pick = [...free].map(j => [j, Math.abs(Math.log(ratio / aspects[j]))]).sort((a, b) => a[1] - b[1])[0];
            free.delete(pick[0]);
            assignment[i] = pick[0];
            error += pick[1] * area(i);
        }
        if (!best || error < best.error) best = { t, assignment, error };
    }
    return best;
}

function layoutMosaic(grid, tiles) {
    const live = tiles.filter(tile => !tile.failed);
    grid.replaceChildren(...live.map(tile => tile.node));
    if (!live.length) return show('source-media-fallback');
    const { t, assignment } = chooseMosaic(live.map(({ img }) => (img.naturalWidth && img.naturalHeight ? img.naturalWidth / img.naturalHeight : 1)), grid.clientWidth || 420, grid.clientHeight || 380);
    grid.style.gridTemplateColumns = `repeat(${t.cols}, minmax(0, 1fr))`;
    grid.style.gridTemplateRows = `repeat(${t.rows}, minmax(0, 1fr))`;
    t.tiles.forEach(([c, r, cs, rs], i) => Object.assign(live[assignment[i]].node.style, { gridColumn: `${c} / span ${cs}`, gridRow: `${r} / span ${rs}` }));
}

/** До 6 файлов: по картинке каждого поста, потом вторые картинки и т. д. */
function mosaicItems(source) {
    const posts = (source.posts || []).map(p => ({ images: (p.images?.length ? p.images : [p.image]).filter(Boolean), video: p.video || '', unavailable: Boolean(p.video_unavailable),
        text: p.text || '', url: safeSourcePostUrl({ source_url: p.url }) })).filter(p => p.images.length);
    const items = [];
    for (let round = 0; items.length < 6 && posts.some(p => round < p.images.length); round++) {
        for (const p of posts) if (round < p.images.length && items.length < 6) items.push({ src: p.images[round], video: round ? '' : p.video, unavailable: !round && p.unavailable, text: p.text, url: p.url });
    }
    return items;
}

function sourceGallery(target) {
    const nodes = [...$('source-grid').children].filter(n => !n.dataset.external);   // большие видео без файла в галерею не входят
    const index = nodes.indexOf(target.closest('.source-tile'));
    return index < 0 ? null : { index, items: nodes.map(n => ({ kind: n.dataset.video ? 'video' : 'image', src: n.dataset.video || n.dataset.src, poster: n.dataset.src, sourceUrl: n.dataset.url || '', alt: 'Пост источника' })) };
}

/** Мозаика: видео играют сами без звука, качаются не больше трёх сразу; раскладка уточняется по пропорциям загрузившихся картинок. */
function renderMosaic(source) {
    const grid = $('source-grid');
    hide('source-media-fallback');
    const items = mosaicItems(source);
    grid.replaceChildren();
    if (!items.length) return show('source-media-fallback');
    const token = state.mosaic = Symbol('mosaic');
    const queue = { active: 0, waiting: [], next() { while (this.active < 3 && this.waiting.length) this.waiting.shift()(); } };
    let settled = 0;
    const mark = (title, extra = '') => Object.assign(el('span', { className: `video-mark ${extra}`.trim(), title }), { innerHTML: VIDEO_ICON });
    const tiles = items.map(item => {
        const img = el('img', { src: item.src, alt: 'Пост источника', loading: 'eager', decoding: 'async', title: item.text && item.text !== 'Пост без текста' ? item.text.slice(0, 200) : '' });
        img.dataset.sourceUrl = item.url || '';
        const node = el('div', { className: 'source-tile is-loading' }, img);
        Object.assign(node.dataset, { src: item.src, video: item.video, url: item.url || '' });
        if (item.unavailable && item.url) {   // Telegram не отдаёт большое видео в веб-версии: клик открывает пост
            node.dataset.external = img.dataset.external = item.url;
            img.classList.add('is-video-thumb');
            img.title = 'Видео слишком большое: Telegram не отдаёт его в веб-версии. Клик откроет пост в Telegram.';
            node.append(mark(img.title, 'is-external'));
        } else img.dataset.gallery = 'source';
        if (item.video) {
            img.classList.add('is-video-thumb');
            const video = el('video', { className: 'tile-video', poster: item.src, muted: true, loop: true, playsInline: true, preload: 'auto', hidden: true });
            let released = false;
            const release = () => { if (!released) { released = true; queue.active--; queue.next(); } };
            video.addEventListener('playing', () => { video.hidden = false; release(); }, { once: true });
            video.addEventListener('error', () => { video.remove(); release(); }, { once: true });
            video.addEventListener('click', () => { const g = sourceGallery(video); if (g) openViewerGallery(g.items, g.index); });
            queue.waiting.push(() => {
                if (state.mosaic !== token) return;
                queue.active++;
                Object.assign(video, { muted: true, src: item.video, autoplay: true });
                video.play?.().catch(() => {});
                setTimeout(release, 10000);   // так и не началось — уступает очередь
            });
            node.append(mark('Видео'), video);
        }
        const tile = { img, node, failed: false };
        const done = failed => { node.classList.remove('is-loading'); if (state.mosaic === token) { tile.failed = failed; if (++settled === tiles.length) layoutMosaic(grid, tiles); } };
        img.addEventListener('load', () => done(false), { once: true });
        img.addEventListener('error', () => done(true), { once: true });
        return tile;
    });
    queue.next();
    layoutMosaic(grid, tiles);
    setTimeout(() => { if (state.mosaic === token && settled < tiles.length) layoutMosaic(grid, tiles); }, 4000);   // не дождались всех — раскладываем, что есть
}

function renderStats(s = {}) {
    const chips = [
        s.subscribers > 0 && chip('подписчики', COUNT.format(s.subscribers)),
        s.view_rate != null && chip('просмотры', `${Math.round(s.view_rate * 100)}% подписчиков`, 'Какая доля подписчиков видит пост: медиана просмотров свежих постов ÷ подписчики'),
        s.view_rate >= 0.25 && el('span', { className: 'eng-chip eng-up', textContent: '↑ живая аудитория' }),
        s.view_rate != null && s.view_rate < 0.05 && el('span', { className: 'eng-chip eng-down', textContent: '↓ посты почти не смотрят' }),
        s.thread_posts != null && chip('в треде', `${COUNT.format(s.thread_posts)} ${pluralRu(s.thread_posts, 'пост', 'поста', 'постов')}`),
        s.thread_files && chip('файлов в треде', COUNT.format(s.thread_files), 'Мозаика — подборка файлов со всего треда, а не только из шапки'),
        s.posts_per_week != null && chip('постов в неделю', `~${Math.round(s.posts_per_week)}`),
        s.last_post && chip('последний пост', formatAgo(s.last_post))
    ].filter(Boolean);
    $('source-stats').replaceChildren(...chips);
    $('source-stats').hidden = !chips.length;
}

function renderSource(source) {
    state.source = source;
    renderMosaic(source);
    const s = source.stats || {};
    const url = safeSourcePostUrl({ source_url: s.url });
    $('source-name').replaceChildren('Источник: ', url ? el('a', { className: 'source-name-link', href: url, target: '_blank', rel: 'noopener noreferrer', textContent: source.name }) : source.name);
    $('source-platform-label').textContent = source.platform + (source.suggested ? ' · найден автоподбором' : '');
    setMessage('source-score', source.score != null ? `${source.score}%` : '');
    renderStats(s);
    if (url) $('source-link').href = url; else $('source-link').removeAttribute('href');
    $('source-link').hidden = !url;
    $('source-link').textContent = source.platform === 'Двач' ? 'Открыть тред ↗' : 'Открыть источник ↗';
    $('source-why').textContent = source.reason || 'Кандидат из каталога источников.';
    $('source-desc').replaceChildren(el('strong', { textContent: 'Описание: ' }), s.description || '');
    $('source-desc').hidden = !s.description;
    clearTimeout(state.poll);
    showArea('card');
    log(`Показан источник: ${source.name}${source.score != null ? `, оценка подбора ${source.score}%` : ''}.`);
}

function showArea(area, message = '') {
    show('source-card-wrapper', area === 'card');
    show('source-empty', area === 'empty');
    setMessage('source-error', area === 'error' ? message : '');
    hide('source-loading');
    if (area !== 'empty') return;
    $('source-empty-message').textContent = (message || 'Новых источников пока нет.') + (state.discovering ? ' Подбор идёт: найденные источники появятся здесь по одному.' : '');
    clearTimeout(state.poll);
    // Пока подбор идёт, а карточки нет, раз в 10 с тихо проверяем готовые находки
    if (state.discovering) state.poll = setTimeout(async () => {
        if (isTabActive('source-tab') && !state.source && !state.loading) await loadNextSource({ quiet: true });
        if (!state.source) showArea('empty', message);
    }, 10000);
}

export function clearDiscoveredSource() { Object.assign(state, { source: null, skipped: [] }); hide('source-card-wrapper'); }

async function loadNextSource({ quiet = false } = {}) {
    if (state.loading) return;
    state.loading = true;
    if (!quiet) { state.source = null; hide('source-card-wrapper'); hide('source-empty'); setMessage('source-error', ''); show('source-loading'); }
    try {
        const data = await api('suggest_sources', { body: { platform: $('source-platform').value, skip: state.skipped, ...(quiet ? { suggested_only: true } : {}) } });
        if (data.discovered) renderSource(data.discovered);
        else if (!quiet) showArea('empty', data.message);
    } catch (error) { if (!quiet) showArea('empty', `Не удалось загрузить источник: ${error.message}`); }
    finally { state.loading = false; }
}

async function vote(type, reason = '') {
    const source = state.source;
    if (!source || state.voting) return;
    if (type === 'postpone') { state.skipped.push(source.source_id); log(`Отложен источник: ${source.name}`); return loadNextSource(); }
    state.voting = true;
    const buttons = document.querySelectorAll('#source-tab .controls button');
    buttons.forEach(b => { b.disabled = true; });
    try {
        const subject = Object.fromEntries(['source_id', 'platform', 'name', 'id', 'board', 'thread_id', 'suggested', 'score'].filter(k => source[k] !== undefined).map(k => [k, source[k]]));
        await api('source_vote', { body: { vote: type, reason, source: subject } });
        if (type !== 'maybe') loadTopicSources();
        log(`${{ approve: 'Одобрен', reject: 'Отклонён', maybe: 'Без решения' }[type]} источник: ${source.name}${reason ? ` (${REASONS[reason]})` : ''}`);
        state.voting = false;
        await loadNextSource();
    } catch (error) { setMessage('source-error', `Решение не сохранено: ${error.message}`); }
    finally { state.voting = false; buttons.forEach(b => { b.disabled = false; }); }
}

async function loadDiscoveryStatus() {
    if (DEBUG_MODE) { $('discovery-status').textContent = 'В демо-режиме автоподбор отключён.'; $('discovery-button').disabled = true; return; }
    try {
        const d = await api('discovery_status');
        state.discovering = Boolean(d.requested || d.running);
        $('discovery-status').textContent = [`Тема «${d.topic}».`,
            (d.last_topic || '').toLowerCase() === (d.topic || '').toLowerCase() && d.last_run ? `Последний подбор: ${formatAgo(d.last_run)}.` : 'По этой теме подбора ещё не было.',
            `В каталоге найденных: ${d.auto_added}, ждут вашего решения: ${d.suggestions}.`,
            d.frozen ? `Заморожено из-за редких постов: ${d.frozen} (перепроверяются раз в несколько дней).` : '',
            d.requested ? 'Подбор заказан — начнётся при запуске парсера.' : '', d.vk_connected ? '' : 'Подключите VK, чтобы искать и сообщества ВКонтакте.'].filter(Boolean).join(' ');
    } catch (error) { $('discovery-status').textContent = `Сведения недоступны: ${error.message}`; }
}

async function startDiscovery() {
    $('discovery-button').disabled = true;
    try {
        log((await api('discover_sources', { body: null })).message);
        state.discovering = true;
        if (!state.source) showArea('empty');
        watchParser();
    } catch (error) { setMessage('source-error', `Автоподбор не запущен: ${error.message}`); }
    finally { $('discovery-button').disabled = false; loadDiscoveryStatus(); }
}

// ---- Источники темы ----

function avatar(source) {
    const box = el('span', { className: 'source-avatar', textContent: (source.name || source.id || '?').replace(/^[^\p{L}\p{N}]+/u, '').charAt(0).toUpperCase() || '?' });
    box.setAttribute('aria-hidden', 'true');
    if (source.avatar) {   // без lazy: картинка вне документа «лениво» не грузится
        const image = el('img', { src: source.avatar, alt: '', decoding: 'async', referrerPolicy: 'no-referrer' });
        image.addEventListener('load', () => box.replaceChildren(image), { once: true });
    }
    return box;
}

function topicSourceCard(source) {
    const cover = () => el('div', { className: 'source-cover' }, avatar(source));
    const slides = source.preview.map(src => ({ kind: 'image', src, poster: src, sourceUrl: source.url, alt: source.name }));
    let media = cover();
    if (slides.length) {
        media = el('div', { className: `proposal-media mosaic-${slides.length}` });
        slides.forEach((slide, i) => {
            const tile = el('button', { className: 'proposal-tile is-loading', type: 'button', title: 'Открыть', onclick: e => { e.stopPropagation(); openViewerGallery(slides, i); } },
                el('img', { src: slide.src, alt: '', loading: 'lazy', decoding: 'async', referrerPolicy: 'no-referrer',
                    onload: () => tile.classList.remove('is-loading'),
                    onerror: () => { tile.remove(); const left = media.children.length; left ? media.className = `proposal-media mosaic-${left}` : media.replaceWith(cover()); } }));
            media.append(tile);
        });
    }
    const chips = [source.subscribers > 0 && chip('подписчики', COUNT.format(source.subscribers)), source.view_rate != null && chip('просмотры', `${Math.round(source.view_rate * 100)}%`, 'Какая доля подписчиков видит пост'),
        source.score != null && chip('оценка', `${source.score}%`, 'Оценка автоподбора: по теме, аудитория, доля просмотров')].filter(Boolean);
    const notes = [!source.own && (source.topics.length ? `из общей темы: ${source.topics.join(', ')}` : 'источник для всех тем'), source.auto && 'найден автоподбором'].filter(Boolean);
    const card = el('article', { className: `proposal-card source-tile-card${source.frozen ? ' is-frozen' : ''}` }, media,
        el('div', { className: 'source-head' }, avatar(source), el('div', { className: 'source-head-text' }, el('strong', { textContent: source.name, title: source.name }),
            el('a', { href: source.url, target: '_blank', rel: 'noopener noreferrer', textContent: source.handle })),
            el('span', { className: 'source-platform-tag', textContent: { 'ВКонтакте': 'VK' }[source.platform] || source.platform })),
        chips.length ? el('div', { className: 'source-chips' }, ...chips) : null,
        el('span', { className: `source-status${source.frozen ? ' is-frozen' : ''}`, textContent: source.platform === 'Двач' ? `доска имиджборда${source.category ? ` · ${source.category}` : ''}`
            : source.frozen ? `заморожен: ${source.frozen_reason || 'не активен'}` : source.activity || 'активен' }),
        notes.length ? el('p', { className: 'source-note', textContent: notes.join(' · ') }) : null);
    const actions = el('div', { className: 'proposal-actions' }, el('a', { className: 'proposal-action', href: source.url, target: '_blank', rel: 'noopener noreferrer', textContent: 'Открыть ↗' }));
    if (!DEBUG_MODE && source.own) actions.append(el('button', { className: 'proposal-action proposal-hide', type: 'button', textContent: '✕ Убрать', title: `Убрать из темы «${topicSources.topic}»`, onclick: async () => {
        if (!confirm(`Убрать «${source.name}» из темы «${topicSources.topic}»? Посты из него перестанут попадать в подбор по этой теме.`)) return;
        card.classList.add('is-removing');
        try {
            await api('remove_topic_source', { body: { platform: source.platform, id: source.id, name: source.name } });
            topicSources.sources = topicSources.sources.filter(o => o !== source);
            log(`Источник «${source.name}» убран из темы «${topicSources.topic}».`);
            renderTopicSources();
        } catch (error) { card.classList.remove('is-removing'); log(`Не удалось убрать источник: ${error.message}`); }
    } }));
    card.append(actions);
    return card;
}

function renderTopicSources() {
    const { sources, topic, filter } = topicSources;
    const visible = sources.filter(s => filter === 'all' || s.platform === filter);
    $('topic-sources-title').textContent = topic ? `Источники темы «${topic}»` : 'Источники темы';
    $('topic-sources-count').textContent = sources.length ? `${visible.length} из ${sources.length}` : '';
    $('topic-sources-status').textContent = visible.length ? '' : sources.length ? 'На этой площадке источников темы нет.' : 'У темы пока нет источников: нажмите «Подобрать источники» или одобряйте предложенные выше.';
    $('topic-sources-grid').replaceChildren(...visible.map(topicSourceCard));
    document.querySelectorAll('[data-topic-source-filter]').forEach(b => b.classList.toggle('active', b.dataset.topicSourceFilter === filter));
}

async function loadTopicSources() {
    if (topicSources.loading) return;
    topicSources.loading = true;
    try {
        const data = await api('topic_sources');
        Object.assign(topicSources, { sources: data.sources || [], topic: data.topic || '' });
        renderTopicSources();
    } catch (error) { $('topic-sources-status').textContent = `Не удалось загрузить источники: ${error.message}`; }
    finally { topicSources.loading = false; }
}

/** «Добавить источник»: ссылка → проверка на сервере → источник темы; список обновляется. */
async function addSource(event) {
    event.preventDefault();
    const [input, button, status] = [$('source-add-link'), $('source-add-button'), $('source-add-status')];
    const link = input.value.trim();
    if (!link) return;
    button.disabled = true;
    status.textContent = 'Проверяю источник…';
    try {
        const result = await api('add_topic_source', { body: { link } });
        status.textContent = result.added ? `Добавлен: ${result.name} (свежих постов с картинками: ${result.posts})` : `Уже в теме: ${result.name}`;
        input.value = '';
        await loadTopicSources();
    } catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; }
}

export function initializeSourceTab() {
    $('source-add-form').addEventListener('submit', addSource);
    registerViewerGallery('source', sourceGallery);
    document.querySelectorAll('#source-tab [data-source-vote]').forEach(b => b.addEventListener('click', () => vote(b.dataset.sourceVote)));
    bindMenu($('source-complaint-button'), $('source-complaint-menu'), item => vote('reject', item.dataset.sourceReason));
    const retry = () => { state.skipped = []; loadNextSource(); };
    $('source-platform').addEventListener('change', retry);
    $('source-empty-retry').addEventListener('click', retry);
    $('source-empty-discover').addEventListener('click', startDiscovery);
    $('discovery-button').addEventListener('click', startDiscovery);
    document.querySelectorAll('[data-topic-source-filter]').forEach(b => b.addEventListener('click', () => { topicSources.filter = b.dataset.topicSourceFilter; renderTopicSources(); }));
    $('topic-sources-refresh').addEventListener('click', loadTopicSources);
    onTabOpen('source-tab', () => { loadDiscoveryStatus(); loadTopicSources(); if (!state.source && !state.loading) loadNextSource(); });
    onParserStateChange(running => {   // парсер закончил: подбор мог найти источники, сбор — обновить аудиторию
        if (running) return;
        state.discovering = false;
        if (!isTabActive('source-tab')) return;
        loadDiscoveryStatus();
        loadTopicSources();
        if (!state.source && !state.loading) loadNextSource();
    });
}
