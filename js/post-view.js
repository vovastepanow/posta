// post-view.js — единый просмотрщик постов: медиа-пост, текстовый пост и статья в одной карточке.
// Принимает нормализованный пост (post-adapters.js); кнопки действий даёт место вызова. Текст — только textContent.
import { $, COUNT, VIDEO_ICON, el, formatAgo } from './core.js';
import { openViewerGallery, videoPageFor } from './viewer.js';
import { loadImage, pageCheck, videoProgress, watchVideo } from './video-pending.js';
import { articleCover, articleMeta, siteBadge } from './card.js';
import { blockNode } from './article.js';
import { api } from './core.js';
import { analyzePost, needsFetch } from './post-adapters.js';

export const BIG_VIDEO_NOTE = 'Большое видео: Telegram не отдаёт его в веб-версии. Подключите личный аккаунт Telegram, и оно будет скачиваться само.';
const TEXT_BIG = 280;        // короткий текст (до 280 символов) — крупно
const TOKEN = /(https?:\/\/[^\s<>"']*[^\s<>"'.,;:!?)\]]|#[\p{L}\p{N}_]+|@[A-Za-z0-9_]{3,})/u;

const view = { posts: [], index: 0, actions: () => [], loadMore: null, onClose: null, opener: null, scrollY: 0, watches: [], scrollTimer: 0, zoomTile: null,
    loading: false, token: 0, root: null, refs: null };

/** Просмотрщик открыт. */
export const postViewOpen = () => Boolean(view.root && !view.root.hidden);

/** Открыть пост index из posts (нормализованные посты). actions(post) → [{label, icon, primary, run}]; loadMore() → ещё посты (массив) или пусто. */
export function openPostView(posts, index = 0, { actions = () => [], loadMore = null, onClose = null, title = '', translate = false } = {}) {
    if (!posts?.length) return;
    ensureRoot();
    view.posts = posts;
    view.actions = actions;
    view.loadMore = loadMore;
    view.onClose = onClose;
    view.translate = translate;
    view.refs.title.textContent = title;
    view.opener = document.activeElement;
    view.scrollY = window.scrollY;
    view.root.hidden = false;
    document.body.classList.add('pv-open');
    history.pushState({ postView: true }, '');
    showPost(Math.min(Math.max(0, index), posts.length - 1));
    view.refs.close.focus({ preventScroll: true });
}

/** Закрыть просмотрщик; фокус — на элемент, с которого открыли; fromHistory — закрытие кнопкой «назад». */
export function closePostView(fromHistory = false) {
    if (!postViewOpen()) return;
    stopWatches();
    view.root.hidden = true;
    document.body.classList.remove('pv-open');
    if (!fromHistory && history.state?.postView) history.back();
    view.onClose?.();
    view.opener?.focus?.({ preventScroll: true });
    window.scrollTo(0, view.scrollY);
}

/** Раскладка — по содержимому: есть медиа → карточка с мозаикой; статья → макет читалки; иначе — текст. */
/** Текст, который виден сейчас: перевод или оригинал. */
export const shownText = post => (post.translation && !post.showOriginal ? post.translation.text : post.text);

export const layoutOf = post => (post.media?.length ? 'media' : post.article ? 'article' : 'text');

/** Корень и шапка создаются один раз, в конце body. */
function ensureRoot() {
    if (view.root) return;
    const close = el('button', { type: 'button', className: 'pv-close', ariaLabel: 'Закрыть просмотр поста', title: 'Закрыть (Esc)', textContent: '✕', onclick: () => closePostView() });
    const refs = {
        counter: el('span', { className: 'pv-counter' }),
        title: el('span', { className: 'pv-title' }),
        prev: el('button', { type: 'button', className: 'pv-side-nav is-prev', ariaLabel: 'Предыдущий пост', title: 'Предыдущий пост (←)', textContent: '‹', onclick: () => stepPost(-1) }),
        next: el('button', { type: 'button', className: 'pv-side-nav is-next', ariaLabel: 'Следующий пост', title: 'Следующий пост (→)', textContent: '›', onclick: () => stepPost(1) }),
        close,
        body: el('div', { className: 'pv-body' }),
    };
    const head = el('header', { className: 'pv-head' }, refs.counter, refs.title, close);
    const sheet = el('div', { className: 'pv-sheet' }, head, refs.body);
    refs.frame = el('div', { className: 'pv-frame' }, refs.prev, sheet, refs.next);
    view.root = el('div', { className: 'pv', role: 'dialog', ariaModal: 'true', ariaLabel: 'Просмотр поста', hidden: true }, refs.frame);
    view.refs = refs;
    view.root.addEventListener('click', event => { if (event.target === view.root || event.target === refs.frame) closePostView(); });   // клик по фону
    refs.body.addEventListener('scroll', markScrolling, { passive: true });
    document.body.append(view.root);
    document.addEventListener('keydown', onKey, true);   // в фазе перехвата: раньше viewer.js, иначе один Esc закроет и зум, и пост
    window.addEventListener('popstate', () => { if (postViewOpen()) closePostView(true); });   // «назад» закрывает просмотрщик
    watchZoomClose();
    bindSwipe();
}

/** Клавиши: ← → — посты, Esc — закрыть. Пока открыт зум, клавиши достаются ему. */
function onKey(event) {
    if (!postViewOpen() || document.body.classList.contains('image-viewer-open')) return;
    if (event.key === 'Escape') return closePostView();
    if (event.key === 'ArrowLeft') stepPost(-1);
    else if (event.key === 'ArrowRight') stepPost(1);
    else if (event.key === 'Tab') trapFocus(event);
}

/** Tab не уходит за окно: фокус по кругу внутри просмотрщика. */
function trapFocus(event) {
    const items = [...view.root.querySelectorAll('button, a[href], video[controls]')].filter(item => !item.disabled && !item.hidden && item.offsetParent !== null);
    if (!items.length) return;
    const [first, last] = [items[0], items[items.length - 1]];
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
}

/** Прокрутка карточки: на телефоне боковые кнопки прячутся и возвращаются через 600 мс после остановки. */
function markScrolling() {
    view.refs.frame.classList.add('is-scrolling');
    clearTimeout(view.scrollTimer);
    view.scrollTimer = setTimeout(() => view.refs.frame.classList.remove('is-scrolling'), 600);
}

/** Свайп влево/вправо по карточке (телефон). По мозаике, кнопкам и ссылкам не листаем. */
function bindSwipe() {
    let start = null;
    view.root.addEventListener('touchstart', event => {
        start = event.target.closest('.pv-mosaic, video, button, a') ? null : event.touches[0].clientX;
    }, { passive: true });
    view.root.addEventListener('touchend', event => {
        if (start === null) return;
        const dx = event.changedTouches[0].clientX - start;
        start = null;
        if (Math.abs(dx) > 60) stepPost(dx < 0 ? 1 : -1);
    });
}

/** Закрытие зума плиткой не управляет: после него фокус возвращается на нажатую плитку, пост остаётся открыт. */
function watchZoomClose() {
    new MutationObserver(() => {
        if (document.body.classList.contains('image-viewer-open') || !view.zoomTile) return;
        view.zoomTile.focus({ preventScroll: true });
        view.zoomTile = null;
    }).observe(document.body, { attributes: true, attributeFilter: ['class'] });
}

/** Листание по кругу: после последнего — первый, перед первым — последний. */
function stepPost(delta) {
    const n = view.posts.length;
    if (n > 1) showPost((view.index + delta + n) % n);
}

/** Показать пост index (границы — без перехода). Соседние картинки — заранее; у последнего — подгрузка. */
function showPost(index) {
    if (index < 0 || index >= view.posts.length) return;
    stopWatches();
    view.index = index;
    renderPost(view.posts[index]);
    translatePost(view.posts[index]);
    view.refs.body.scrollTop = 0;
    if (!matchMedia('(prefers-reduced-motion: reduce)').matches) view.refs.body.animate([{ opacity: 0.4 }, { opacity: 1 }], { duration: 120 });
    preload(view.posts[index + 1]);
    preload(view.posts[index - 1]);
    refreshNav();
    if (index === view.posts.length - 1) requestMore();
}

function refreshNav() {
    const n = view.posts.length;
    view.refs.counter.textContent = `${view.index + 1} / ${n}`;
    view.refs.counter.title = `Пост ${view.index + 1} из ${n}${view.loadMore ? ' · дальше подгружаются новые' : ''}; листание по кругу`;
    view.refs.prev.hidden = view.refs.next.hidden = n < 2;
}

/** Подгрузка по последнему посту: новые посты дописываются в конец, без перехода. */
function requestMore() {
    if (!view.loadMore || view.loading) return;
    view.loading = true;
    Promise.resolve().then(() => view.loadMore()).then(more => {
        if (more?.length && postViewOpen()) view.posts.push(...more);
        refreshNav();
    }).catch(() => {}).finally(() => { view.loading = false; });
}

/** Автоперевод текста поста (источники): ответ приходит позже — текст в открытом листе заменяется на месте; настройка «Автоперевод» — на сервере. */
async function translatePost(post) {
    if (!view.translate || !post.text || post.translation !== undefined) return;
    post.translation = null;
    try {
        const data = await api('translate_text', { body: { text: post.text } });
        if (!data.text) return;
        post.translation = { text: data.text, note: data.note || 'Переведено' };
        if (postViewOpen() && view.posts[view.index] === post) {
            const top = view.refs.body.scrollTop;
            renderPost(post);
            view.refs.body.scrollTop = top;
        }
    } catch { post.translation = undefined; }   // сеть — попробуем при следующем показе
}

/** Картинка соседнего поста — заранее; видео — нет. */
function preload(post) {
    const first = post?.media?.find(m => m.kind === 'image' && m.src);
    if (first) Object.assign(new Image(), { decoding: 'async', src: first.src });
}

/** Тело просмотрщика под раскладку поста: одна колонка; статья — макет читалки. */
function renderPost(post) {
    const body = view.refs.body;
    const layout = layoutOf(post);
    body.setAttribute('data-layout', layout);
    const ctx = overlayContext(post);
    if (layout === 'article') body.replaceChildren(articleBlock(post, ctx));
    else body.replaceChildren(el('article', { className: 'pv-read' }, ...sideBlocks(post, { big: layout === 'text' && (post.text || '').length <= TEXT_BIG, ctx })));
}

/** Контекст оверлея: кнопки места вызова, ссылка и статистика, докачка видео здесь. */
function overlayContext(post) {
    return { actions: view.actions, zoom: null, watchPending: true, link: true, stats: true, sourceUrl: post.url || '' };
}

/** Встроенный просмотрщик (лист поста в чужой контейнер, например «Подбор»): без оверлея, ‹ ›, клавиш и ловушки фокуса.
 *  opts: actions(post) — кнопки; zoom: {onPost, postCheck} — зум картинок и видео с кнопкой редактора как в «Подборе»;
 *  watchPending — докачка видео ведётся здесь (иначе место вызова следит само); link, stats — ссылка и статистика, если их показывает место вызова. */
export function renderPostSheet(container, post, { actions = () => [], zoom = null, watchPending = true, link = true, stats = true, big = false } = {}) {
    stopWatches();
    releaseVideos(container);
    const ctx = { actions, zoom, watchPending, link, stats, sourceUrl: post.url || '' };
    container.classList.add('is-embedded');
    if (layoutOf(post) === 'article') container.replaceChildren(articleBlock(post, ctx));
    else container.replaceChildren(el('article', { className: 'pv-read pv-embedded' }, ...sideBlocks(post, { big, ctx })));
    return container;
}

/** Колонка поста: источник, мозаика, текст, опрос, статистика, действия и ссылка. */
function sideBlocks(post, { big = false, ctx = overlayContext(post) } = {}) {
    const blocks = [sourceRow(post), post.media?.length ? mosaicBlock(post.media, ctx) : null, textBlock(post, big)];
    if (post.poll) blocks.push(pollBlock(post.poll));
    if (ctx.stats) blocks.push(statsRow(post), insightBlock(post));
    blocks.push(actionsBar(post, ctx.actions), ctx.link ? linkRow(post) : null);
    return blocks.filter(Boolean);
}

/** Все файлы поста сеткой, как альбом в Telegram: раскладка — классом n-1…n-6 или n-many (см. CSS). */
function mosaicBlock(media, ctx) {
    const note = el('p', { className: 'pv-mosaic-note', hidden: true });
    const layout = media.length <= 6 ? `n-${media.length}` : 'n-many';
    return el('div', { className: 'pv-media' }, el('div', { className: `pv-mosaic ${layout}` }, ...media.map((m, i) => tileNode(m, i, media, ctx))), note);
}

/** Плитка одного файла. Картинка и видео с файлом — зум; докачка — видео-плитка по готовности; большое видео — пояснение под сеткой. */
function tileNode(m, i, media, ctx) {
    if (m.src) return openableTile(m, i, media, ctx);
    if (m.pending && m.page) return pendingTile(m, i, media, ctx);
    if (m.unavailable) return el('button', { type: 'button', className: 'pv-cell', ariaLabel: 'Большое видео', onclick: event => showNote(event.currentTarget) },
        posterNode(m.poster), el('span', { className: 'pv-cell-mark', textContent: 'большое видео' }), videoMark());
    if (needsFetch(m, ctx.watchPending)) return fetchTile(m, i, media, ctx);
    if (m.page) return el('a', { className: 'pv-cell pv-cell-link', href: m.page, target: '_blank', rel: 'noopener noreferrer', textContent: 'Смотреть на сайте ↗' });
    return plainTile(m.poster, 'Файл недоступен');
}

/** Плитка картинки — кнопка зума по всем файлам поста; плитка видео — см. videoTile (играет само или постер с «▶»). */
function openableTile(m, i, media, ctx) {
    if (m.kind === 'video') return videoTile(m, i, media, ctx, isFirstVideo(media, i) && !reducedMotion());
    const img = imageNode(m);
    const cell = el('button', { type: 'button', className: 'pv-cell', ariaLabel: `Картинка ${i + 1} из ${media.length}`, onclick: event => { event.stopPropagation(); openGallery(media, i, event.currentTarget, ctx); } }, img);
    loadImage(img, m.src, cell);   // дольше двух секунд — полоса загрузки поверх плитки
    return cell;
}

/** Значок «это видео» — тот же, что в предложке (.video-mark); других значков на видео нет. */
const videoMark = () => el('span', { className: 'video-mark', innerHTML: VIDEO_ICON });

/** Первое видео поста с файлом: оно играет само (без звука), остальные — постер с «▶». */
const isFirstVideo = (media, i) => media.findIndex(x => x.kind === 'video' && x.src) === i;
const reducedMotion = () => matchMedia('(prefers-reduced-motion: reduce)').matches;
const VIDEO_SELECTOR = 'video.pv-cell-media';
const viewObservers = new WeakMap();   // видео → наблюдатель видимости (снимается при смене листа)

/** Видео в плитке: сразу — играющая плитка (autoplay без звука); иначе — постер с «▶», клик запускает этот ролик со звуком. */
function videoTile(m, i, media, ctx, autoplay) {
    return autoplay ? liveTile(m, i, media, ctx) : posterTile(m, i, media, ctx);
}

/** Постер с значком видео и меткой длительности: клик — ролик в этой плитке, со звуком и панелью контролов. */
function posterTile(m, i, media, ctx) {
    const mark = m.duration ? el('span', { className: 'pv-cell-mark', textContent: duration(m.duration) }) : null;
    return el('button', { type: 'button', className: 'pv-cell pv-cell-poster', ariaLabel: `Видео ${i + 1} из ${media.length} — открыть со звуком`,
        onclick: event => { event.stopPropagation(); openGallery(media, i, event.currentTarget, ctx); } }, posterNode(m.poster), mark, videoMark());
}

/** Немое превью: кнопка с <video> внутри (без controls), играет по кругу без звука. Клик — медиапросмотрщик со звуком, с начала. */
function liveTile(m, i, media, ctx) {
    bindVisibility();
    const box = el('button', { type: 'button', className: 'pv-cell pv-cell-video', ariaLabel: `Видео ${i + 1} из ${media.length} — открыть со звуком` });
    const video = el('video', { className: 'pv-cell-media', loop: true, playsInline: true, preload: 'metadata', poster: m.poster || '', muted: true, autoplay: true, disablePictureInPicture: true });
    video.src = m.src;
    box._src = m.src;
    box._fallback = m.fallback || '';
    video.addEventListener('error', () => {
        if (m.fallback && video.src !== m.fallback) { video.src = m.fallback; return; }   // запасной адрес
        box.replaceWith(plainTile(m.poster, 'Видео недоступно'));
    });
    box.append(video, videoMark());
    box.addEventListener('click', event => {
        event.stopPropagation();   // картинки и постеры на странице — свой просмотрщик (viewer.js); здесь — зум этой плитки
        video.pause();
        openGallery(media, i, box, ctx);   // медиапросмотрщик — с начала ролика
    });
    watchVisibility(video, box);
    if (!reducedMotion()) video.play().catch(() => {});
    return box;
}

/** Видео вне экрана — пауза, вернулось в кадр — продолжает (порог 0,5). */
function watchVisibility(video, box) {
    const io = new IntersectionObserver(([entry]) => {
        if (entry.intersectionRatio < 0.5) { if (!video.paused) { video.pause(); video.dataset.viewPaused = '1'; } }
        else if (video.dataset.viewPaused) { delete video.dataset.viewPaused; video.play().catch(() => {}); }
    }, { threshold: [0, 0.5, 1] });
    io.observe(box);
    viewObservers.set(video, io);
}

let visibilityBound = false;
/** Вкладка браузера скрыта — пауза; вернулась — продолжает то, что играло. */
function bindVisibility() {
    if (visibilityBound) return;
    visibilityBound = true;
    document.addEventListener('visibilitychange', () => {
        for (const video of document.querySelectorAll(VIDEO_SELECTOR)) {
            if (document.hidden) { if (!video.paused) { video.pause(); video.dataset.tabPaused = '1'; } }
            else if (video.dataset.tabPaused) { delete video.dataset.tabPaused; video.play().catch(() => {}); }
        }
    });
}

/** Смена поста, закрытие или перерисовка листа: видео на паузе, адрес снят (не качается и не звучит в фоне). */
function releaseVideos(root) {
    for (const video of root?.querySelectorAll(VIDEO_SELECTOR) || []) {
        viewObservers.get(video)?.disconnect();
        video.pause();
        video.removeAttribute('src');
        video.load();
    }
}

/** Ролик качается: плитка с постером и плашкой; по готовности — обычная видео-плитка на её месте, сетка не перерисовывается. */
function pendingTile(m, i, media, ctx) {
    const box = plainTile(m.poster, 'Видео загружается…');
    box.classList.add('is-video-pending');   // место вызова, которое следит само, находит плитку и ставит на неё полосу
    if (!ctx.watchPending) return box;   // место вызова само следит за роликом и перерисует лист
    // Пост открыт — его ролик ждут: сервер поднимет его в начало очереди (уже стоящий в очереди не дублируется)
    if (videoPageFor(m.page)) api('video_fetch', { body: { url: m.page } }).catch(() => {});
    const bar = videoProgress(box);
    view.watches.push(watchVideo(pageCheck(videoPageFor(m.page) || m.page), {
        onProgress: p => bar.update(p),
        onReady: result => {
            bar.stop();
            if (!box.isConnected) return;
            Object.assign(m, { src: result.video, pending: false });
            box.replaceWith(tileNode(m, i, media, ctx));
        },
        onFail: message => { bar.stop(); if (box.isConnected) box.replaceWith(plainTile(m.poster, message)); },
    }));
    return box;
}

/**
 * Ролик с адресом, но без файла (Shorts, TikTok, Telegram): скачивание ставится, когда пост показан — дальше как докачка.
 * Не вышло — кнопка «▶ Загрузить видео»; нет личного аккаунта Telegram (409) — ссылка с пояснением.
 */
function fetchTile(m, i, media, ctx) {
    let node;
    const show = next => { if (node?.isConnected) node.replaceWith(next); node = next; };
    const ready = local => { Object.assign(m, { src: local, pending: false }); show(tileNode(m, i, media, ctx)); };
    const start = async () => {
        const box = plainTile(m.poster, 'Видео загружается…');
        show(box);
        const bar = videoProgress(box);   // через две секунды ожидания — полоса со стадией и процентом
        try {
            const info = await api('video_fetch', { body: { url: m.page } });
            if (info.status === 'done' && info.local) { bar.stop(); return ready(info.local); }
            if (info.status === 'failed') { bar.stop(); return show(retryTile(m, info.error || 'Видео не скачалось.', start)); }
            view.watches.push(watchVideo(pageCheck(videoPageFor(m.page)), {
                onProgress: p => bar.update(p),
                onReady: result => { bar.stop(); ready(result.video); },
                onFail: message => { bar.stop(); show(retryTile(m, message, start)); },
            }));
        } catch (error) {
            bar.stop();
            if (error.status === 409) return show(linkCell(m, error.message));
            show(retryTile(m, error.message, start));
        }
    };
    start();
    return node;
}

/** Не скачан: кнопка повторной загрузки, пояснение (причина) и ссылка на сайт. */
function retryTile(m, note, start) {
    return el('div', { className: 'pv-cell pv-cell-link pv-cell-retry' }, posterNode(m.poster),
        el('button', { type: 'button', className: 'pv-retry', textContent: '▶ Загрузить видео', onclick: event => { event.stopPropagation(); start(); } }),
        el('small', { textContent: note }), el('a', { href: m.page, target: '_blank', rel: 'noopener noreferrer', textContent: 'Смотреть на сайте ↗' }));
}

/** Ссылка на сайт с пояснением: ролик скачать нельзя, пока не подключён нужный аккаунт. */
function linkCell(m, note) {
    return el('div', { className: 'pv-cell pv-cell-link' }, el('a', { href: m.page, target: '_blank', rel: 'noopener noreferrer', textContent: 'Смотреть на сайте ↗' }),
        el('small', { textContent: note }));
}

/** Картинка плитки без адреса (его ставит loadImage); ошибка — запасной адрес, потом плашка «Файл недоступен». */
function imageNode(m) {
    const img = el('img', { alt: '', decoding: 'async', referrerPolicy: 'no-referrer' });
    img.addEventListener('error', () => {
        if (m.fallback && img.src !== m.fallback) img.src = m.fallback;   // запасной адрес картинки
        else img.replaceWith(el('span', { className: 'pv-cell-plate', textContent: 'Файл недоступен' }));
    });
    return img;
}

/** Постер видео или плитки; не загрузился — без картинки, плитка остаётся тёмной. */
function posterNode(src) {
    if (!src) return null;
    const img = el('img', { src, alt: '', loading: 'lazy', decoding: 'async', referrerPolicy: 'no-referrer' });
    img.addEventListener('error', () => img.remove());
    return img;
}

/** Плитка без файла: постер (если есть) и текст поверх. */
function plainTile(poster, text) {
    return el('div', { className: 'pv-cell' }, posterNode(poster), el('span', { className: 'pv-cell-plate', textContent: text }));
}

/** Большое видео: пояснение — под сеткой, текстом (виден на телефоне без наведения). */
function showNote(tile) {
    const note = tile.closest('.pv-media')?.querySelector('.pv-mosaic-note');
    if (!note) return;
    note.textContent = BIG_VIDEO_NOTE;
    note.hidden = false;
}

/** Зум по плитке: картинки и видео поста с файлом, открытие на нажатой. Фокус вернётся на плитку (watchZoomClose). */
function openGallery(media, i, tile, ctx) {
    const playable = media.filter(m => m.src && (m.kind === 'image' || m.kind === 'video'));
    const sourceUrl = ctx.sourceUrl;
    const items = playable.map(m => (m.kind === 'video'
        ? { kind: 'video', src: m.src, poster: m.poster || '', fallback: m.fallback || '', sourceUrl, alt: 'Видео поста' }
        : { kind: 'image', src: m.src, fallback: m.fallback || '', sourceUrl, alt: 'Картинка поста' }));
    const at = playable.indexOf(media[i]);
    view.zoomTile = tile;
    openViewerGallery(items, at, ctx.zoom?.onPost ?? null, ctx.zoom?.postCheck ?? null);
}

/** Зум закрыт: превью плитки, которую открывали, продолжает без звука. Если в зуме тот же ролик — с секунды, где остановился зум;
 *  другой ролик (долистали) или картинка — с места, где превью стояло. Пост сменился или лист перерисован — ничего. */
function onZoomVideoClosed({ detail }) {
    const box = view.zoomTile;
    const video = box?.isConnected ? box.querySelector(VIDEO_SELECTOR) : null;
    if (!video) return;   // постер или плитки уже нет: превью не было
    if (detail.src && (detail.src === box._src || detail.src === box._fallback)) video.currentTime = detail.time;
    video.play().catch(() => {});
}
window.addEventListener('viewer-video-closed', onZoomVideoClosed);

const duration = seconds => { const s = Math.max(0, Math.floor(seconds || 0)); return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`; };

/** Строка источника: значок, название, дата с полной датой в title, статус. */
function sourceRow(post) {
    const name = post.source?.name || '';
    const date = post.date ? new Date(post.date * 1000).toLocaleString('ru-RU') : '';
    return el('div', { className: 'pv-source' },
        siteBadge(name),
        el('div', { className: 'pv-source-text' },
            el('span', { className: 'pv-source-name', textContent: name || 'Источник' }),
            el('span', { className: 'pv-date', title: date, textContent: post.date ? formatAgo(post.date) : '' })),
        post.badge ? el('span', { className: `pv-badge tone-${post.badge.tone || 'muted'}`, textContent: post.badge.text }) : null);
}

/** Охват, комментарии, реакции — чипами, как в главной карточке. */
function statsRow(post) {
    const s = post.stats || {};
    const reactions = Array.isArray(s.reactions) ? s.reactions : null;
    const chips = [
        s.views != null && el('span', { className: 'eng-chip', textContent: `👁 ${COUNT.format(s.views)}` }),
        reactions ? reactions.map(([emoji, n]) => el('span', { className: 'eng-chip eng-reaction', title: `Реакций ${emoji}: ${n}` }, el('span', { className: 'eng-emoji', textContent: emoji }), el('b', { textContent: COUNT.format(n) }))) : null,
        typeof s.reactions === 'number' && s.reactions != null && el('span', { className: 'eng-chip', textContent: `❤ ${COUNT.format(s.reactions)}` }),
        s.comments != null && el('span', { className: 'eng-chip', textContent: `💬 ${COUNT.format(s.comments)}` }),
    ].flat().filter(Boolean);
    return chips.length ? el('div', { className: 'pv-stats' }, ...chips) : null;
}

/** Оценка поста для того, кто его заберёт: вердикт по охвату, доли аудитории, состав поста и что поправить перед публикацией. */
function insightBlock(post) {
    const { verdict, metrics, facts, warnings } = analyzePost(post);
    const copy = (label, value, done) => el('button', { type: 'button', className: 'pv-mini', textContent: label, onclick: async event => {
        const button = event.currentTarget;
        try { await navigator.clipboard.writeText(value); button.textContent = done; } catch { button.textContent = 'Не вышло'; }
        setTimeout(() => { if (button.isConnected) button.textContent = label; }, 1500);
    } });
    const tools = [post.text ? copy('Копировать текст', post.text, 'Скопировано ✓') : null, post.url ? copy('Копировать ссылку', post.url, 'Скопировано ✓') : null].filter(Boolean);
    if (!verdict && !metrics.length && !facts.length && !warnings.length && !tools.length) return null;
    return el('section', { className: 'pv-insight', ariaLabel: 'Оценка поста' },
        verdict ? el('p', { className: `pv-verdict tone-${verdict.tone}`, title: verdict.hint, textContent: verdict.text }) : null,
        metrics.length ? el('dl', { className: 'pv-metrics' }, ...metrics.flatMap(m => [el('dt', { textContent: m.label }), el('dd', { title: m.hint, textContent: m.value })])) : null,
        facts.length ? el('p', { className: 'pv-facts', textContent: facts.join(' · ') }) : null,
        ...warnings.map(text => el('p', { className: 'pv-warn', textContent: `⚠ ${text}` })),
        tools.length ? el('div', { className: 'pv-tools-row' }, ...tools) : null);
}

/** Текст поста: переносы сохраняются, ссылки, #теги и @имена выделены, **жирный**. Без текста секции нет вовсе;
 *  тихая строка — только у поста, в котором нет ни медиа, ни опроса, ни статьи (иначе лист был бы пустым). */
function textBlock(post, big) {
    if (!post.text) return post.media?.length || post.poll || post.article ? null : el('p', { className: 'pv-quiet', textContent: 'Пост без текста' });
    const tr = post.translation;
    const shown = tr && !post.showOriginal ? tr.text : post.text;
    const text = el('div', { className: big ? 'pv-text-big' : 'pv-text' }, ...richText(shown));
    if (!tr) return text;
    const wrap = el('div', { className: 'pv-translated' });
    const toggle = el('button', { type: 'button', className: 'pv-mini', textContent: post.showOriginal ? 'Показать перевод' : 'Показать оригинал', onclick: () => {
        post.showOriginal = !post.showOriginal;
        wrap.replaceWith(textBlock(post, big));
    } });
    wrap.append(text, el('p', { className: 'pv-facts', style: 'margin:6px 0 0' }, `🌐 ${tr.note} · `, toggle));
    return wrap;
}

/** Опрос из очереди: вопрос и варианты строками, без голосования. */
function pollBlock(poll) {
    return el('div', { className: 'pv-poll' },
        el('p', { className: 'pv-poll-q', textContent: poll.question || 'Опрос' }),
        el('ol', { className: 'pv-poll-list' }, ...(poll.options || []).map(text => el('li', { className: 'pv-option', textContent: text }))));
}

/** Текст → узлы: **жирный**, ссылки http(s) — <a>, #теги и @имена — выделены. Только текстовые узлы и ссылки. */
export function richText(text) {
    const nodes = [];
    for (const part of String(text).split(/(\*\*[^*\n]+\*\*)/u)) {
        if (!part) continue;
        const bold = part.length > 4 && part.startsWith('**') && part.endsWith('**');
        const children = (bold ? part.slice(2, -2) : part).split(TOKEN).filter(Boolean).map(tokenNode);
        nodes.push(...(bold ? [el('strong', {}, ...children)] : children));
    }
    return nodes;
}

function tokenNode(piece) {
    if (/^https?:\/\//.test(piece) && isHttpUrl(piece)) return el('a', { href: piece, target: '_blank', rel: 'noopener noreferrer', textContent: piece });
    if (/^[#@]/.test(piece) && TOKEN.test(piece)) return el('span', { className: 'pv-tag', textContent: piece });
    return document.createTextNode(piece);
}

function isHttpUrl(value) {
    try { return ['http:', 'https:'].includes(new URL(value).protocol); } catch { return false; }
}

/** Действия места вызова: кнопка занята, пока run() не завершился. */
function actionsBar(post, actions) {
    const list = actions(post) || [];
    if (!list.length) return null;
    return el('div', { className: 'pv-actions' }, ...list.map(action => {
        const button = el('button', { type: 'button', className: `pv-action${action.primary ? ' is-primary' : ''}`, textContent: `${action.icon ? `${action.icon} ` : ''}${action.label}` });
        button.addEventListener('click', async () => {
            button.disabled = true;
            button.setAttribute('aria-busy', 'true');
            try { await action.run(post); }
            finally { if (button.isConnected) { button.disabled = false; button.removeAttribute('aria-busy'); } }
        });
        return button;
    }));
}

function linkRow(post) {
    return post.url ? el('a', { className: 'pv-link', href: post.url, target: '_blank', rel: 'noopener noreferrer', textContent: 'Открыть в источнике ↗' }) : null;
}

/** Статья: обложка 16:9, мета, заголовок, анонс, блоки (article_read) со скелетом; без блоков — анонс, текст и ссылка на сайт. */
function articleBlock(post, ctx) {
    const a = post.article;
    const item = { url: a.readUrl || post.url, article: { title: a.title, lead: a.lead, site: a.site, rubric: a.rubric, author: a.author, minutes: a.minutes, cover: { src: a.cover } } };
    const blocks = el('div', { className: 'pv-article-blocks' }, el('div', { className: 'ar-skeleton', ariaHidden: 'true' }));
    const token = ++view.token;
    if (a.readUrl) loadBlocks(a.readUrl, blocks, token, item, post);
    else blocks.replaceChildren(articleFallback(post));
    return el('article', { className: `pv-read pv-article${ctx.link ? '' : ' pv-embedded'}` },
        articleCover(item),
        articleMeta(item),
        el('h2', { className: 'pv-article-title', textContent: a.title || post.text.split('\n')[0] || '' }),
        a.lead ? el('p', { className: 'pv-article-lead', textContent: a.lead }) : null,
        blocks,
        actionsBar(post, ctx.actions),
        ctx.link ? linkRow(post) : null);
}

/** Блоки статьи с сервера; ошибка или чужая запись — запасной вид. */
async function loadBlocks(url, box, token, item, post) {
    try {
        const data = await api('article_read', { params: { url } });
        if (token !== view.token || !box.isConnected) return;
        const images = (data.article?.blocks || []).filter(b => b.type === 'image');
        const nodes = (data.article?.blocks || []).map(b => blockNode(b, images));
        box.replaceChildren(...(nodes.length ? nodes : [el('p', { className: 'ar-note', textContent: 'На странице не нашлось текста статьи. Откройте её на сайте.' })]));
    } catch {
        if (token === view.token && box.isConnected) box.replaceChildren(articleFallback(post));
    }
}

/** Запасной вид статьи: текст поста и кнопка «Читать на сайте». */
function articleFallback(post) {
    return el('div', { className: 'pv-article-fallback' },
        post.text ? el('div', { className: 'pv-text' }, ...richText(post.text)) : null,
        post.url ? el('a', { className: 'pv-link', href: post.url, target: '_blank', rel: 'noopener noreferrer', textContent: 'Читать на сайте ↗' }) : null);
}

/** Остановить докачку и видео (при листании и закрытии). */
function stopWatches() {
    for (const watch of view.watches) watch.stop();
    view.watches = [];
    releaseVideos(view.refs?.body);
}
