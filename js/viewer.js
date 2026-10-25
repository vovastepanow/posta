// viewer.js — просмотрщик картинок и видео: окно облегает медиа, двигается целиком, колесо масштабирует вокруг курсора,
// 0 — исходный размер. В галерее клик по внешней половине тёмной области слева от окна — предыдущий файл, справа — следующий; закрытие — Esc, клик по картинке или 🚀
// (открывает редактор поста). У видео свои кнопки: пробел/K — пауза, ←/→ — 5 с, M — звук, F — полный экран.
import { $, api, videoPageFor } from './core.js';
import { openAccountSection } from './account.js';
import { pageCheck, videoProgress, watchVideo } from './video-pending.js';

const ZOOM_LIMITS = { image: [0.15, 8], video: [0.3, 4] };
const ZOOM_STEP = 1.15;
const EDGE = 60;     // столько окна всегда остаётся на экране
const MARGIN = 14;
const state = { kind: 'image', zoom: 1, x: 0, y: 0, base: { width: 480, height: 270 }, drag: null, sourceUrl: '', justDragged: false, gallery: null, tgu: null, bar: null };
const providers = {};
let mediaPoster = null;

/** Источник галереи по data-gallery у картинки: provider(img) → {items, index, onPost?, postCheck?}. */
export const registerViewerGallery = (name, provider) => { providers[name] = provider; };
/** Публикация открытого файла, если у галереи нет своего onPost. */
export const registerViewerPoster = poster => { mediaPoster = poster; };

const frame = () => $('image-viewer-window');
const media = () => $(state.kind === 'video' ? 'image-viewer-video' : 'image-viewer-image');

export function mediaFileName(url, kind = 'image') {
    const fallback = kind === 'video' ? 'Видео' : 'Изображение';
    if (!url || /^(data|blob):/.test(url)) return fallback;
    try { const u = new URL(url, location.href); return decodeURIComponent(u.pathname.split('/').filter(Boolean).pop() || '') || u.hostname || fallback; } catch { return fallback; }
}

function setHeader(kind, primary, original) {
    const url = original && !original.startsWith('data:') ? original : primary;   // у копии из кэша имя — хэш, поэтому исходная ссылка
    const link = $('image-viewer-file');
    link.textContent = mediaFileName(url, kind);
    if (url && !url.startsWith('data:')) link.href = url; else link.removeAttribute('href');
    const many = (state.gallery?.items.length || 0) > 1;
    $('image-viewer-position').hidden = !many;
    if (many) $('image-viewer-position').textContent = `${state.gallery.index + 1} / ${state.gallery.items.length}`;
    $('image-viewer-post').hidden = !state.sourceUrl;
    if (state.sourceUrl) $('image-viewer-post').href = state.sourceUrl;
}

function layout() {
    const [w, h] = [Math.round(state.base.width * state.zoom), Math.round(state.base.height * state.zoom)];
    Object.assign(media().style, { width: `${w}px`, height: `${h}px` });
    frame().style.width = `${Math.max(90, w + 12)}px`;
    frame().style.transform = `translate(${Math.round(state.x)}px, ${Math.round(state.y)}px)`;
}
function clamp() {
    state.x = Math.max(EDGE - frame().offsetWidth, Math.min(innerWidth - EDGE, state.x));
    state.y = Math.max(EDGE - frame().offsetHeight, Math.min(innerHeight - EDGE, state.y));
}
function center() { layout(); state.x = (innerWidth - frame().offsetWidth) / 2; state.y = (innerHeight - frame().offsetHeight) / 2; layout(); }

/** Размер медиа известен: вписать в экран (маленькое — увеличить до размера окна) и поставить по центру. */
function setMediaSize(width, height) {
    if (!width || !height) return;
    const header = $('image-viewer-header').offsetHeight || 40;
    const fit = Math.min(Math.max(120, innerWidth - MARGIN * 2 - 12) / width, Math.max(120, innerHeight - MARGIN * 2 - header - 12) / height);
    state.base = { width: Math.round(width * fit), height: Math.round(height * fit) };
    state.zoom = 1;
    center();
}

/** Масштаб вокруг курсора: точка под указателем остаётся на месте. */
function zoomAt(factor, cx, cy) {
    const [min, max] = ZOOM_LIMITS[state.kind];
    let zoom = Math.max(min, Math.min(max, state.zoom * factor));
    if (Math.abs(zoom - 1) < 0.04) zoom = 1;
    if (zoom === state.zoom) return;
    const rect = media().getBoundingClientRect();
    const ratio = zoom / state.zoom;
    state.x += cx - (cx - rect.left) * ratio - rect.left;
    state.y += cy - (cy - rect.top) * ratio - rect.top;
    state.zoom = zoom;
    layout(); clamp(); layout();
}

