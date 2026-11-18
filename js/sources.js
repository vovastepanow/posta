// sources.js — вкладка «Источники»: кандидат карточкой (мозаика свежих медиа, сводка, панель решений), автоподбор
// и уже добавленные источники темы карточками в стиле «Предложки».
import { $, ask, COUNT, DEBUG_MODE, VIDEO_ICON, cleanCaption, api, bindMenu, chip, el, formatAgo, hide, isTabActive, log, onParserStateChange, onTabOpen, onTopicChange, pluralRu, qualityText, safeSourcePostUrl, setMessage, show, watchParser } from './core.js';
import { BIG_VIDEO_NOTE, closePostView, openPostView, shownText } from './post-view.js';
import { fromSourcePost } from './post-adapters.js';
import { videoPageFor } from './viewer.js';
import { bindFutureDraftButton, toggleFutureDraft, loadCurrentItem, postItemToTelegram } from './content.js';
import { openAccountSection } from './account.js';
import { mediaFileKey } from './media-dedupe.js';
import { loadImage, pageCheck, videoProgress, watchVideo } from './video-pending.js';
import { shortText } from './card.js';
import { initRegistry, loadRegistry } from './registry.js';
import { prepareOwnImage } from './image-prepare.js';

const REASONS = { off_topic: 'не по теме', dead: 'мёртвый', other: 'другое' };
const state = { source: null, skipped: [], loading: false, request: 0, voting: false, discovering: false, mosaic: null, poll: null, emptyAction: '' };
const topicSources = { sources: [], topic: '', filter: 'all', loading: false };

// Сохранённые предложения показываются сразу; сетевые примеры догружаются только у видимых карточек.
const pendingSources = { generation: 0, sources: [], observer: null, pageObserver: null, seen: [], total: 0, hasMore: false, pageLoading: false, lastScroll: 0, queue: [], active: 0 };
const pendingImageObserver = 'IntersectionObserver' in window ? new IntersectionObserver(entries => {
    for (const entry of entries) if (entry.isIntersecting) {
        pendingImageObserver.unobserve(entry.target);
        entry.target._startImageTimeout?.();
    }
}, { rootMargin: '150px' }) : null;

function pendingPost(post, source, index, posts, onImageError) {
    const file = post.media?.[0];
    const image = file?.kind === 'video' ? file.poster : file?.src;
    const tile = el('button', { className: 'pending-post', type: 'button', title: 'Открыть пост' });
    if (image) {
        const img = el('img', { src: image, alt: '', loading: 'lazy', decoding: 'async', referrerPolicy: 'no-referrer' });
        let imageTimer;
        const failed = () => {
            clearTimeout(imageTimer);
            pendingImageObserver?.unobserve(tile);
            if (!img.isConnected) return;
            if (file?.kind === 'image' && file.fallback && img.src !== file.fallback) { img.src = file.fallback; startTimeout(); return; }
            img.remove();
            if (!tile.querySelector('.pending-post-text')) tile.append(el('span', { className: 'pending-post-text', textContent: 'Открыть пост' }));
            onImageError();
        };
        const startTimeout = () => { clearTimeout(imageTimer); if (!img.complete || !img.naturalWidth) imageTimer = setTimeout(failed, 5000); };
        img.addEventListener('load', () => { clearTimeout(imageTimer); pendingImageObserver?.unobserve(tile); });
        img.addEventListener('error', failed);
        tile.append(img);
        tile._startImageTimeout = startTimeout;
        if (pendingImageObserver) pendingImageObserver.observe(tile);
        else requestAnimationFrame(startTimeout);
    }
    if (file?.kind === 'video') tile.append(el('span', { className: 'pending-post-video', textContent: '▶' }));
    const text = cleanCaption(post.text || '');
    if (text) tile.append(el('span', { className: 'pending-post-text', textContent: shortText(text, 130) }));
    if (!image && !text) tile.append(el('span', { className: 'pending-post-text', textContent: 'Открыть пост' }));
    tile.addEventListener('click', () => {
        const meta = { name: source.name, url: source.url || '' };
        openPostView(posts.map(p => fromSourcePost(p, meta)), index, { title: source.name, translate: true });
    });
    return tile;
}

function pendingRenderPosts(card, source) {
    const grid = card.querySelector('.pending-posts');
    const posts = (source.posts || []).filter(p => p.text || p.media?.length).slice(0, 6);
    grid.replaceChildren(...posts.map((post, index) => pendingPost(post, source, index, posts, () => {
        if (card.dataset.previewRefreshed) {
            card.querySelector('.pending-preview-note').textContent = 'Некоторые изображения недоступны у источника.';
            return;
        }
        card.dataset.previewBroken = '1';
        requestAnimationFrame(() => { if (card.isConnected) queuePendingPreview(card, source, pendingSources.generation); });
    })));
    card.querySelector('.pending-preview-note').textContent = posts.length ? `Примеры постов: ${posts.length}` : 'Сохранённых примеров нет.';
    // Старые сохранённые примеры Lemmy могли содержать оригинал и превью как альбом.
    const oldLemmyPreview = posts.some(post => {
        try {
            const page = new URL(post.url);
            const media = post.media || [];
            return /^\/post\/\d+\/?$/.test(page.pathname) && media.length === 2 && media.every(m => m.kind === 'image') &&
                new URL(media[1].src).host === page.host && new URL(media[1].src).pathname.startsWith('/pictrs/image/') && !media[0].fallback;
        } catch { return false; }
    });
    if (oldLemmyPreview && !card.dataset.previewRefreshed) {
        card.dataset.previewBroken = '1';
        requestAnimationFrame(() => { if (card.isConnected) queuePendingPreview(card, source, pendingSources.generation); });
    }
    return posts.length;
}

async function pendingPreviewRequest(sourceId, titleOnly = false) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 20000);
    try {
        return await api('pending_source_preview', { body: { source_id: sourceId, title_only: titleOnly }, signal: controller.signal });
    } finally { clearTimeout(timer); }
}

function queuePendingPreview(card, source, generation) {
    if (card.dataset.previewQueued) return;
    card.dataset.previewQueued = '1';
    const job = { card, source, generation };
    if (card.dataset.previewBroken) pendingSources.queue.unshift(job);
    else pendingSources.queue.push(job);
    runPendingPreviews();
}

function runPendingPreviews() {
    while (pendingSources.active < 6 && pendingSources.queue.length) {
        const job = pendingSources.queue.shift();
        if (job.generation !== pendingSources.generation || !job.card.isConnected) continue;
        pendingSources.active++;
        const note = job.card.querySelector('.pending-preview-note');
        const titleOnly = Boolean(job.source.platform !== 'Telegram' && !job.card.dataset.previewBroken && !job.source.needs_media_refresh && job.source.needs_title && (job.source.posts || []).length >= 4);
        if (!titleOnly) job.card.dataset.previewRefreshed = '1';
        if (!titleOnly) note.textContent = 'Загружаем примеры постов…';
        pendingPreviewRequest(job.source.source_id, titleOnly).then(data => {
            if (job.generation !== pendingSources.generation || !job.card.isConnected) return;
            if (data.name) {
                job.source.name = data.name;
                const name = job.card.querySelector('.pending-source-title strong a, .pending-source-title strong span');
                if (name) name.textContent = data.name;
                job.source.needs_title = false;
            }
            if (titleOnly) {
                if (job.card.dataset.previewBroken) {
                    delete job.card.dataset.previewQueued;
                    queuePendingPreview(job.card, job.source, job.generation);
                }
                return;
            }
            const fetched = data.posts || [];
            if (fetched.length) job.source.posts = fetched;
            if (!pendingRenderPosts(job.card, job.source)) {
                note.textContent = 'Не удалось найти примеры. Источник можно открыть по ссылке.';
                const retry = el('button', { type: 'button', className: 'pending-preview-retry', textContent: 'Повторить' });
                retry.addEventListener('click', () => { delete job.card.dataset.previewQueued; retry.remove(); queuePendingPreview(job.card, job.source, job.generation); });
                note.append(' ', retry);
            }
        }).catch(() => {
            if (job.generation !== pendingSources.generation || !job.card.isConnected) return;
            note.textContent = titleOnly ? 'Название временно недоступно.' : 'Примеры временно недоступны.';
            const retry = el('button', { type: 'button', className: 'pending-preview-retry', textContent: 'Повторить' });
            retry.addEventListener('click', () => { delete job.card.dataset.previewQueued; retry.remove(); queuePendingPreview(job.card, job.source, job.generation); });
            note.append(' ', retry);
        }).finally(() => { pendingSources.active--; runPendingPreviews(); });
    }
}

