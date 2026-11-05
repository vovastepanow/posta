// Главная страница: контент, источники и состояние подбора.
// Данные — тот же API, что у приложения.
import { showPlan, showProfile } from './profile.js';
import { cleanCaption, log, onTabOpen, onTopicChange } from './core.js';
import { bindFutureDraftButton, applyRocketCheck, postItemToTelegram } from './content.js';

const $ = id => document.getElementById(id);

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
    catch (e) { throw e; }
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

const firstLine = i => cleanCaption(i.text).split('\n')[0].replace(/#\S+/g, '').trim().slice(0, 90);

const FILTERS = [
    ['all', 'Всё', () => true],
    ['images', 'Картинки', i => !!i.display_image && !i.display_video && !i.video],
    ['videos', 'Видео', i => postMedia(i).some(file => file.video) || !!(i.video_via || i.video_pending)],
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

function postMedia(item) {
    if (item.gallery?.length > 1) return item.gallery.map(file => ({ image: file.display_image || file.image || '', video: file.display_video || file.video || '' }));
    if (item.images?.length > 1) return item.images.map((src, index) => ({ image: index === 0 ? item.display_image || src : src, video: index === 0 ? item.display_video || item.video || '' : '' }));
    const image = item.display_image || item.image || '';
    const video = item.display_video || item.video || '';
    return image || video ? [{ image, video }] : [];
}

const videoObserver = 'IntersectionObserver' in window ? new IntersectionObserver(entries => {
    for (const entry of entries) {
        const video = entry.target;
        if (entry.isIntersecting && !document.hidden) {
            if (!video.getAttribute('src')) video.src = video.dataset.src;
            video.play().catch(() => {});
        } else video.pause();
    }
}, { rootMargin: '150px' }) : null;
document.addEventListener('visibilitychange', () => {
    if (!videoObserver) return;
    const videos = document.querySelectorAll('#grid video');
    if (document.hidden) videos.forEach(video => video.pause());
    else videos.forEach(video => { videoObserver.unobserve(video); videoObserver.observe(video); });
});

function makeVideo(src, poster, preview = false) {
    const video = el('video', { poster: poster || '', muted: !preview, autoplay: preview, loop: !preview, playsInline: true, preload: preview ? 'auto' : 'none', controls: preview });
    video.setAttribute('playsinline', '');
    if (preview || !videoObserver) { video.src = src; video.autoplay = true; }
    else video.dataset.src = src;
    return video;
}

function card(i) {
    const files = postMedia(i), first = files[0];
    const media = first?.video ? makeVideo(first.video, first.image) : first?.image ? el('img', { src: first.image, alt: '', loading: 'lazy' }) : el('div', { className: 'text-only', textContent: (i.text || '').slice(0, 220) });
    const hasVideo = files.some(file => file.video) || Boolean(i.video_via || i.video_pending);
    const ago = i.timestamp ? formatAgo(i.timestamp) : '';
    const title = firstLine(i);
    const rocket = el('button', { type: 'button', className: 'home-card-action', textContent: '🚀', ariaLabel: 'Подготовить пост', onclick: async e => {
        e.stopPropagation();
        try { await postItemToTelegram(i, { onDone: () => loadContent().catch(() => {}) }); } catch (error) { log(`Не удалось открыть редактор: ${error.message}`); }
    } });
    applyRocketCheck(rocket, i.post_check, 'Подготовить пост');
    const add = bindFutureDraftButton(el('button', { type: 'button', className: 'home-card-action', textContent: '+🚀', ariaLabel: 'Добавить в будущий пост' }), () => i);
    const remove = el('button', { type: 'button', className: 'home-card-action', textContent: '✕', ariaLabel: 'Убрать из отобранных', title: 'Убрать из отобранных (оценка останется в истории)', onclick: async e => {
        e.stopPropagation();
        remove.disabled = true;
        try { await api('proposal_hide', { body: { image: i.image } }); await loadContent(); } catch (error) { remove.disabled = false; log(`Не удалось убрать пост: ${error.message}`); }
    } });
    return el('article', { className: 'tile', tabIndex: 0, role: 'button', ariaLabel: `Открыть пост: ${title}`, onclick: e => { if (!e.target.closest('a, img, button')) { e.stopPropagation(); openPreview(i); } }, onkeydown: e => { if ((e.key === 'Enter' || e.key === ' ') && e.target === e.currentTarget) { e.preventDefault(); openPreview(i); } } },
        el('div', { className: 'tile-media' }, media,
            hasVideo ? el('span', { className: 'tile-video-mark', ariaLabel: 'Есть видео', textContent: '▶' }) : null,
            files.length > 1 ? el('span', { className: 'tile-media-count', ariaLabel: `Медиафайлов: ${files.length}`, textContent: `▣ ${files.length}` }) : null),
        el('div', { className: 'tile-body' },
            el('b', { textContent: title }),
            el('small', { textContent: [platformOf(i), ago].filter(Boolean).join(' · ') }),
            i.source_url ? el('a', { href: i.source_url, target: '_blank', rel: 'noopener', textContent: '↗', title: 'Открыть у источника' }) : null,
            el('div', { className: 'home-card-actions' }, rocket, add, remove)));
}

// ---- просмотр поста ----
function openPreview(i) {
    const dlg = $('preview');
    const backdrop = $('preview-backdrop');
    const showPreview = () => { backdrop.hidden = false; dlg.show(); };
    const files = postMedia(i);
    let currentMedia = 0;
    const mediaHost = el('div', { className: 'preview-media' });
    const showMedia = () => {
        const file = files[currentMedia];
        mediaHost.querySelector('video')?.pause();
        if (!file) return;
        const media = file.video ? makeVideo(file.video, file.image, true) : el('img', { src: file.image, alt: `Медиа ${currentMedia + 1} из ${files.length}` });
        mediaHost.replaceChildren(media);
        if (files.length > 1) {
            const previous = el('button', { type: 'button', className: 'preview-media-prev', ariaLabel: 'Предыдущий файл', textContent: '‹', onclick: () => { currentMedia = (currentMedia + files.length - 1) % files.length; showMedia(); } });
            const next = el('button', { type: 'button', className: 'preview-media-next', ariaLabel: 'Следующий файл', textContent: '›', onclick: () => { currentMedia = (currentMedia + 1) % files.length; showMedia(); } });
            mediaHost.append(previous, next, el('span', { className: 'preview-media-counter', textContent: `${currentMedia + 1} / ${files.length}` }));
        }
    };
    showMedia();
    const p = platformOf(i);
    const openEditor = async scheduled => {
        try {
            let scheduledTs = 0;
            if (scheduled) {
                dlg.close();
                const { pickDateTime } = await import('./editor.js');
                const date = await pickDateTime({});
                if (!date) { showPreview(); return; }
                scheduledTs = Math.floor(date.getTime() / 1000);
            }
            if (dlg.open) dlg.close();
            const { postItemToTelegram } = await import('./content.js');
            await postItemToTelegram(i, { scheduledTs, onDone: () => loadContent().catch(() => {}) });
        } catch (error) {
            $('preview-error').textContent = `Не удалось открыть редактор: ${error.message}`;
            if (!dlg.open) showPreview();
        }
    };
    dlg.replaceChildren(
        el('h2', { className: 'preview-title', textContent: 'Просмотр поста' }),
        el('button', { type: 'button', className: 'preview-close', textContent: '×', ariaLabel: 'Закрыть', onclick: () => dlg.close() }),
        files.length ? mediaHost : null,
        el('div', { className: 'preview-body' },
            el('p', { className: 'preview-meta' }, el('span', { className: 'badge-inline', style: `--c:${colorOf(p)}`, textContent: `${PLATFORMS[p] || '◌'} ${p}` }),
                el('span', { textContent: String(i.source || '').split(' | ').pop() }),
                i.relevance ? el('span', { textContent: `★ ${i.relevance}` }) : null,
                i.timestamp ? el('span', { className: 'muted', textContent: formatAgo(i.timestamp) }) : null),
            el('p', { className: 'preview-text', textContent: cleanCaption(i.text) }),
            i.why_relevant ? el('p', { className: 'preview-why' }, el('b', { textContent: 'Почему отобран: ' }), i.why_relevant) : null,
            el('p', { id: 'preview-error', className: 'preview-error', role: 'alert' }),
            el('div', { className: 'preview-actions' },
                el('button', { type: 'button', className: 'cta', textContent: '🚀', ariaLabel: 'Подготовить пост', title: 'Подготовить пост', onclick: () => openEditor(false) }),
                bindFutureDraftButton(el('button', { type: 'button', className: 'ghost', textContent: '+🚀', ariaLabel: 'Добавить в будущий пост' }), () => i),
                el('button', { type: 'button', className: 'ghost', textContent: '🕒', ariaLabel: 'Запланировать', title: 'Запланировать', onclick: () => openEditor(true) })),
            i.source_url ? el('a', { className: 'preview-source', href: i.source_url, target: '_blank', rel: 'noopener noreferrer', textContent: 'Открыть оригинал ↗' }) : null));
    dlg.onclose = () => { backdrop.hidden = true; dlg.querySelector('video')?.pause(); };
    showPreview();
}

// ---- смотровое окно ----
const live = { timer: 0, last: null, gen: 0 };

// строка «Только что отобрано» в правой панели: миниатюра, первая строка текста, источник · балл
function feedItem(i) {
    const open = e => { if (e.type === 'click' && e.target.closest('img')) return; e.stopPropagation(); openPreview(i); };
    return el('li', { tabIndex: 0, title: firstLine(i), onclick: open, onkeydown: e => { if (e.key === 'Enter') open(); } },
        el('span', { className: 'th' }, i.display_image ? el('img', { src: i.display_image, alt: '', loading: 'lazy' }) : null),
        el('span', {}, el('span', { className: 'fline', textContent: firstLine(i) }),
            el('small', { textContent: `${String(i.source || '').split(' | ').pop()}${i.relevance ? ` · ★${i.relevance}` : ''}` })));
}

// ---- конвейер: текущее действие и счётчики прохода ----
// rank — порядок шагов сборщика; ПК-фильтр идёт в одном шаге с «Фильтрами» (stage filter)
const RANK = { collect: 0, filter: 1, prescreen: 3, analysis: 4, pool: 5 };
const PIPE_STEPS = [
    { stage: 'collect', name: 'Сбор', rank: 0, num: f => f.collected },
    { stage: 'filter', name: 'Фильтры', rank: 1, num: f => f.candidates, note: f => `отсеяно ${f.filtered}` },
    { stage: 'prescreen', name: 'Быстрая оценка', rank: 3, num: f => f.prescreen_rejected, note: () => 'отсеяно' },
    { stage: 'analysis', name: 'Глубокая оценка', rank: 4, num: f => f.analyzed },
    { stage: 'pool', name: 'Добавлено за проход', rank: 5, num: f => f.added },
];
const STAGE_NAME = { collect: 'Сбор постов', filter: 'Фильтры', prescreen: 'Быстрая оценка', analysis: 'Глубокая оценка', pool: 'В пул', discovery: 'Автоподбор источников' };

function renderPipe(r) {
    const running = !!r.running;
    const last = r.last_pass;
    const funnel = running ? r.progress?.funnel : last?.funnel;
    const added = running && funnel ? Number(funnel.added) || 0 : null;
    if (added !== null && renderPipe.previousAdded !== null && renderPipe.previousAdded !== undefined && added > renderPipe.previousAdded) {
        renderPipe.pulseUntil = Date.now() + 1200;
    }
    renderPipe.previousAdded = added;
    // перерисовываем только при изменении: опрос раз в 4 с не должен сбивать фокус и клики
    const sig = JSON.stringify([running, r.stopping, r.paused, r.stage, r.progress?.share, funnel, last?.at]);
    if (sig === renderPipe.sig) return;
    renderPipe.sig = sig;
    const cur = RANK[r.stage] ?? -1;
    $('pipeTitle').textContent = running || !last ? 'Конвейер' : `Последний проход · ${new Date(last.at * 1000).toTimeString().slice(0, 5)}`;
    $('pipeNow').textContent = r.stopping ? 'Останавливается…' : r.paused ? 'Сбор остановлен' : running ? (STAGE_NAME[r.stage] || 'Подбор') : (last ? 'Сборщик отдыхает' : 'Ещё не было проходов');
    $('pipe').replaceChildren(...PIPE_STEPS.map(step => {
        const repeating = step.stage === 'prescreen' || step.stage === 'analysis';
        const st = running ? (step.rank === cur ? 'run' : repeating && cur >= RANK.prescreen ? 'active' : step.rank < cur ? 'ok' : 'wait') : funnel && !repeating ? 'ok' : 'wait';
        const num = funnel && (st !== 'wait' || step.stage === 'pool' || !running) ? (Number(step.num(funnel)) || 0).toLocaleString('ru-RU') : '—';
        const note = [st === 'run' ? 'идёт…' : '', funnel && step.note && (st !== 'wait' || !running) ? step.note(funnel) : ''].filter(Boolean).join(' · ');
        const btn = el('div', { className: `job ${st}` },
            step.stage === 'pool'
                ? el('span', { className: `pool-led${Date.now() < (renderPipe.pulseUntil || 0) ? ' is-lit' : ''}`, ariaHidden: 'true' })
                : el('span', { className: `dot ${st}`, textContent: st === 'ok' ? '✓' : '' }),
            el('span', {}, step.name, el('span', { className: 't', textContent: note })),
            el('span', { className: 'n', textContent: num }));
        return btn;
    }));
    clearTimeout(renderPipe.pulseTimer);
    if (Date.now() < (renderPipe.pulseUntil || 0)) {
        renderPipe.pulseTimer = setTimeout(() => $('pipe').querySelector('.pool-led')?.classList.remove('is-lit'), renderPipe.pulseUntil - Date.now());
    }
}

async function loadLive() {
    const gen = ++live.gen;   // один активный опрос: ответ старого запроса не планирует новый таймер
    clearTimeout(live.timer);
    let r;
    try { r = await apiOrLogin('studio_live'); } catch (error) {
        renderPipe.sig = null;
        $('pipeNow').textContent = error.status === 401 ? 'Войдите, чтобы увидеть состояние' : 'Не удалось загрузить состояние';
        $('feed').replaceChildren(el('li', { className: 'muted', textContent: error.status === 401 ? 'Войдите, чтобы увидеть материалы.' : 'Не удалось загрузить материалы.' }));
        if (gen === live.gen && error.status !== 401) live.timer = setTimeout(loadLive, 15000);
        return;
    }
    if (gen !== live.gen) return;
    try { renderLive(r); }
    catch (e) { console.error('studio_live:', e); }   // ошибка отрисовки не должна останавливать опрос
    finally { if (gen === live.gen) live.timer = setTimeout(loadLive, r.running ? 4000 : 15000); }
}

function renderLive(r) {
    const items = r.items || [];

    $('nav-pool').textContent = r.pool || '';
    $('home-pool-count').textContent = r.pool ? `· ${r.pool}` : '';

    live.last = r;
    renderPipe(r);
    $('feed').replaceChildren(...(items.length ? items.slice(0, 5).map(feedItem)
        : [el('li', { className: 'muted', textContent: 'Пока ничего: подборка пуста.' })]));
}

function render() {
    const list = visible();
    videoObserver?.disconnect();
    $('grid').replaceChildren(...(list.length ? list.map(card) : [el('p', { className: 'muted', textContent: 'Здесь пока пусто: лайкните посты в «Предложке» — они появятся здесь.' })]));
    if (videoObserver) $('grid').querySelectorAll('video').forEach(video => videoObserver.observe(video));
    $('chips').replaceChildren(...FILTERS.map(([id, label, f]) => el('button', {
        type: 'button', className: state.filter === id ? 'is-active' : '', onclick: () => { state.filter = id; render(); },
    }, label, el('em', { textContent: String(state.items.filter(f).length) }))));
    $('side-filters').replaceChildren(...FILTERS.slice(0, 4).map(([id, label, f]) => el('li', { className: state.filter === id ? 'is-active' : '', onclick: () => { state.filter = id; render(); } },
        el('span', { textContent: label }), el('em', { textContent: String(state.items.filter(f).length) }))));
}

async function loadContent() {
    const [proposals, info] = await Promise.all([apiOrLogin('proposals'), api('account_info')]);
    state.items = proposals.items || [];
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

function init() {
    $('sort').onchange = e => { state.sort = e.target.value; render(); };
    $('search').oninput = e => { state.q = e.target.value; render(); };
    document.addEventListener('keydown', e => { if (e.key === '/' && document.activeElement !== $('search')) { e.preventDefault(); $('search').focus(); } });
    const fail = e => { $('grid').replaceChildren(el('p', { className: 'muted', textContent: e.status === 401 ? 'Войдите в приложение, чтобы увидеть материалы.' : `Ошибка: ${e.message}` })); };
    loadContent().catch(fail);
    onTabOpen('home-tab', () => loadContent().catch(fail));
    onTopicChange(() => { loadContent().catch(fail); loadLive(); });
    loadSources().catch(() => {});
    renderPipe({});
    loadLive();
    window.addEventListener('parser-stopped', loadLive);
    api('billing_info').then(showPlan).catch(() => {});
    $('preview-backdrop').addEventListener('click', () => $('preview').close());
    document.addEventListener('keydown', e => { if (e.key === 'Escape' && $('preview').open && $('image-viewer').hidden) $('preview').close(); }, true);
}

init();