function stopVideo() {
    const v = $('image-viewer-video');
    v.onerror = v.onloadedmetadata = v.onloadeddata = null;
    v.pause(); v.removeAttribute('src'); v.load();
}

function status(text, withLink = false) {
    $('image-viewer-status-text').textContent = text;
    $('image-viewer-source').hidden = !(withLink && state.sourceUrl);
    if (state.sourceUrl) $('image-viewer-source').href = state.sourceUrl;
    $('image-viewer-status').hidden = !text;
    if (!text) $('image-viewer-refetch').hidden = true;
}

export { videoPageFor } from './core.js';

/** Поля элемента галереи для ролика записи: адрес (уже недоступное видео — пост Telegram, ролик TikTok/Shorts ещё не скачан — адрес ролика) и ожидание. */
export function videoFields(item, sourceUrl = '') {
    if (item?.video_unavailable) return { videoPage: videoPageFor(sourceUrl), videoPending: false };
    const pending = Boolean(item?.video_via) && !item.video && !item.video_unavailable;
    return { videoPage: pending ? videoPageFor(item.video_page) : '', videoPending: pending };
}

/** Кнопка загрузки ролика: для Telegram — через личный аккаунт; ролик ещё качается (videoPending) — сразу ждём его. */
function offerVideo(item, gallery) {
    const button = $('image-viewer-tgu');
    const telegram = /^https:\/\/t\.me\//.test(item.videoPage || '');
    button.hidden = !item.videoPage;
    button.disabled = false;
    button.textContent = telegram ? 'Скачать через мой Telegram' : 'Загрузить видео';
    button.onclick = () => fetchVideo(item, gallery, state.tgu = Symbol('tgu'));
    $('image-viewer-account').hidden = true;
    if (item.videoPage && item.videoPending) fetchVideo(item, gallery, state.tgu = Symbol('tgu'));
}

/** Ставит ролик в очередь и ждёт его (video-pending.js); устаревший ожидатель (окно сменили) молча останавливается. */
async function fetchVideo(item, gallery, token) {
    $('image-viewer-tgu').disabled = true;
    $('image-viewer-account').hidden = true;
    status('Ставлю видео в очередь…');
    try {
        const info = await api('video_fetch', { body: { url: item.videoPage } });
        if (state.tgu !== token) return;
        if (info.status === 'done' && info.local) return swapToVideo(item, gallery, info.local);
        if (info.status === 'failed') throw new Error(info.error || 'Видео не скачалось.');
        status('Скачиваю видео: в очереди…');
        state.waitFor?.stop();
        state.waitFor = watchVideo(pageCheck(item.videoPage), {
            onProgress: p => {
                if (state.tgu !== token || !p) return;
                state.bar ||= videoProgress($('image-viewer-stage'));
                state.bar.update(p);
                status(`${p.label}${typeof p.percent === 'number' ? `: ${Math.round(p.percent)}%` : '…'}`);
            },
            onReady: result => { if (state.tgu === token) { state.bar?.stop(); state.bar = null; swapToVideo(item, gallery, result.video); } },
            onFail: message => { if (state.tgu === token) { state.bar?.stop(); state.bar = null; $('image-viewer-tgu').disabled = false; status(`Не удалось скачать: ${message}`, true); } }
        });
    } catch (error) {
        if (state.tgu !== token) return;
        $('image-viewer-tgu').disabled = false;
        if (error.status === 409) {   // аккаунт не подключён: пояснение и переход в «Личный кабинет → Соцсети»
            status(error.message);
            const account = $('image-viewer-account');
            account.hidden = false;
            account.onclick = () => { close(); openAccountSection('social'); };
            return;
        }
        status(`Не удалось скачать: ${error.message}`, true);
    }
}

/** Файл готов: окно подменяет текущий элемент галереи на видео из кэша (тот же индекс). */
function swapToVideo(item, gallery, local) {
    // Плитки источников по адресу ролика обновятся сами (sources.js слушает video-ready)
    if (item.videoPage) window.dispatchEvent(new CustomEvent('video-ready', { detail: { page: item.videoPage, local } }));
    const video = { kind: 'video', src: local, poster: item.poster || item.src, fallback: '', sourceUrl: item.sourceUrl, alt: item.alt };
    const items = gallery?.items ? gallery.items.map((x, i) => (i === gallery.index ? video : x)) : [video];
    openViewerGallery(items, gallery?.items ? gallery.index : 0, gallery?.onPost ?? null, gallery?.postCheck ?? null);
}