// Подробности открываются сразу по сохранённым данным и дополняются свежей аналитикой.
function openPendingDetails(source, card) {
    const dlg = el('dialog', { className: 'pending-detail-dialog', ariaLabel: `Информация об источнике ${source.name || ''}` });
    const close = el('button', { type: 'button', className: 'pending-detail-close', textContent: '×', ariaLabel: 'Закрыть' });
    const title = el('h2', { className: 'pending-detail-title', textContent: source.name || source.id || 'Источник' });
    const platform = el('p', { className: 'pending-source-platform', textContent: source.platform_label || source.kind || source.platform || '' });
    const status = el('p', { className: 'pending-detail-status', role: 'status', textContent: 'Загружаем свежие сведения…' });
    const metrics = el('div', { className: 'pending-detail-metrics', ariaLabel: 'Аналитика источника' });
    const desc = el('p', { className: 'pending-detail-description' });
    const reason = el('p', { className: 'pending-detail-reason', textContent: source.reason || 'Найден автоподбором по теме.' });
    const examples = el('div', { className: 'pending-detail-examples' });
    const url = safeSourcePostUrl({ source_url: source.url || '' });
    const link = url ? el('a', { className: 'pending-detail-link', href: url, target: '_blank', rel: 'noopener noreferrer', textContent: 'Открыть источник ↗' }) : null;
    close.addEventListener('click', () => dlg.close());
    dlg.addEventListener('click', event => { if (event.target === dlg) dlg.close(); });
    dlg.addEventListener('close', () => dlg.remove(), { once: true });
    dlg.append(el('div', { className: 'pending-detail-head' }, el('div', {}, title, platform), close));
    if (link) dlg.append(link);
    dlg.append(status, el('h3', { textContent: 'Аналитика' }), metrics, desc, el('h3', { textContent: 'Почему предложен' }), reason,
        el('h3', { textContent: 'Примеры постов' }), examples);
    document.body.append(dlg);
    dlg.showModal();
    function render(data = {}) {
        const saved = source.metrics || {}, fresh = data.stats || {}, activity = saved.activity || {};
        const fields = [];
        const add = (label, value) => { if (value !== null && value !== undefined && value !== '') fields.push(el('div', {}, el('span', { textContent: label }), el('b', { textContent: String(value) }))); };
        if (source.score != null) add('Оценка подбора', `${source.score}%`);
        const subscribers = Number(fresh.subscribers || saved.subscribers || 0);
        if (subscribers > 0) add('Подписчики', COUNT.format(subscribers));
        const rate = fresh.view_rate ?? saved.view_rate;
        if (rate !== null && rate !== undefined && Number.isFinite(Number(rate))) add('Охват подписчиков', `${Math.round(Number(rate) * 100)}%`);
        const perWeek = fresh.posts_per_week ?? activity.per_week;
        if (perWeek !== null && perWeek !== undefined && Number.isFinite(Number(perWeek))) add('Постов в неделю', `≈ ${Math.round(Number(perWeek))}`);
        const last = fresh.last_post || activity.last_post;
        if (last) add('Последний пост', formatAgo(last));
        if (saved.content != null) add('Соответствие теме', `${saved.content}%`);
        if (data.quality?.quality != null) add('Оценки постов', qualityText(data.quality));
        const posts = data.posts?.length ? data.posts : (source.posts || []);
        const views = posts.map(p => p.views).filter(v => v !== null && v !== undefined && Number.isFinite(Number(v))).map(Number).sort((a, b) => a - b);
        if (views.length) {
            const mid = Math.floor(views.length / 2);
            const median = views.length % 2 ? views[mid] : (views[mid - 1] + views[mid]) / 2;
            add(`Медиана просмотров · ${views.length} пост.`, COUNT.format(median));
        }
        metrics.replaceChildren(...(fields.length ? fields : [el('p', { className: 'pending-sources-hint', textContent: 'Метрик пока нет.' })]));
        desc.textContent = fresh.description || '';
        examples.replaceChildren(...(posts.length ? posts.slice(0, 6).map((post, index) => {
            const postUrl = safeSourcePostUrl({ source_url: post.url || '' });
            const label = cleanCaption(post.text || '').slice(0, 110) || `Пост ${index + 1}`;
            return postUrl ? el('a', { href: postUrl, target: '_blank', rel: 'noopener noreferrer', textContent: label }) : el('span', { textContent: label });
        }) : [el('p', { className: 'pending-sources-hint', textContent: 'Примеры пока недоступны.' })]));
    }
    render();
    pendingPreviewRequest(source.source_id).then(data => {
        if (!dlg.isConnected) return;
        if (data.name) {
            source.name = data.name;
            source.needs_title = false;
            title.textContent = data.name;
            const node = card?.querySelector('.pending-source-title strong a, .pending-source-title strong span');
            if (node) node.textContent = data.name;
        }
        render(data);
        status.textContent = data.posts?.length ? 'Данные обновлены.' : 'Свежие примеры не получены; показаны доступные сведения.';
    }).catch(error => { if (dlg.isConnected) status.textContent = `Не удалось обновить сведения: ${error.message}`; });
}

function pendingCard(source, generation) {
    const card = el('article', { className: 'pending-source-card', tabIndex: 0, role: 'button', ariaLabel: `Подробнее об источнике ${source.name || ''}` });
    card.addEventListener('click', event => { if (!event.target.closest('button, a, details') && !window.getSelection()?.toString()) openPendingDetails(source, card); });
    card.addEventListener('keydown', event => { if (event.target === card && (event.key === 'Enter' || event.key === ' ')) { event.preventDefault(); openPendingDetails(source, card); } });
    const url = safeSourcePostUrl({ source_url: source.url || '' });
    const name = url ? el('a', { href: url, target: '_blank', rel: 'noopener noreferrer', textContent: source.name || source.id }) : el('span', { textContent: source.name || source.id });
    const head = el('div', { className: 'pending-source-title' }, el('strong', {}, name), source.score != null ? el('span', { className: 'pending-score', textContent: `${source.score}%` }) : null);
    const platform = el('p', { className: 'pending-source-platform', textContent: source.platform_label || source.kind || source.platform || '' });
    const note = el('p', { className: 'pending-preview-note' });
    const grid = el('div', { className: 'pending-posts', ariaLabel: 'Примеры постов' });
    const actions = el('div', { className: 'pending-source-actions' });
    const error = el('p', { className: 'pending-source-error', role: 'alert' });
    actions.append(el('button', { type: 'button', className: 'secondary-button', textContent: 'Подробнее', onclick: () => openPendingDetails(source, card) }));
    for (const [voteType, label] of [['approve', 'Одобрить'], ['maybe', 'Не знаю'], ['reject', 'Отклонить']]) {
        const button = el('button', { type: 'button', className: voteType === 'approve' ? 'primary-button' : 'secondary-button', textContent: label });
        button.addEventListener('click', async () => {
            actions.querySelectorAll('button').forEach(b => { b.disabled = true; });
            try {
                await api('source_vote', { body: { vote: voteType, source: Object.fromEntries(['source_id', 'platform', 'name', 'id', 'suggested', 'score'].filter(k => source[k] !== undefined).map(k => [k, source[k]])) } });
                pendingSources.observer?.unobserve(card);
                card.remove();
                const index = pendingSources.sources.findIndex(s => s.source_id === source.source_id);
                if (index >= 0) { pendingSources.sources.splice(index, 1); pendingSources.total = Math.max(0, pendingSources.total - 1); }
                updatePendingCount();
                loadDiscoveryStatus();
                if (voteType !== 'maybe') loadTopicSources();
                log(`${label}: ${source.name}`);
            } catch (e) { error.textContent = `Не удалось сохранить решение: ${e.message}`; actions.querySelectorAll('button').forEach(b => { b.disabled = false; }); }
        });
        actions.append(button);
    }
    const why = el('details', { className: 'pending-source-why' }, el('summary', { textContent: 'Почему предложен' }), el('p', { textContent: source.reason || 'Найден автоподбором по теме.' }));
    card.append(head, platform, note, grid, why, actions, error);
    const cachedCount = pendingRenderPosts(card, source);
    // Работающие сохранённые примеры не занимают очередь сетевых запросов.
    if (cachedCount < 4 || source.needs_title || source.needs_media_refresh) {
        if (cachedCount < 4) note.textContent = cachedCount ? `Примеры постов: ${cachedCount} · ищем ещё` : 'Примеры загрузятся при просмотре карточки.';
        if (pendingSources.observer) pendingSources.observer.observe(card);
        else requestAnimationFrame(() => queuePendingPreview(card, source, generation));
        card._pendingSource = source;
    }
    return card;
}

function updatePendingCount() {
    const n = pendingSources.total;
    $('pending-sources-count').textContent = `${n} ${pluralRu(n, 'источник ожидает', 'источника ожидают', 'источников ожидают')} решения`;
    $('pending-sources-empty').hidden = n > 0;
    if (!n) $('pending-sources-empty').textContent = 'Для этой темы и площадки пока нет источников, ожидающих решения.';
}

async function appendPendingBatch(generation) {
    if (generation !== pendingSources.generation || pendingSources.pageLoading || !pendingSources.hasMore) return;
    pendingSources.pageLoading = true;
    const more = $('pending-sources-more');
    more.disabled = true;
    more.textContent = 'Загружаем…';
    const width = $('pending-sources-grid').clientWidth || 900;
    const columns = Math.max(1, Math.floor((width + 14) / 384));
    const rows = Math.max(2, Math.ceil(window.innerHeight / 420) + 1);
    try {
        const data = await api('pending_sources_page', { body: { platform: $('source-platform').value, sort: $('source-sort').value, exclude: pendingSources.seen, limit: Math.min(40, columns * rows) } });
        if (generation !== pendingSources.generation) return;
        const incoming = (data.sources || []).filter(s => !pendingSources.seen.includes(s.source_id));
        pendingSources.sources.push(...incoming);
        pendingSources.seen.push(...incoming.map(s => s.source_id));
        pendingSources.total = Number(data.total) || 0;
        pendingSources.hasMore = Boolean(data.has_more) && incoming.length > 0;
        $('pending-sources-grid').append(...incoming.map(source => pendingCard(source, generation)));
        more.hidden = !pendingSources.hasMore;
        updatePendingCount();
        pendingSources.lastScroll = window.scrollY;
    } catch (error) {
        if (generation === pendingSources.generation) {
            more.hidden = false;
            $('pending-sources-empty').hidden = false;
            $('pending-sources-empty').textContent = `Не удалось загрузить источники: ${error.message}`;
        }
    } finally {
        if (generation === pendingSources.generation) { pendingSources.pageLoading = false; more.disabled = false; more.textContent = 'Показать ещё'; }
    }
}

async function loadPendingSources() {
    const generation = ++pendingSources.generation;
    pendingSources.observer?.disconnect();
    pendingSources.pageObserver?.disconnect();
    Object.assign(pendingSources, { sources: [], seen: [], total: 0, hasMore: true, pageLoading: false, queue: [] });
    $('pending-sources-count').textContent = 'Загружаем сохранённые источники…';
    $('pending-sources-empty').hidden = true;
    $('pending-sources-grid').replaceChildren();
    $('pending-sources-more').hidden = true;
    pendingSources.observer = 'IntersectionObserver' in window ? new IntersectionObserver(entries => {
        for (const entry of entries) if (entry.isIntersecting && entry.target._pendingSource) {
            pendingSources.observer?.unobserve(entry.target);
            queuePendingPreview(entry.target, entry.target._pendingSource, generation);
        }
    }, { rootMargin: '250px' }) : null;
    pendingSources.lastScroll = window.scrollY;
    await appendPendingBatch(generation);
}

// ---- Лента постов кандидата: компактные строки (миниатюра и текст), по клику — просмотрщик постов ----

const FEED_FIRST = 12;         // сначала столько постов, остальные — по кнопке «Ещё»
const feed = { posts: [], shown: 0, token: null, watches: [] };

/** Посты для ленты: файл, уже показанный в другом посте, не повторяется; пост без медиа и текста убирается. */
function feedPosts(source) {
    const seen = new Set();
    return (source.posts || []).map(post => ({ ...post, media: (post.media || []).filter(m => {
        if (!m.src) return true;   // видео без файла — пометка, а не повтор
        const key = mediaFileKey(m.src);
        if (seen.has(key)) return false;
        seen.add(key);
        return true;
    }) })).filter(post => post.media.length || post.text);
}

/** Миниатюра: если картинка не открылась — серая плашка с прочерком, строка поста остаётся. */
function thumbImage(src, box) {
    const img = el('img', { alt: '', decoding: 'async', referrerPolicy: 'no-referrer' });
    img.addEventListener('error', () => img.replaceWith(thumbPlate('—', 'Файл недоступен')));
    loadImage(img, src, box, { compact: true });   // дольше двух секунд — тонкая полоса на миниатюре
    return img;
}

/** Плашка миниатюры: знак и пояснение в title. */
function thumbPlate(mark, title, extra = '') {
    return el('span', { className: `sp-plate ${extra}`.trim(), title, textContent: mark });
}

/** Значок «это видео» — тот же, что в предложке (.video-mark); других значков на видео не ставим. */
const videoMark = () => el('span', { className: 'video-mark', innerHTML: VIDEO_ICON });

/** Миниатюра поста 96 px (72 px на телефоне): альбом — первая картинка и счётчик 1/N; видео — постер и значок. */
function postThumb(media) {
    const first = media[0];
    const box = el('div', { className: 'sp-thumb' });
    if (media.length > 1) box.append(el('span', { className: 'sp-count', textContent: `1/${media.length}` }));
    const src = first.kind === 'video' ? first.poster : first.src;
    if (src) {
        box.append(thumbImage(src, box));
        if (first.kind === 'video' && first.pending && first.page) watchFeedVideo(box, first);   // ролик качается: по готовности пост откроется сразу с видео
    }
    else if (first.kind === 'video' && first.src) {   // без постера — первый кадр, без воспроизведения
        const video = el('video', { src: first.src, muted: true, playsInline: true, preload: 'metadata' });
        video.addEventListener('error', () => video.replaceWith(thumbPlate('—', 'Видео недоступно')));
        box.append(video);
    } else if (first.kind === 'video' && first.pending && first.page) {
        box.append(thumbPlate('', 'Видео загружается…'));
        watchFeedVideo(box, first);
    } else if (first.kind === 'video' && first.unavailable) box.append(thumbPlate('', BIG_VIDEO_NOTE));
    else if (first.kind === 'video' && first.page) box.append(thumbPlate('', 'Видео — откройте пост'));
    else box.append(thumbPlate('—', 'Файл недоступен'));
    if (first.kind === 'video') box.append(videoMark());
    return box;
}

/** Ролик, который докачивается: по готовности пост получает файл (просмотрщик сразу играет его), миниатюра без постера — плашка «Видео готово». */
function watchFeedVideo(box, m) {
    const bar = videoProgress(box, { compact: true });
    const watch = watchVideo(pageCheck(videoPageFor(m.page) || m.page), {
        onProgress: p => bar.update(p),
        onReady: result => {
            bar.stop();
            Object.assign(m, { src: result.video, pending: false });
            if (!box.isConnected || box.querySelector('img')) return;
            box.replaceChildren(thumbPlate('', 'Видео готово'), videoMark());
            box.dataset.page = m.page;
        },
        onFail: message => { bar.stop(); if (box.isConnected) box.replaceChildren(thumbPlate('', message), videoMark()); }
    });
    feed.watches.push(watch);
}

/** Строка поста: миниатюра слева (если есть медиа), текст 4 строки, под ним дата, просмотры, реакции, альбом, ссылка. */
function sourcePostCard(post, index) {
    const media = post.media || [];
    const url = safeSourcePostUrl({ source_url: post.url });
    const card = el('article', { className: `sp-card${media.length ? '' : ' is-text'}`, tabIndex: 0, role: 'button', ariaLabel: `Пост ${index + 1}: открыть` });
    const open = event => { if (event.target.closest('a')) return; event.stopPropagation(); openFeedPost(index); };
    card.addEventListener('click', open);
    card.addEventListener('keydown', event => { if ((event.key === 'Enter' || event.key === ' ') && event.target === card) { event.preventDefault(); openFeedPost(index); } });
    if (media.length) card.append(postThumb(media));
    const body = el('div', { className: 'sp-body' });
    const caption = cleanCaption(post.text);
    body.append(el('p', { className: caption && !media.length ? 'sp-text-big' : 'sp-text', textContent: shortText(caption, media.length ? 220 : 600) }));
    const stats = [post.views != null && `👁 ${COUNT.format(post.views)}`, post.reactions != null && `❤ ${COUNT.format(post.reactions)}`, post.comments != null && `💬 ${COUNT.format(post.comments)}`].filter(Boolean);
    body.append(el('div', { className: 'sp-meta' },
        el('span', { textContent: post.posted_at ? formatAgo(post.posted_at) : '' }),
        ...stats.map(text => el('span', { textContent: text })),
        media.length > 1 ? el('span', { textContent: `альбом · ${media.length}` }) : null,
        url ? el('a', { className: 'sp-link', href: url, target: '_blank', rel: 'noopener noreferrer', title: 'Открыть пост на площадке', textContent: '↗' }) : null));
    card.append(body);
    return card;
}

function openFeedPost(index) {
    const source = state.source || {};
    const st = source.stats || {};
    const views = feed.posts.map(p => p.views).filter(v => v != null).sort((a, b) => a - b);
    const medianViews = views.length >= 4 ? (views[(views.length - 1) >> 1] + views[views.length >> 1]) / 2 : null;   // меньше 4 постов — не оценка
    const meta = { name: source.name || '', url: safeSourcePostUrl({ source_url: st.url }), subscribers: st.subscribers, viewRate: st.view_rate, perWeek: st.posts_per_week, medianViews };
    const posts = feed.posts.map(post => fromSourcePost(post, meta));
    const rocket = post => ({ label: 'Запостить', icon: '🚀', primary: true, run: () => postFeedPost(feed.posts[posts.indexOf(post)], shownText(post)) });
    openPostView(posts, index, { title: meta.name, translate: true, actions: post => [rocket(post), { label: '', icon: '+🚀', setup: button => bindFutureDraftButton(button, () => feedItem(feed.posts[posts.indexOf(post)], shownText(post)), { click: false }), run: () => toggleFutureDraft(feedItem(feed.posts[posts.indexOf(post)], shownText(post))) }] });
}

/** 🚀 в просмотрщике: пост ленты источника → редактор поста (файлы без адреса, ещё не скачанные ролики и недоступные пропускаются). */
function feedItem(post, shown) {
    const files = (post?.media || []).filter(m => m.src && !m.pending && !m.unavailable);
    const gallery = files.map(m => m.kind === 'video' ? { image: m.poster || '', video: m.src } : { image: m.src });
    const text = String(shown ?? post?.text ?? '').trim();
    const item = { text, source_url: post?.url || '', description: '', ...(gallery.length ? { image: gallery[0].image || gallery[0].video, video: gallery[0].video || '', gallery } : { text_post: true }) };
    return item;
}
function postFeedPost(post, shown) {
    const item = feedItem(post, shown);
    if (!item.image && !item.text) { log('В этом посте нечего публиковать: нет ни текста, ни готовых файлов.'); return; }
    closePostView();
    return postItemToTelegram(item).catch(e => log(`Не удалось открыть редактор: ${e.message}`));
}

/** Показать ещё постов ленты (по FEED_FIRST за раз). */
function showFeed(count) {
    const from = feed.shown;
    feed.shown = Math.min(feed.posts.length, from + count);
    $('source-posts').append(...feed.posts.slice(from, feed.shown).map((post, i) => sourcePostCard(post, from + i)));
    const left = feed.posts.length - feed.shown;
    $('source-posts-more').hidden = left <= 0;
    $('source-posts-more').textContent = `Ещё ${left} ${pluralRu(left, 'пост', 'поста', 'постов')}`;
}

/** Лента кандидата: сброс прошлого (видео, докачки), первые постов, кнопка «Ещё». */
function renderFeed(source) {
    for (const watch of feed.watches ?? []) watch.stop();
    feed.watches = [];
    feed.posts = feedPosts(source);
    feed.shown = 0;
    feed.token = Symbol('feed');
    $('source-posts').replaceChildren();
    const empty = !feed.posts.length;
    $('source-media-fallback').textContent = 'Постов с медиа или текстом пока нет.';
    show('source-media-fallback', empty);
    $('source-posts-more').hidden = true;
    if (!empty) showFeed(FEED_FIRST);
}

/** Строка охвата под карточкой: сколько постов показано из скольких и сколько страниц канала просмотрено. */
function renderCoverage(source) {
    const s = source.stats || {};
    const line = $('source-coverage');
    const withMedia = feed.posts.filter(p => p.media.length).length;
    line.hidden = !feed.posts.length;
    if (line.hidden) return;
    const total = s.posts_total ?? (source.posts || []).length;
    line.textContent = `Показано ${feed.posts.length} ${pluralRu(feed.posts.length, 'пост', 'поста', 'постов')} из ${total} · из них с медиа ${withMedia}` + (s.pages != null ? ` · страниц канала ${s.pages}` : '');
}

/** Ролик, который уже скачал просмотрщик (video-ready): миниатюра строки получает значок готового ролика. */
window.addEventListener('video-ready', event => {
    const { page, local } = event.detail || {};
    if (!page || !local) return;
    for (const box of $('source-posts').querySelectorAll('.sp-thumb')) {
        if (box.dataset.page !== page) continue;
        box.replaceChildren(thumbPlate('', 'Видео готово'), videoMark());
    }
});

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