/** Кнопка «Загрузить заново» в окне ошибки: item.refetch() качает файл из исходного поста, при успехе окно закрывается. */
function offerRefetch(item) {
    const button = $('image-viewer-refetch');
    button.hidden = typeof item.refetch !== 'function';
    button.disabled = false;
    button.onclick = async () => {
        button.disabled = true;
        status('Загружаю файл из исходного поста…');
        try { await item.refetch(); close(); }
        catch (error) { status(`Не удалось загрузить: ${error.message}`, true); offerRefetch(item); }
    };
}

function syncRocket() {
    const check = state.gallery?.postCheck;
    const problems = [...(check?.problems || []), ...(check?.warnings || [])];
    const button = $('image-viewer-rocket');
    button.classList.toggle('rocket-blocked', check?.status === 'blocked');
    button.classList.toggle('rocket-warn', check?.status !== 'blocked' && problems.length > 0);
    button.title = problems.length ? `${check.status === 'blocked' ? 'Запостить нельзя' : 'Есть замечания'}: ${problems.join(' · ')}` : 'Запостить в Telegram (закрыть окно — Esc, клик по картинке или по тёмной области возле неё)';
}

/** Открывает файл: {kind, src, fallback (исходная ссылка, если копия не загрузится), poster, sourceUrl, alt}. */
function open(item, gallery = null) {
    stopVideo();
    Object.assign(state, { kind: item.kind, zoom: 1, base: { width: 480, height: 270 }, drag: null, justDragged: false, gallery, sourceUrl: item.sourceUrl || '', tgu: null });
    state.waitFor?.stop();
    // Стрелки перелистывания — только если медиа несколько
    $('image-viewer-prev').hidden = $('image-viewer-next').hidden = !(gallery && gallery.items.length > 1);
    for (const m of [$('image-viewer-image'), $('image-viewer-video')]) m.style.width = m.style.height = '';
    syncRocket();
    setHeader(item.kind, item.src, item.fallback);
    status('');
    offerVideo(item, gallery);
    const [image, video, stage] = [$('image-viewer-image'), $('image-viewer-video'), $('image-viewer-stage')];
    const target = item.kind === 'video' ? video : image;
    image.hidden = item.kind === 'video';
    video.hidden = item.kind !== 'video';
    stage.classList.add('is-loading');
    // Ролик качается в очереди — пояснение о «слишком большом» не показываем, ждём: «Скачиваю видео…»
    // Дольше двух секунд — полоса поверх просмотра; ролик, который ещё качается, двигает её процентом скачивания (fetchVideo)
    state.bar?.stop();
    state.bar = videoProgress(stage, { title: item.kind === 'video' ? 'Видео загружается' : 'Картинка загружается' });
    const loaded = () => { if (!(item.videoPage && item.videoPending)) { state.bar?.stop(); state.bar = null; } stage.classList.remove('is-loading'); const waiting = item.videoPage && item.videoPending; status(waiting ? 'Скачиваю видео…' : item.note || (item.videoPage ? 'Ролик ещё не загружен.' : ''), !waiting && !!item.note); };
    target.onerror = () => {
        if (item.fallback && target.getAttribute('src') !== item.fallback) {
            if (item.kind === 'video') status('Сохранённая копия не открылась, пробую исходную ссылку…');
            target.src = item.fallback;
            if (item.kind === 'video') video.play().catch(() => {});
            return;
        }
        target.onerror = null;
        state.bar?.stop();
        state.bar = null;
        stage.classList.remove('is-loading');
        status(item.refetch ? 'Файл пропал: сохранённой копии нет, а исходная ссылка устарела.'
            : item.kind !== 'video' ? 'Картинка недоступна: источник удалил файл или ссылка устарела.'
            : video.error?.code === MediaError.MEDIA_ERR_SRC_NOT_SUPPORTED ? 'Видео не воспроизводится: файл недоступен или браузер не поддерживает его формат.'
            : 'Видео не загрузилось: источник удалил файл или ссылка устарела.', true);
        offerRefetch(item);
    };
    if (item.kind === 'video') {
        status('Загрузка видео…');
        video.poster = item.poster || '';
        // start — секунда превью из плитки поста: одноразовая, при листании и возврате ролик идёт с нуля
        const start = item.start > 0 ? item.start : 0;
        item.start = 0;
        video.onloadedmetadata = () => {
            setMediaSize(video.videoWidth, video.videoHeight);
            if (start) video.currentTime = Math.min(start, video.duration - 0.1);
        };
        video.onloadeddata = loaded;
        Object.assign(video, { src: item.src, muted: false, volume: 0.33 });
    } else {
        image.alt = item.alt || 'Просмотр изображения';
        image.onload = () => { loaded(); setMediaSize(image.naturalWidth, image.naturalHeight); };
    }
    $('image-viewer').classList.toggle('is-video', item.kind === 'video');
    $('image-viewer').hidden = false;
    document.body.classList.add('image-viewer-open');
    center();
    if (item.kind === 'video') video.play().catch(() => {});
    else { image.src = item.src; if (image.complete && image.naturalWidth) setMediaSize(image.naturalWidth, image.naturalHeight); }
}

/** Галерея: items [{kind, src, fallback, poster, sourceUrl, alt}], с файла index. */
export function openViewerGallery(items, index = 0, onPost = null, postCheck = null) {
    if (!items?.length) return;
    index = Math.min(Math.max(0, index), items.length - 1);
    open(items[index], { items, index, onPost, postCheck });
}

function postAction() {
    const g = state.gallery;
    if (typeof g?.onPost === 'function') return g.onPost;
    const item = g?.items?.[g.index] || { kind: state.kind, src: media().getAttribute('src') || '', sourceUrl: state.sourceUrl };
    return mediaPoster && item.src ? () => mediaPoster({ kind: item.kind, src: item.src, poster: item.poster || '', sourceUrl: item.sourceUrl || '' }) : null;
}

function close() {
    state.bar?.stop();
    state.bar = null;
    // Зум закрыт: плитка поста (post-view.js) продолжит превью. Событие — при любом закрытии, даже если в конце долистали до картинки:
    // src — адрес ролика, который сейчас показан (у картинки пустой), время — его секунда
    if (!$('image-viewer').hidden) {
        const shown = state.gallery?.items?.[state.gallery.index];
        const video = $('image-viewer-video');
        const isVideo = state.kind === 'video';
        window.dispatchEvent(new CustomEvent('viewer-video-closed', { detail: { src: isVideo ? (shown?.src || video.getAttribute('src') || '') : '', time: isVideo ? video.currentTime : 0 } }));
    }
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    $('image-viewer').hidden = true;
    $('image-viewer').classList.remove('is-dragging', 'is-video');
    $('image-viewer-stage').classList.remove('is-loading');
    $('image-viewer-image').removeAttribute('src');
    stopVideo();
    state.gallery = null;
    state.tgu = null;
    state.waitFor?.stop();
    syncRocket();
    document.body.classList.remove('image-viewer-open');
}

// ---- Кнопки видео ----