/** Итог под карточкой: сколько медиа показано из скольких постов канала и сколько страниц просмотрено. */
function renderSource(source) {
    state.source = source;
    renderFeed(source);
    const s = source.stats || {};
    const url = safeSourcePostUrl({ source_url: s.url });
    $('source-name').replaceChildren('Источник: ', url ? el('a', { className: 'source-name-link', href: url, target: '_blank', rel: 'noopener noreferrer', textContent: source.name }) : source.name);
    $('source-platform-label').textContent = (source.platform_label || source.kind || source.platform) + (source.suggested ? ' · найден автоподбором' : '') + (['Двач', '4chan'].includes(source.platform) ? ` · Тред по теме: ${qualityText(source.quality)} · Доска по теме: ${qualityText(source.board_quality)}` : ' · Оценки постов источника: ' + qualityText(source.quality));
    setMessage('source-score', source.score != null ? `${source.score}%` : '');
    renderStats(s);
    renderCoverage(source);
    if (url) $('source-link').href = url; else $('source-link').removeAttribute('href');
    $('source-link').hidden = !url;
    $('source-link').textContent = ['Двач', '4chan'].includes(source.platform) ? 'Открыть тред ↗' : 'Открыть источник ↗';
    $('source-board-note').hidden = !['Двач', '4chan'].includes(source.platform);
    $('source-why').textContent = (source.reason || 'Кандидат из каталога источников.') + (['Двач', '4chan'].includes(source.platform) ? ' Одобрение добавит этот тред; доска используется для поиска других тредов.' : '');
    $('source-desc').replaceChildren(el('strong', { textContent: 'Описание: ' }), s.description || '');
    $('source-desc').hidden = !s.description;
    clearTimeout(state.poll);
    showArea('card');
    log(`Показан источник: ${source.name}${source.score != null ? `, оценка подбора ${source.score}%` : ''}.`);
}

/** Кнопки пустого состояния по действию сервера: поиск, подключение VK или добавление ссылки. */
function setEmptyActions(action) {
    show('source-empty-discover', action === 'discover');
    show('source-empty-vk', action === 'connect_vk');
    show('source-empty-add', action === 'add_link');
}

function showArea(area, message = '', action = state.emptyAction) {
    show('source-card-wrapper', area === 'card');
    show('source-empty', area === 'empty');
    setMessage('source-error', area === 'error' ? message : '');
    hide('source-loading');
    if (area !== 'empty') return;
    setEmptyActions(action);
    $('source-empty-message').textContent = (message || 'Новых источников пока нет.') + (state.discovering ? ' Подбор идёт: найденные источники появятся здесь по одному.' : '');
    clearTimeout(state.poll);
    // Пока подбор идёт, а карточки нет, раз в 10 с тихо проверяем готовые находки
    if (state.discovering) state.poll = setTimeout(async () => {
        if (isTabActive('source-tab') && !state.source && !state.loading) await loadNextSource({ quiet: true });
        if (!state.source) showArea('empty', message, action);
    }, 10000);
}

export function clearDiscoveredSource() { Object.assign(state, { source: null, skipped: [] }); hide('source-card-wrapper'); }

async function loadNextSource({ quiet = false } = {}) {
    if (state.loading) return;
    state.loading = true;
    const request = ++state.request;
    if (!quiet) { state.source = null; hide('source-card-wrapper'); hide('source-empty'); setMessage('source-error', ''); show('source-loading'); }
    let timer;
    try {
        const data = await Promise.race([
            api('suggest_sources', { body: { platform: $('source-platform').value, skip: state.skipped, ...(quiet ? { suggested_only: true } : {}) } }),
            new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('поиск занимает слишком много времени, попробуйте ещё раз')), 30000); })
        ]);
        if (request !== state.request) return;
        if (data.discovered) renderSource(data.discovered);
        else if (!quiet) { state.emptyAction = data.action || ''; showArea('empty', data.message, state.emptyAction); }
    } catch (error) { if (!quiet && request === state.request) showArea('empty', `Не удалось загрузить источник: ${error.message}`); }
    finally { clearTimeout(timer); if (request === state.request) state.loading = false; }
}

async function vote(type, reason = '') {
    const source = state.source;
    if (!source || state.voting) return;
    if (type === 'postpone') {
        state.voting = true;
        try {
            await api('source_postpone', { body: { source } });
            state.skipped.push(source.source_id);
            log(`Источник отложен на 7 дней: ${source.name}`);
            await loadNextSource();
        } catch (error) { setMessage('source-error', `Не удалось отложить источник: ${error.message}`); }
        finally { state.voting = false; }
        return;
    }
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
            `Ручной подбор (отдельный механизм): всего найдено в каталоге ${d.auto_added}, ждут вашего решения ${d.suggestions}. Это накопленный итог, а не находки AI-автопилота за один запуск.`,
            d.batch_target ? `Подбор: ${d.batch_found} из ${d.batch_target}, в очереди ${d.batch_pending}.${d.batch_active && !d.running ? " Поиск продолжится в фоне." : ""}` : '',
            d.frozen ? `Заморожено из-за редких постов: ${d.frozen} (перепроверяются раз в несколько дней).` : '',
            d.requested ? 'Подбор заказан — начнётся при запуске парсера.' : '', d.vk_connected ? '' : 'Открытые сообщества VK доступны без подключения; для поиска через VK API нужен ключ.'].filter(Boolean).join(' ');
    } catch (error) { $('discovery-status').textContent = `Сведения недоступны: ${error.message}`; }
}

async function startDiscovery() {
    $('discovery-button').disabled = true;
    try {
        log((await api('discover_sources', { body: { platform: $('discovery-platform').value } })).message);
        state.discovering = true;
        if (!state.source) showArea('empty');
        watchParser();
    } catch (error) { setMessage('source-error', `Автоподбор не запущен: ${error.message}`); }
    finally { $('discovery-button').disabled = false; loadDiscoveryStatus(); }
}

function renderAutopilot(data) {
    const container = $('autopilot-topics');
    container.replaceChildren();
    const backends = (data.backends || []).filter(backend => backend.enabled).map(backend => backend.name).join(', ') || 'не включены';
    $('autopilot-status').textContent = `Теневой режим на сервере: ${data.shadow_enabled ? 'включён' : 'выключен'}; автоматический: ${data.auto_enabled ? 'включён' : 'выключен'}. Настроенные площадки: ${backends}.`;
    for (const topic of data.topics || []) {
        const number = value => Number(value || 0).toLocaleString('ru-RU');
        const dateTime = stamp => stamp ? new Date(stamp * 1000).toLocaleString('ru-RU') : 'ещё нет';
        const failureNames = {
            budget_exhausted: 'Исчерпан бюджет одного из ресурсов во время прошлого цикла',
            news_search_disabled: 'Поиск источников отключён',
            news_search_unavailable: 'Новостной поиск недоступен',
            search_unavailable: 'Поисковые площадки не ответили',
            topic_busy: 'Тема занята другим процессом',
            supply_unavailable: 'Не удалось проверить запас',
            classifier_unavailable: 'Модель анализа недоступна',
            restricted_profile: 'Тема не допускает публичный поиск',
            other_error: 'Последний цикл завершился с ошибкой'
        };
        const disabled = topic.mode === 'off' || topic.paused || topic.job_paused ||
            !(topic.mode === 'auto' ? data.auto_enabled : data.shadow_enabled);
        const activePhase = topic.running ? topic.pipeline_phase : '';
        const phaseLabels = { prepare: 'Подготовка', search: 'Поиск по площадкам', review: 'Проверка источников', delivery: 'Доставка в пул' };
        const status = disabled ? (topic.paused ? 'На паузе' : 'Выключен')
            : topic.running ? 'Работает: ' + (phaseLabels[activePhase] || 'запуск цикла')
            : topic.failure ? 'Ожидает повтора после ошибки'
            : topic.mode === 'auto' && topic.available >= topic.target && topic.target > 0 ? 'Запас собран'
            : 'Ожидает следующего цикла';
        const sourceCandidates = topic.mode === 'auto' ? topic.auto_candidates : topic.shadow_candidates;
        const sourcesReview = topic.mode === 'auto' ? topic.auto_sources_probation : topic.shadow_probation;
        const sourcesActive = topic.mode === 'auto' ? topic.auto_sources_active : topic.shadow_confirmed;
        const stat = (label, value, hint) => el('div', { className: 'autopilot-metric' },
            el('span', { className: 'autopilot-metric-value', textContent: number(value) }),
            el('span', { className: 'autopilot-metric-label', textContent: label }),
            hint ? el('small', { textContent: hint }) : '');
        const phase = (name, value, detail, state) => el('div', { className: 'autopilot-phase' },
            el('span', { className: 'autopilot-phase-dot ' + state, 'aria-hidden': 'true' }),
            el('div', {}, el('strong', { textContent: name }), el('span', { textContent: value }),
                el('small', { textContent: detail })));
        const found = Number(topic.last_source_candidates || 0);
        const totalChecked = Number(topic.direct_reviewed_total || 0);
        const failure = failureNames[topic.failure];
        const staleError = failure ? el('p', { className: 'autopilot-alert', textContent:
            'Последняя ошибка: ' + failure + '. Это история предыдущего цикла, а не обязательно текущий лимит.' }) : '';
        const description = el('div', { className: 'autopilot-overview' },
            el('div', { className: 'autopilot-overview-head' },
                el('strong', { textContent: topic.topic }),
                el('span', { className: 'autopilot-state' + (topic.running ? ' is-running' : ''), textContent: status })),
            el('div', { className: 'autopilot-metrics' },
                stat('Найдено за 1 цикл', found, 'Автопилот; не сумма находок ручного подбора'),
                stat('Ожидают проверки', sourceCandidates, 'Сохранено в очереди'),
                stat('На испытании', sourcesReview, 'Проверка качества'),
                stat('Подтверждено', sourcesActive, 'Действующие источники')),
            el('div', { className: 'autopilot-pipeline', 'aria-label': 'Этапы подбора источников' },
                phase('1. Поиск', topic.last_search_at ? number(found) + ' кандидатов' : 'Нет данных',
                    topic.last_search_at ? 'Последний поиск: ' + dateTime(topic.last_search_at) +
                        ' · площадки: ' + number(topic.last_backends_ok) + ' с результатами, ' +
                        number(topic.last_backends_empty) + ' пустых, ' + number(topic.last_backends_unavailable) + ' недоступны'
                        : 'Поиск ещё не проводился', activePhase === 'search' ? 'active' : topic.last_search_at ? 'done' : 'waiting'),
                phase('2. Проверка источников', number(sourceCandidates) + ' в очереди · ' + number(sourcesReview) + ' на испытании',
                    'Источник подключается после проверки качества; найденный кандидат ещё не одобрен.',
                    activePhase === 'review' ? 'active' : sourcesActive > 0 ? 'done' : sourcesReview > 0 ? 'done' : 'waiting'),
                phase('3. AI-отбор постов', number(totalChecked) + ' проверено',
                    number(topic.direct_accepted_total) + ' принято · ' + number(topic.direct_rejected_total) +
                        ' отклонено. Счётчики относятся к прямому поиску постов.',
                    totalChecked > 0 ? 'done' : 'waiting'),
                phase('4. Контент в пуле', number(topic.available) + ' из ' + number(topic.target) + ' постов',
                    topic.capacity_limited ? 'Цель ограничена вместимостью пула.' :
                        'Это текущий запас публикаций по теме, не число найденных кандидатов.',
                    activePhase === 'delivery' ? 'active' : topic.available > 0 ? 'done' : 'waiting')),
            el('div', { className: 'autopilot-footnotes' },
                el('span', { textContent: 'Сегодня осталось: ' + number(topic.fetch_remaining) +
                    ' HTTP-загрузок · ' + number(topic.model_remaining) + ' AI-вызовов по теме' }),
                el('span', { textContent: 'Сервер: ' + number(topic.server_fetch_remaining ?? 0) +
                    ' HTTP · ' + number(topic.server_model_remaining ?? 0) + ' AI' }),
                el('span', { textContent: 'Запусков задания: ' + number(topic.attempts) +
                    (topic.pipeline_phase_at ? ' · последний этап: ' + dateTime(topic.pipeline_phase_at) : '') }),
                el('span', { textContent: topic.running ? 'Текущий цикл выполняется' :
                    'Следующая проверка: ' + dateTime(topic.next_run_at) })),
            staleError);
        const modeSelect = el('select');
        modeSelect.setAttribute('aria-label', `Режим для темы ${topic.topic}`);
        for (const [value, label] of [['off', 'Выключен'], ['shadow', 'Теневой'], ['auto', 'Автоматический']]) {
            const option = el('option', { value, textContent: label });
            option.selected = topic.mode === value;
            modeSelect.append(option);
        }
        modeSelect.addEventListener('change', async () => {
            modeSelect.disabled = true;
            try {
                const result = await api('source_autopilot_demand', { body: { topic: topic.topic, mode: modeSelect.value, posts_per_day: topic.posts_per_day, horizon_days: topic.horizon_days } });
                renderAutopilot(result);
            } catch (error) { $('autopilot-status').textContent = `Настройки не сохранены: ${error.message}`; modeSelect.value = topic.mode; modeSelect.disabled = false; }
        });
        const history = el('details', {}, el('summary', { textContent: 'История решений' }));
        history.hidden = topic.mode === 'off';
        const historyList = el('div', { textContent: 'Откройте историю, чтобы загрузить решения.' });
        const more = el('button', { className: 'secondary-button', type: 'button', textContent: 'Показать ещё', hidden: true });
        history.append(historyList, more);
        let historyLoaded = false, historyLoading = false, before = 0;
        const loadHistoryPage = async () => {
            if (historyLoading) return;
            historyLoading = true;
            more.disabled = true;
            try {
                const result = await api('source_autopilot_history', { params: { topic: topic.topic, mode: topic.mode, before, limit: 20 } });
                if (!historyLoaded) historyList.replaceChildren();
                historyLoaded = true;
                for (const decision of result.decisions || []) {
                    const states = { candidate: 'кандидат', probation: 'испытание', active: 'подтверждён', rejected: 'отклонён', cooldown: 'ожидание' };
                    const reasons = { discovered: 'найден', shadow_preliminary_pass: 'прошёл предварительную проверку', observed_useful: 'подтверждена полезность', observed_ad_or_duplicates: 'слишком много рекламы или дублей', observed_irrelevant: 'недостаточно материалов по теме', quality_drift: 'качество снизилось' };
                    const text = `${decision.source_id}: ${states[decision.old_state] || 'найден'} → ${states[decision.new_state] || decision.new_state}; ${reasons[decision.reason] || decision.reason}. ${formatAgo(decision.at)}.`;
                    historyList.append(el('p', { textContent: text }));
                }
                if (!before && !(result.decisions || []).length) historyList.textContent = 'Решений пока нет.';
                before = result.next_before || 0;
                more.hidden = !before;
            } catch (error) { $('autopilot-status').textContent = `История недоступна: ${error.message}`; }
            finally { historyLoading = false; more.disabled = false; }
        };
        more.addEventListener('click', loadHistoryPage);
        history.addEventListener('toggle', () => {
            if (history.open && !historyLoaded && topic.mode !== 'off') loadHistoryPage();
        });
        const request = el('button', { className: 'secondary-button', type: 'button', textContent: 'Запросить проверку', disabled: topic.mode === 'off' || topic.paused });
        request.addEventListener('click', async () => {
            request.disabled = true;
            try {
                renderAutopilot(await api('source_autopilot_request', { body: { topic: topic.topic } }));
                $('autopilot-status').textContent = 'Проверка запрошена. Срок повтора и суточные лимиты сохранены.';
            } catch (error) { $('autopilot-status').textContent = `Не удалось запросить проверку: ${error.message}`; request.disabled = false; }
        });
        const pause = el('button', { className: 'secondary-button', type: 'button', textContent: topic.paused ? 'Продолжить' : 'Пауза', disabled: topic.mode === 'off' });
        pause.addEventListener('click', async () => {
            pause.disabled = true;
            try { renderAutopilot(await api('source_autopilot_pause', { body: { topic: topic.topic, paused: !topic.paused } })); }
            catch (error) { $('autopilot-status').textContent = `Не удалось изменить паузу: ${error.message}`; pause.disabled = false; }
        });
        container.append(el('div', { className: 'autopilot-topic-row' }, description,
            el('div', { className: 'autopilot-topic-actions' }, modeSelect, pause, request), history));
    }
}

async function loadAutopilot() {
    if (DEBUG_MODE) { $('autopilot-status').textContent = 'В демо-режиме проверка недоступна.'; return; }
    try { renderAutopilot(await api('source_autopilot_status')); }
    catch (error) { $('autopilot-status').textContent = `Сведения недоступны: ${error.message}`; }
}

async function saveAutopilot(event) {
    event.preventDefault();
    const button = event.currentTarget.querySelector('button');
    button.disabled = true;
    try {
        const result = await api('source_autopilot_demand', { body: { topic: $('autopilot-topic').value.trim(), mode: $('autopilot-mode').value, posts_per_day: Number($('autopilot-rate').value), horizon_days: Number($('autopilot-horizon').value) } });
        renderAutopilot(result);
    } catch (error) { $('autopilot-status').textContent = `Настройки не сохранены: ${error.message}`; }
    finally { button.disabled = false; }
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

/** Карточка поста поверх карточки источника: лёгкая подложка, карточка смещена, источник виден под ней. */
let sourcePreview = null;

function closeSourcePreview() {
    if (!sourcePreview) return;
    document.removeEventListener('keydown', sourcePreview.onKey);
    window.removeEventListener('resize', sourcePreview.place);
    sourcePreview.overlay.remove();
    sourcePreview = null;
}

function openSourcePreview(source, index, anchorOf) {
    closeSourcePreview();
    const overlay = el('div', { className: 'source-preview-overlay', onclick: e => { if (e.target === overlay) closeSourcePreview(); } });
    const shown = { index: 0 };
    const counter = el('span', { className: 'source-preview-counter' });
    const img = el('img', { alt: source.name, loading: 'lazy', decoding: 'async', referrerPolicy: 'no-referrer', onload: () => place() });
    const step = delta => setIndex(shown.index + delta);
    const setIndex = i => {
        shown.index = (i + source.preview.length) % source.preview.length;
        img.src = source.preview[shown.index];
        counter.textContent = source.preview.length > 1 ? `${shown.index + 1} из ${source.preview.length}` : '';
    };
    const card = el('div', { className: 'source-preview-card', role: 'dialog', ariaLabel: `Пост источника «${source.name}»` },
        el('button', { type: 'button', className: 'source-preview-close', textContent: '✕', ariaLabel: 'Закрыть', onclick: closeSourcePreview }),
        img,
        el('div', { className: 'source-preview-foot' },
            source.preview.length > 1 ? el('button', { type: 'button', className: 'source-preview-nav', textContent: '←', ariaLabel: 'Предыдущий', onclick: () => step(-1) }) : null,
            el('span', { className: 'source-preview-name', textContent: source.name }),
            el('a', { href: source.url, target: '_blank', rel: 'noopener noreferrer', textContent: 'Открыть пост ↗' }),
            counter,
            source.preview.length > 1 ? el('button', { type: 'button', className: 'source-preview-nav', textContent: '→', ariaLabel: 'Следующий', onclick: () => step(1) }) : null));
    overlay.append(card);
    document.body.append(overlay);
    // Положение: от правого-нижнего угла карточки источника; на узком экране — по всей ширине
    const place = () => {
        const rect = anchorOf().getBoundingClientRect();
        const narrow = innerWidth <= 600;
        const width = narrow ? innerWidth - 24 : Math.min(rect.width * 0.85, 420);
        const left = Math.max(8, Math.min(narrow ? 12 : rect.left + 24, innerWidth - width - 8));
        const top = Math.max(8, Math.min(narrow ? rect.top + 12 : rect.top + 24, innerHeight - card.offsetHeight - 8));
        Object.assign(card.style, { width: `${width}px`, left: `${left}px`, top: `${top}px` });
    };
    const onKey = e => { if (e.key === 'Escape') closeSourcePreview(); };
    sourcePreview = { overlay, onKey, place };
    document.addEventListener('keydown', onKey);
    window.addEventListener('resize', place);
    setIndex(index);
    place();
}

// Названия старых RSS-источников уточняются по мере появления карточек на экране.
const topicTitles = { observer: null, queue: [], active: 0, generation: 0 };
function queueTopicTitle(card, source, generation) {
    topicTitles.queue.push({ card, source, generation });
    while (topicTitles.active < 2 && topicTitles.queue.length) {
        const job = topicTitles.queue.shift();
        if (job.generation !== topicTitles.generation || !job.card.isConnected) continue;
        topicTitles.active++;
        let timer;
        Promise.race([
            api('rss_source_title', { body: { source_id: job.source.source_id } }),
            new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('Источнику нужно больше времени')), 35000); })
        ]).then(data => {
            if (job.generation !== topicTitles.generation || !job.card.isConnected || !data.name) return;
            job.source.name = data.name;
            job.source.needs_title = false;
            const node = job.card.querySelector('.source-head-text strong');
            if (node) { node.textContent = data.name; node.title = data.name; }
        }).catch(() => {}).finally(() => { clearTimeout(timer); topicTitles.active--; drainTopicTitles(); });
    }
}
function drainTopicTitles() {
    if (!topicTitles.queue.length || topicTitles.active >= 2) return;
    const job = topicTitles.queue.shift();
    queueTopicTitle(job.card, job.source, job.generation);
}