const formatTime = s => {
    if (!Number.isFinite(s) || s < 0) return '0:00';
    const [h, m, sec] = [Math.floor(s / 3600), Math.floor(s / 60) % 60, String(Math.floor(s) % 60).padStart(2, '0')];
    return h ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`;
};

/** Ползунок из div: значение 0..1 по клику, перетаскиванию и стрелкам. */
function bindSlider(slider, onInput, keyStep) {
    const ratio = e => { const r = slider.getBoundingClientRect(); return Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)); };
    slider.addEventListener('pointerdown', e => { if (e.button) return; slider.setPointerCapture(e.pointerId); slider.classList.add('is-dragging'); onInput(ratio(e)); });
    slider.addEventListener('pointermove', e => { if (slider.hasPointerCapture(e.pointerId)) onInput(ratio(e)); });
    for (const type of ['pointerup', 'pointercancel']) slider.addEventListener(type, () => slider.classList.remove('is-dragging'));
    slider.addEventListener('keydown', e => {
        if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
        e.preventDefault(); e.stopPropagation();
        keyStep(e.key === 'ArrowRight' ? 1 : -1);
    });
}

/** Заливка во всю дорожку, обрезается справа: радуга не растягивается вместе с заливкой. */
function setSlider(fill, slider, r) {
    const p = Math.min(1, Math.max(0, r)) * 100;
    $(fill).style.clipPath = `inset(0 ${(100 - p).toFixed(2)}% 0 0 round 999px)`;
    $(slider).setAttribute('aria-valuenow', String(Math.round(p)));
}

function initializeVideoControls() {
    const [video, controls, stage] = [$('image-viewer-video'), $('video-controls'), $('image-viewer-stage')];
    const renderPlay = () => { controls.classList.toggle('is-paused', video.paused); $('vc-play').setAttribute('aria-label', video.paused ? 'Смотреть' : 'Пауза'); };
    const renderTime = () => {
        setSlider('vc-progress-fill', 'vc-progress', video.duration > 0 ? video.currentTime / video.duration : 0);
        $('vc-time').textContent = `${formatTime(video.currentTime)} / ${formatTime(video.duration)}`;
        const end = video.buffered.length ? video.buffered.end(video.buffered.length - 1) : 0;
        $('vc-buffered').style.width = video.duration > 0 ? `${Math.min(100, end / video.duration * 100)}%` : '0%';
    };
    const renderVolume = () => {
        const level = video.muted ? 0 : video.volume;
        setSlider('vc-volume-fill', 'vc-volume', level);
        controls.classList.toggle('is-muted', level === 0);
        $('vc-mute').setAttribute('aria-label', level === 0 ? 'Включить звук' : 'Выключить звук');
    };
    const toggle = () => (video.paused ? video.play().catch(() => {}) : video.pause());
    const seek = s => { if (Number.isFinite(video.duration)) video.currentTime = Math.min(video.duration, Math.max(0, video.currentTime + s)); };
    const setVolume = level => { video.volume = Math.min(1, Math.max(0, level)); video.muted = video.volume === 0; };
    const toggleMute = () => { if (video.muted || !video.volume) { video.muted = false; video.volume ||= 0.33; } else video.muted = true; };
    const fullscreen = () => (document.fullscreenElement ? document.exitFullscreen() : stage.requestFullscreen?.())?.catch?.(() => {});   // во весь экран — сцена с кнопками
    $('vc-play').addEventListener('click', toggle);
    $('vc-mute').addEventListener('click', toggleMute);
    $('vc-fullscreen').addEventListener('click', fullscreen);
    bindSlider($('vc-progress'), r => { if (Number.isFinite(video.duration)) video.currentTime = r * video.duration; }, d => seek(d * 5));
    bindSlider($('vc-volume'), setVolume, d => setVolume((video.muted ? 0 : video.volume) + d * 0.1));
    for (const type of ['play', 'pause', 'emptied']) video.addEventListener(type, renderPlay);
    for (const type of ['timeupdate', 'durationchange', 'loadedmetadata', 'progress', 'emptied']) video.addEventListener(type, renderTime);
    video.addEventListener('volumechange', renderVolume);
    document.addEventListener('fullscreenchange', () => stage.classList.toggle('is-fullscreen', document.fullscreenElement === stage));
    document.addEventListener('keydown', e => {
        if ($('image-viewer').hidden || state.kind !== 'video' || e.target.closest?.('input, textarea, select')) return;
        const key = e.key.toLowerCase();
        if (key === ' ' && e.target.closest?.('button')) return;
        const action = { ' ': toggle, k: toggle, arrowleft: () => seek(-5), arrowright: () => seek(5), m: toggleMute, 'ь': toggleMute, f: fullscreen, 'а': fullscreen }[key];
        if (action) { action(); e.preventDefault(); }
    });
    renderPlay(); renderTime(); renderVolume();
}

/** Перелистывание галереи стрелками; у одного медиа стрелок нет. */
function step(delta) {
    const g = state.gallery;
    if (!g || g.items.length < 2) return;
    openViewerGallery(g.items, (g.index + delta + g.items.length) % g.items.length, g.onPost, g.postCheck);
}

export function initializeImageViewer() {
    const [overlay, box] = [$('image-viewer'), frame()];
    initializeVideoControls();
    $('image-viewer-prev').addEventListener('click', () => step(-1));
    $('image-viewer-next').addEventListener('click', () => step(1));
    // Клик по любой картинке страницы: галерея (data-gallery), видео (data-video), внешняя ссылка (data-external) или картинка
    document.addEventListener('click', e => {
        const img = e.target.closest('img');
        if (!img || img.closest('#image-viewer') || !(img.currentSrc || img.src)) return;
        e.preventDefault();
        const d = img.dataset;
        if (d.external) return void open_(d.external);
        const g = d.gallery ? providers[d.gallery]?.(img) : null;
        if (g?.items?.length) return openViewerGallery(g.items, g.index, g.onPost, g.postCheck);
        open({ kind: d.video ? 'video' : 'image', src: d.video || img.currentSrc || img.src, poster: img.currentSrc || img.src, fallback: d.video ? d.videoFallback : d.fallback, sourceUrl: d.sourceUrl, alt: img.alt });
    });
    const open_ = url => window.open(url, '_blank', 'noopener,noreferrer');
    document.addEventListener('keydown', e => {
        if (overlay.hidden) return;
        if (e.key === 'Escape') close();
        else if ((e.key === 'ArrowLeft' || e.key === 'ArrowRight') && !e.target.closest('.vc-slider')) step(e.key === 'ArrowLeft' ? -1 : 1);
        else if ('+='.includes(e.key) && !e.target.closest('video')) zoomAt(ZOOM_STEP, innerWidth / 2, innerHeight / 2);
        else if (e.key === '-' && !e.target.closest('video')) zoomAt(1 / ZOOM_STEP, innerWidth / 2, innerHeight / 2);
        else if (e.key === '0') { state.zoom = 1; center(); }
    });
    addEventListener('resize', () => { if (!overlay.hidden) { clamp(); layout(); } });
    overlay.addEventListener('wheel', e => { e.preventDefault(); zoomAt(e.deltaY < 0 ? ZOOM_STEP : 1 / ZOOM_STEP, e.clientX, e.clientY); }, { passive: false });
    // Окно тянется за шапку, картинку и рамку; захват указателя — только при настоящем перетаскивании (иначе страдают клики по ссылкам)
    box.addEventListener('pointerdown', e => {
        if (!e.button && !e.target.closest('button, video, .image-viewer-status, .video-controls')) state.drag = { id: e.pointerId, sx: e.clientX, sy: e.clientY, ox: state.x, oy: state.y, moved: false };
    });
    box.addEventListener('pointermove', e => {
        const d = state.drag;
        if (!d || d.id !== e.pointerId || (!d.moved && Math.hypot(e.clientX - d.sx, e.clientY - d.sy) < 4)) return;
        d.moved = true;
        box.setPointerCapture(e.pointerId);
        overlay.classList.add('is-dragging');
        [state.x, state.y] = [d.ox + e.clientX - d.sx, d.oy + e.clientY - d.sy];
        clamp(); layout();
    });
    const endDrag = e => { if (state.drag?.id !== e.pointerId) return; state.justDragged = state.drag.moved; state.drag = null; overlay.classList.remove('is-dragging'); };
    box.addEventListener('pointerup', endDrag);
    box.addEventListener('pointercancel', endDrag);
    box.addEventListener('dragstart', e => e.preventDefault());
    // ЛКМ: картинка закрывается, видео ставится на паузу (и снимается с паузы). Перелистывания кликами нет.
    box.addEventListener('click', e => {
        const dragged = state.justDragged;
        state.justDragged = false;
        if (e.target.closest('a')) { if (dragged) e.preventDefault(); return; }
        if (!dragged && e.target.closest('#image-viewer-rocket')) { const post = postAction(); close(); post?.(); return; }
        if (dragged || e.target.closest('.video-controls, button, .image-viewer-status')) return;
        if (state.kind === 'video') {
            const video = $('image-viewer-video');
            video.paused ? video.play().catch(() => {}) : video.pause();
            return;
        }
        close();
    });
    // Тёмная область: если медиа несколько, то внешняя половина слева от окна (у края экрана) — «назад», справа — «вперёд»;
    // ближняя к окну половина и случай одного медиа — клик закрывает
    const multiple = () => (state.gallery?.items.length || 0) > 1;
    const side = e => {
        if (!multiple()) return 0;
        const r = frame().getBoundingClientRect();
        return e.clientX < r.left / 2 ? -1 : e.clientX > (r.right + innerWidth) / 2 ? 1 : 0;
    };
    overlay.addEventListener('click', e => { if (e.target !== overlay) return; const dir = side(e); dir ? step(dir) : close(); });
    overlay.addEventListener('pointermove', e => {
        const dir = e.target === overlay && !state.drag ? side(e) : 0;
        overlay.classList.toggle('hover-prev', dir < 0);
        overlay.classList.toggle('hover-next', dir > 0);
        overlay.classList.toggle('is-flip', dir !== 0);
    });
    overlay.addEventListener('pointerleave', () => overlay.classList.remove('hover-prev', 'hover-next', 'is-flip'));
}