function topicSourceCard(source) {
    const cover = () => el('div', { className: 'source-cover' }, avatar(source));
    const slides = source.preview.map(src => ({ kind: 'image', src, poster: src, sourceUrl: source.url, alt: source.name }));
    let media = cover();
    if (slides.length) {
        media = el('div', { className: `proposal-media mosaic-${slides.length}` });
        slides.forEach((slide, i) => {
            const tile = el('button', { className: 'proposal-tile is-loading', type: 'button', title: 'Открыть пост', onclick: e => { e.stopPropagation(); openSourcePreview(source, i, () => card); } },
                el('img', { src: slide.src, alt: '', loading: 'lazy', decoding: 'async', referrerPolicy: 'no-referrer',
                    onload: () => tile.classList.remove('is-loading'),
                    onerror: () => { tile.remove(); const left = media.children.length; left ? media.className = `proposal-media mosaic-${left}` : media.replaceWith(cover()); } }));
            media.append(tile);
        });
    }
    const chips = [source.subscribers > 0 && chip('подписчики', COUNT.format(source.subscribers)), source.view_rate != null && chip('просмотры', `${Math.round(source.view_rate * 100)}%`, 'Какая доля подписчиков видит пост'),
        source.score != null && chip('оценка', `${source.score}%`, 'Оценка автоподбора: по теме, аудитория, доля просмотров')].filter(Boolean);
    const stateLabels = { candidate: 'кандидат', probation: 'на испытании', active: 'подтверждён', rejected: 'отклонён', cooldown: 'ожидает проверки' };
    const healthLabels = { rate_limited: 'площадка ограничила запросы', feed_unavailable: 'лента временно недоступна', source_not_found: 'лента не найдена', assessment_unavailable: 'оценка временно недоступна', insufficient_evidence: 'мало наблюдений', probation_full: 'места на испытании заняты' };
    const evidence = source.autopilot?.evidence;
    const observation = evidence?.evaluated ? `оценено постов: ${evidence.evaluated}, по теме: ${evidence.relevant ?? 0}, проходов: ${evidence.successful_passes ?? 0}` : '';
    const notes = [!source.own && (source.topics.length ? `из общей темы: ${source.topics.join(', ')}` : 'источник для всех тем'), source.auto && 'найден автоподбором', stateLabels[source.autopilot?.state], healthLabels[source.autopilot?.reason], observation].filter(Boolean);
    const card = el('article', { className: `proposal-card source-tile-card${source.frozen ? ' is-frozen' : ''}` }, media,
        el('div', { className: 'source-head' }, avatar(source), el('div', { className: 'source-head-text' }, el('strong', { textContent: source.name, title: source.name }),
            el('a', { href: source.url, target: '_blank', rel: 'noopener noreferrer', textContent: source.handle })),
            el('span', { className: 'source-platform-tag', textContent: source.platform_label || source.kind || { 'ВКонтакте': 'VK', 'Одноклассники': 'ОК', 'TikTok': 'TikTok' }[source.platform] || source.platform })),
        chips.length ? el('div', { className: 'source-chips' }, ...chips) : null,
        el('span', { className: `source-status${source.frozen ? ' is-frozen' : ''}`, textContent: ['Двач', '4chan'].includes(source.platform) ? 'тред имиджборда'
            : source.frozen ? `заморожен: ${source.frozen_reason || 'не активен'}` : source.activity || 'активен' }),
        notes.length ? el('p', { className: 'source-note', textContent: notes.join(' · ') }) : null);
    const actions = el('div', { className: 'proposal-actions' }, el('a', { className: 'proposal-action', href: source.url, target: '_blank', rel: 'noopener noreferrer', textContent: 'Открыть ↗' }));
    if (!DEBUG_MODE && ['RSS', 'Telegram', 'ВКонтакте', 'Одноклассники', 'TikTok'].includes(source.platform)) actions.append(el('button', { className: 'proposal-action', type: 'button', textContent: source.pinned ? 'Открепить' : 'Закрепить для меня', onclick: async () => {
        try {
            await api('source_personal_pin', { body: { source_id: source.source_id, pinned: !source.pinned } });
            source.pinned = !source.pinned;
            if (!source.pinned) await loadTopicSources();
            else renderTopicSources();
        } catch (error) { log(`Не удалось изменить закрепление: ${error.message}`); }
    } }));
    if (source.muted) {   // выключен по оценкам пользователя (TOPICS_SEARCH_PROMPT.md, часть F)
        card.classList.add('is-frozen');
        card.querySelector('.source-status')?.replaceChildren(document.createTextNode('выключен по вашим оценкам'));
        actions.append(el('button', { className: 'proposal-action', type: 'button', textContent: 'Включить', onclick: async () => {
            try {
                await api('source_mute', { body: { source_id: source.source_id, on: true } });
                source.muted = false;
                log(`Источник «${source.name}» снова в подборе этой темы.`);
                renderTopicSources();
            } catch (error) { log(`Не удалось включить источник: ${error.message}`); }
        } }));
    } else if (!DEBUG_MODE) {
        actions.append(el('button', { className: 'proposal-action', type: 'button', textContent: 'Исключить для меня', onclick: async () => {
            try {
                await api('source_mute', { body: { source_id: source.source_id, on: false } });
                source.muted = true;
                renderTopicSources();
            } catch (error) { log(`Не удалось исключить источник: ${error.message}`); }
        } }));
    }
    if (!DEBUG_MODE && source.own && !source.autopilot_managed) actions.append(el('button', { className: 'proposal-action proposal-hide', type: 'button', textContent: '✕ Убрать', title: `Убрать из темы «${topicSources.topic}»`, onclick: async () => {
        if (!(await ask('Посты из него перестанут попадать в подбор по этой теме.', { title: `Убрать «${source.name}» из темы «${topicSources.topic}»?`, ok: 'Убрать', danger: true }))) return;
        card.classList.add('is-removing');
        try {
            await api('remove_topic_source', { body: { platform: source.platform, id: source.id, name: source.name } });
            topicSources.sources = topicSources.sources.filter(o => o !== source);
            log(`Источник «${source.name}» убран из темы «${topicSources.topic}».`);
            renderTopicSources();
        } catch (error) { card.classList.remove('is-removing'); log(`Не удалось убрать источник: ${error.message}`); }
    } }));
    card.append(actions);
    if (source.needs_title) {
        card._titleSource = source;
        if (topicTitles.observer) topicTitles.observer.observe(card);
        else requestAnimationFrame(() => queueTopicTitle(card, source, topicTitles.generation));
    }
    return card;
}

function renderTopicSources() {
    const { sources, topic, filter } = topicSources;
    const visible = sources.filter(s => filter === 'all' || s.platform === filter);
    $('topic-sources-title').textContent = topic ? `Источники темы «${topic}»` : 'Источники темы';
    $('topic-sources-count').textContent = sources.length ? `${visible.length} из ${sources.length}` : '';
    $('topic-sources-status').textContent = visible.length ? '' : sources.length ? 'На этой площадке источников темы нет.' : 'У темы пока нет источников: нажмите «Подобрать источники» или одобряйте предложенные выше.';
    const generation = ++topicTitles.generation;
    topicTitles.observer?.disconnect();
    topicTitles.queue = [];
    topicTitles.observer = 'IntersectionObserver' in window ? new IntersectionObserver(entries => {
        for (const entry of entries) if (entry.isIntersecting && entry.target._titleSource) {
            topicTitles.observer?.unobserve(entry.target);
            queueTopicTitle(entry.target, entry.target._titleSource, generation);
        }
    }, { rootMargin: '250px' }) : null;
    $('topic-sources-grid').replaceChildren(...visible.map(topicSourceCard));
    document.querySelectorAll('[data-topic-source-filter]').forEach(b => b.classList.toggle('active', b.dataset.topicSourceFilter === filter));
}

async function loadTopicSources() {
    if (!$('topic-sources-section').open || topicSources.loading) return;
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

// ---- Свой контент: картинки и CSV сразу в подборку ----

/** Отправка файлов; итог — строка статуса и ошибки списком (textContent). */
async function sendOwn(action, form, button) {
    const [status, errors] = [$('own-content-status'), $('own-content-errors')];
    button.disabled = true;
    status.textContent = 'Загружаю…';
    errors.replaceChildren();
    try {
        const result = await api(action, { body: form });
        status.textContent = `Добавлено в подборку: ${result.added}` + (result.skipped ? `, пропущено: ${result.skipped}` : '');
        errors.replaceChildren(...(result.errors || []).map(text => el('li', { textContent: text })));
        if (result.added) { log(`Свой контент: добавлено в подборку — ${result.added}.`); loadCurrentItem(); }
    } catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; }
}

/** Образец CSV собирается на клиенте. */
function downloadCsvSample(event) {
    event.preventDefault();
    const text = '\uFEFFtext;image\r\nПодпись к первому посту;https://example.com/photo1.jpg\r\nВторой пост с двумя картинками;https://example.com/photo2.jpg|https://example.com/photo3.jpg\r\n';
    const link = el('a', { href: URL.createObjectURL(new Blob([text], { type: 'text/csv;charset=utf-8' })), download: 'posta-example.csv' });
    link.click();
    URL.revokeObjectURL(link.href);
}

function initializeOwnContent() {
    const [files, csv] = [$('own-files'), $('own-csv')];
    $('own-upload-button').addEventListener('click', () => files.click());
    $('own-csv-button').addEventListener('click', () => csv.click());
    $('own-csv-sample').addEventListener('click', downloadCsvSample);
    files.addEventListener('change', async () => {
        if (!files.files.length) return;
        const selected = [...files.files];
        $('own-upload-button').disabled = true;
        $('own-content-status').textContent = 'Подготавливаю картинки на вашем устройстве…';
        const form = new FormData();
        for (const file of selected) form.append('files[]', await prepareOwnImage(file));
        form.append('text', $('own-text').value.trim());
        form.append('album', $('own-album').checked ? '1' : '0');
        sendOwn('own_upload', form, $('own-upload-button')).finally(() => { files.value = ''; });
    });
    csv.addEventListener('change', () => {
        if (!csv.files.length) return;
        const form = new FormData();
        form.append('csv', csv.files[0]);
        sendOwn('own_import_csv', form, $('own-csv-button')).finally(() => { csv.value = ''; });
    });
}

// ---- Лучшие посты источника: поиск по ссылке, выбор и забор в подборку ----

const BEST_POSTS_TAKE_MAX = 20;   // как на сервере
const BEST_SORT_LABELS = { date: 'по дате', likes: 'по лайкам', reposts: 'по репостам', comments: 'по комментариям', views: 'по просмотрам', viral: 'по виральности' };
const bestPosts = { key: '', posts: [], selected: new Set() };

function updateBestPostsButton() {
    const take = $('best-posts-take');
    take.textContent = `Забрать в подборку (${bestPosts.selected.size})`;
    take.disabled = bestPosts.selected.size === 0;
}

/** Отметка поста; сверх лимита за раз отметка снимается. */
function toggleBestPost(token, check) {
    if (check.checked && bestPosts.selected.size >= BEST_POSTS_TAKE_MAX) {
        check.checked = false;
        $('best-posts-status').textContent = `За раз — не больше ${BEST_POSTS_TAKE_MAX} постов.`;
        return;
    }
    check.checked ? bestPosts.selected.add(token) : bestPosts.selected.delete(token);
    updateBestPostsButton();
}

function bestPostCard(post) {
    const check = el('input', { type: 'checkbox', checked: bestPosts.selected.has(post.token) });
    check.addEventListener('change', () => toggleBestPost(post.token, check));
    const stats = [post.likes != null && `♥ ${COUNT.format(post.likes)}`, post.comments != null && `💬 ${COUNT.format(post.comments)}`,
        post.reposts != null && `↪ ${COUNT.format(post.reposts)}`, post.views != null && `👁 ${COUNT.format(post.views)}`, post.viral > 0 && `виральность ${post.viral}`];
    const date = post.posted_at ? new Date(post.posted_at * 1000).toLocaleDateString('ru-RU') : '';
    const link = safeSourcePostUrl({ source_url: post.source_url, source_key: post.source_key });
    return el('label', { className: 'proposal-card best-post' },
        el('img', { className: 'best-post-image', src: post.image, alt: '', loading: 'lazy', decoding: 'async', referrerPolicy: 'no-referrer' }),
        el('span', { className: 'best-post-meta' }, check, el('span', { textContent: [date, ...stats].filter(Boolean).join(' · ') })),
        el('p', { className: 'best-post-text', textContent: post.text }),
        link && el('a', { href: link, target: '_blank', rel: 'noopener noreferrer', textContent: 'Открыть пост ↗' }));
}

function renderBestPosts() {
    $('best-posts-grid').replaceChildren(...bestPosts.posts.map(bestPostCard));
    $('best-posts-actions').hidden = bestPosts.posts.length === 0;
    updateBestPostsButton();
}

/** Сортировки — только те, что умеет площадка источника. */
function fillBestSorts(sorts, current) {
    const select = $('best-posts-sort');
    select.replaceChildren(...sorts.map(s => el('option', { value: s, textContent: BEST_SORT_LABELS[s] || s })));
    select.value = sorts.includes(current) ? current : 'date';
}

async function searchBestPosts(event) {
    event?.preventDefault();
    const [link, days, sort, type, phrase] = [$('best-posts-link').value.trim(), $('best-posts-days').value, $('best-posts-sort').value, $('best-posts-type').value, $('best-posts-phrase').value.trim()];
    if (!link) return;
    const [button, status] = [$('best-posts-button'), $('best-posts-status')];
    button.disabled = true;
    status.textContent = 'Ищу лучшие посты…';
    try {
        const data = await api('best_posts', { params: { link, days, sort, type, phrase } });
        Object.assign(bestPosts, { key: data.key, posts: data.posts, selected: new Set() });
        fillBestSorts(data.sorts, data.sort);
        status.textContent = data.posts.length ? `${data.source.name}: постов — ${data.posts.length}` : 'За этот период подходящих постов нет.';
    } catch (error) {
        Object.assign(bestPosts, { posts: [], selected: new Set() });
        status.textContent = error.message;
    } finally {
        button.disabled = false;
        renderBestPosts();
    }
}

function selectAllBestPosts() {
    bestPosts.selected = new Set(bestPosts.posts.slice(0, BEST_POSTS_TAKE_MAX).map(p => p.token));
    renderBestPosts();
}

/** Выбранные посты — в подборку; повторы и нескачанные картинки пропускаются. */
async function takeBestPosts() {
    const [button, status] = [$('best-posts-take'), $('best-posts-status')];
    button.disabled = true;
    status.textContent = 'Забираю в подборку…';
    const picked = bestPosts.selected.size;
    try {
        const result = await api('best_posts_take', { body: { key: bestPosts.key, tokens: [...bestPosts.selected] } });
        status.textContent = `Добавлено в подборку: ${result.added}` + (result.added < picked ? ' (часть уже была в подборке или не скачалась)' : '');
        bestPosts.selected = new Set();
        renderBestPosts();
        if (result.added) { log(`Лучшие посты: в подборку — ${result.added}.`); loadCurrentItem(); }
    } catch (error) { status.textContent = error.message; }
    finally { updateBestPostsButton(); }
}

function initializeBestPosts() {
    $('best-posts-form').addEventListener('submit', searchBestPosts);
    $('best-posts-sort').addEventListener('change', () => { if ($('best-posts-link').value.trim()) searchBestPosts(); });
    $('best-posts-select-all').addEventListener('click', selectAllBestPosts);
    $('best-posts-take').addEventListener('click', takeBestPosts);
}

// ---- Свой контент: папка и Яндекс Диск ----

const OWN_FOLDER_LIMIT = 1000;            // файлов за один выбор папки (и в сутки на сервере)
const OWN_FILE_MAX = 2 * 1024 * 1024;     // upload_max_filesize на сервере
const OWN_IMAGE_EXT = /\.(jpe?g|png|gif|webp|bmp)$/i;
const OWN_VIDEO_EXT = /\.(mp4|mov|webm|avi|mkv|m4v)$/i;
let ownFolderBusy = false;
let ownFolderStop = false;

/** Файлы папки по одному (лимит загрузки сервера); подпись — из .txt с тем же именем. */
async function importOwnFolder(files) {
    if (ownFolderBusy) return;
    ownFolderBusy = true;
    const [status, errors, stop, button] = [$('own-content-status'), $('own-content-errors'), $('own-folder-stop'), $('own-folder-button')];
    const stem = file => file.webkitRelativePath.replace(/\.[^./]+$/, '');
    const media = files.filter(f => OWN_IMAGE_EXT.test(f.name) || OWN_VIDEO_EXT.test(f.name));
    const captions = new Map(files.filter(f => /\.txt$/i.test(f.name)).map(f => [stem(f), f]));
    const queue = media.slice(0, OWN_FOLDER_LIMIT);
    errors.replaceChildren();
    ownFolderStop = false;
    button.disabled = true;
    stop.hidden = false;
    let added = 0;
    try {
        for (let i = 0; i < queue.length && !ownFolderStop; i++) {
            const original = queue[i];
            const file = await prepareOwnImage(original);
            status.textContent = `Загружено ${i} из ${queue.length}` + (added ? `, в подборку: ${added}` : '');
            if (file.size > OWN_FILE_MAX) {
                errors.append(el('li', { textContent: `${original.name}: файл больше 2 МБ после подготовки.` }));
                continue;
            }
            const note = captions.has(stem(original)) ? (await captions.get(stem(original)).text()).trim() : $('own-text').value.trim();
            const form = new FormData();
            form.append('files[]', file);
            form.append('text', note.slice(0, 1000));
            form.append('album', '0');
            try {
                const result = await api('own_upload', { body: form });
                added += result.added;
                errors.append(...(result.errors || []).map(text => el('li', { textContent: text })));
            } catch (error) { errors.append(el('li', { textContent: `${file.name}: ${error.message}` })); }
        }
        const stopped = ownFolderStop ? ' Остановлено.' : '';
        const rest = media.length > OWN_FOLDER_LIMIT ? ` Загружены первые ${OWN_FOLDER_LIMIT} файлов.` : '';
        status.textContent = `Добавлено в подборку: ${added} из ${queue.length}.${stopped}${rest}`;
        if (added) { log(`Свой контент: из папки в подборку — ${added}.`); loadCurrentItem(); }
    } finally {
        ownFolderBusy = false;
        ownFolderStop = false;
        stop.hidden = true;
        button.disabled = false;
    }
}

/** Импорт картинок с Яндекс Диска: до 100 за нажатие; если остались — повторное нажатие продолжает. */
async function importYadisk() {
    const [input, button, status, errors] = [$('own-yadisk-url'), $('own-yadisk-button'), $('own-content-status'), $('own-content-errors')];
    const url = input.value.trim();
    if (!url) return;
    button.disabled = true;
    status.textContent = 'Импортирую с Яндекс Диска…';
    try {
        const result = await api('own_import_yadisk', { body: { url } });
        status.textContent = `Добавлено в подборку: ${result.added} (проверено ${result.processed} из ${result.total})` + (result.skipped ? `, видео пропущено: ${result.skipped}` : '')
            + (result.done ? '. Папка готова.' : '. Нажмите ещё раз — продолжу с того места.');
        errors.replaceChildren(...(result.errors || []).map(text => el('li', { textContent: text })));
        if (result.added) { log(`Свой контент: с Яндекс Диска в подборку — ${result.added}.`); loadCurrentItem(); }
    } catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; }
}

/** Твиты X по ссылкам (до 20 за нажатие, до 100 в сутки). */
async function importX() {
    const [input, button, status, errors] = [$('own-x-links'), $('own-x-button'), $('own-content-status'), $('own-content-errors')];
    const links = input.value.split('\n').map(s => s.trim()).filter(Boolean);
    if (!links.length) return;
    button.disabled = true;
    status.textContent = 'Загружаю твиты…';
    try {
        const result = await api('own_import_x', { body: { links } });
        status.textContent = `Добавлено в подборку: ${result.added}` + (result.skipped ? `, пропущено: ${result.skipped}` : '');
        errors.replaceChildren(...(result.errors || []).map(([link, text]) => el('li', { textContent: `${link}: ${text}` })));
        if (result.added) { log(`Свой контент: твиты X в подборку — ${result.added}.`); input.value = ''; loadCurrentItem(); }
    } catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; }
}

function initializeOwnFolder() {
    const folder = $('own-folder');
    $('own-folder-button').addEventListener('click', () => folder.click());
    $('own-folder-stop').addEventListener('click', () => { ownFolderStop = true; });
    folder.addEventListener('change', () => { const files = Array.from(folder.files); folder.value = ''; if (files.length) importOwnFolder(files); });
    $('own-yadisk-button').addEventListener('click', importYadisk);
    $('own-x-button').addEventListener('click', importX);
}

// ---- Зеркало источника: личный источник → подборка или очередь канала ----

const mirrorState = { channels: [] };

/** Правила из формы; замены — строки «что → на что», галочка «регулярное выражение» относится ко всем. */
function mirrorRulesFromForm() {
    const words = id => $(id).value.split(',').map(w => w.trim()).filter(Boolean);
    const regex = $('mirror-regex').checked;
    const replace = $('mirror-replace').value.split('\n').map(line => line.split(/\s*(?:→|->)\s*/))
        .filter(([from]) => from && from.trim()).map(([from, to = '']) => ({ from: from.trim(), to: to.trim(), regex }));
    return { include: words('mirror-include'), exclude: words('mirror-exclude'), replace,
        strip_links: $('mirror-strip-links').checked, strip_mentions: $('mirror-strip-mentions').checked, strip_hashtags: $('mirror-strip-hashtags').checked,
        append: $('mirror-append').value.trim() };
}

function mirrorRow(m) {
    const mode = m.mode === 'queue' ? `в очередь: ${m.channel_title || 'канал'}` : 'в подборку';
    const checked = m.last_checked_at ? `последняя проверка: ${new Date(m.last_checked_at * 1000).toLocaleString('ru-RU')}` : 'ещё не проверялось';
    return el('li', { className: 'mirror-row' },
        el('span', { className: 'mirror-name', textContent: m.name }),
        el('span', { className: 'best-post-meta', textContent: `${mode} · ${checked}` }),
        el('button', { type: 'button', className: 'mirror-delete', textContent: 'Удалить', onclick: () => deleteMirror(m.id) }));
}

async function loadMirrors() {
    try {
        const data = await api('mirror_list');
        $('mirror-list').replaceChildren(...data.mirrors.map(mirrorRow));
    } catch (error) { $('mirror-status').textContent = error.message; }
}

async function deleteMirror(id) {
    try {
        await api('mirror_delete', { body: { id } });
        await loadMirrors();
    } catch (error) { $('mirror-status').textContent = error.message; }
}

async function saveMirror(event) {
    event.preventDefault();
    const [button, status, mode] = [$('mirror-button'), $('mirror-status'), $('mirror-mode').value];
    button.disabled = true;
    status.textContent = 'Проверяю источник…';
    try {
        const result = await api('mirror_save', { body: { link: $('mirror-link').value.trim(), mode, channel: mode === 'queue' ? $('mirror-channel').value : '', rules: mirrorRulesFromForm() } });
        status.textContent = `Зеркало добавлено: ${result.mirror.name}`;
        $('mirror-link').value = '';
        await loadMirrors();
    } catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; }
}

/** Каналы для очереди — из очереди Telegram; без подключённого Telegram список пуст. */
async function loadMirrorChannels() {
    try {
        const data = await api('telegram_queue');
        mirrorState.channels = data.connected ? data.channels || [] : [];
    } catch { mirrorState.channels = []; }
    $('mirror-channel').replaceChildren(...mirrorState.channels.map(c => el('option', { value: c.id, textContent: c.chat_title || c.id })));
    updateMirrorMode();
}

function updateMirrorMode() {
    const queue = $('mirror-mode').value === 'queue';
    $('mirror-channel').hidden = !queue;
    if (queue && !mirrorState.channels.length) $('mirror-status').textContent = 'Очередь канала — после подключения Telegram-канала.';
}

function initializeMirror() {
    $('mirror-form').addEventListener('submit', saveMirror);
    $('mirror-mode').addEventListener('change', updateMirrorMode);
}

export function initializeSourceTab() {
    const toolsDialog = $('source-tools-dialog');
    const openTools = () => { if (!toolsDialog.open) toolsDialog.showModal(); };
    $('source-tools-open').addEventListener('click', openTools);
    $('source-tools-close').addEventListener('click', () => toolsDialog.close());
    toolsDialog.addEventListener('click', event => { if (event.target === toolsDialog) toolsDialog.close(); });
    $('source-posts-more').addEventListener('click', () => showFeed(feed.posts.length));   // «Ещё N постов» — все оставшиеся
    initializeOwnContent();
    initializeOwnFolder();
    initializeBestPosts();
    initializeMirror();
    $('source-add-form').addEventListener('submit', addSource);
    document.querySelectorAll('#source-tab [data-source-vote]').forEach(b => b.addEventListener('click', () => vote(b.dataset.sourceVote)));
    bindMenu($('source-complaint-button'), $('source-complaint-menu'), item => vote('reject', item.dataset.sourceReason));
    const retry = () => { state.skipped = []; loadNextSource(); };
    $('source-platform').addEventListener('change', () => { state.request++; state.loading = false; state.source = null; hide('source-loading'); hide('source-card-wrapper'); hide('source-empty'); loadPendingSources(); });
    $('source-sort').addEventListener('change', loadPendingSources);
    $('source-find-other').addEventListener('click', retry);
    $('pending-sources-refresh').addEventListener('click', loadPendingSources);
    $('pending-sources-more').addEventListener('click', () => appendPendingBatch(pendingSources.generation));
    let pendingScrollFrame = 0;
    window.addEventListener('scroll', () => {
        if (pendingScrollFrame || !isTabActive('source-tab')) return;
        pendingScrollFrame = requestAnimationFrame(() => {
            pendingScrollFrame = 0;
            const more = $('pending-sources-more');
            if (!more.hidden && window.scrollY > pendingSources.lastScroll + 40 && more.getBoundingClientRect().top < window.innerHeight + 300) appendPendingBatch(pendingSources.generation);
        });
    }, { passive: true });
    onTopicChange(() => { state.request++; state.loading = false; state.source = null; hide('source-loading'); hide('source-card-wrapper'); hide('source-empty'); if (isTabActive('source-tab')) { loadPendingSources(); loadDiscoveryStatus(); } });
    $('source-empty-retry').addEventListener('click', retry);
    $('source-empty-discover').addEventListener('click', startDiscovery);
    $('source-empty-vk').addEventListener('click', () => openAccountSection('social'));
    $('source-empty-add').addEventListener('click', () => { openTools(); $('source-add-link').focus(); });
    $('discovery-button').addEventListener('click', startDiscovery);
    $('autopilot-form').addEventListener('submit', saveAutopilot);
    window.setInterval(() => { if (isTabActive('source-tab')) loadAutopilot(); }, 30000);
    document.querySelectorAll('[data-topic-source-filter]').forEach(b => b.addEventListener('click', () => { topicSources.filter = b.dataset.topicSourceFilter; renderTopicSources(); }));
    $('topic-sources-refresh').addEventListener('click', loadTopicSources);
    initRegistry();
    $('topic-sources-section').addEventListener('toggle', () => { if ($('topic-sources-section').open) loadTopicSources(); });
    $('registry-section').addEventListener('toggle', () => { if ($('registry-section').open) loadRegistry(); });
    onTabOpen('source-tab', () => { loadDiscoveryStatus(); loadAutopilot(); loadTopicSources(); if ($('registry-section').open) loadRegistry(); loadMirrors(); loadMirrorChannels(); loadPendingSources(); });
    onParserStateChange(running => {   // парсер закончил: подбор мог найти источники, сбор — обновить аудиторию
        if (running) return;
        state.discovering = false;
        if (!isTabActive('source-tab')) return;
        loadDiscoveryStatus();
        loadTopicSources();
        loadPendingSources();
    });
}
