// Главная страница: контент, источники и состояние подбора.
// Данные — тот же API, что у приложения.
import { showPlan, showProfile } from './profile.js';
import { cleanCaption, log, onTabOpen, onTopicChange, switchTab } from './core.js';
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
    const videos = document.querySelectorAll('#grid video, #rejected-grid video');
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

function card(i, rejection = null) {
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
        try { await api('proposal_hide', { body: { image: i.item_key || i.image } }); await loadContent(); } catch (error) { remove.disabled = false; log(`Не удалось убрать пост: ${error.message}`); }
    } });
    return el('article', { className: 'tile', tabIndex: 0, role: 'button', ariaLabel: `Открыть пост: ${title}`, onclick: e => { if (!e.target.closest('a, img, button')) { e.stopPropagation(); openPreview(i); } }, onkeydown: e => { if ((e.key === 'Enter' || e.key === ' ') && e.target === e.currentTarget) { e.preventDefault(); openPreview(i); } } },
        el('div', { className: 'tile-media' }, media,
            hasVideo ? el('span', { className: 'tile-video-mark', ariaLabel: 'Есть видео', textContent: '▶' }) : null,
            files.length > 1 ? el('span', { className: 'tile-media-count', ariaLabel: `Медиафайлов: ${files.length}`, textContent: `▣ ${files.length}` }) : null),
        el('div', { className: 'tile-body' },
            el('b', { textContent: title }),
            el('small', { textContent: [platformOf(i), ago].filter(Boolean).join(' · ') }),
            rejection ? el('p', { className: 'rejected-reason', textContent: `Причина: ${rejection.reason || (rejection.stage === 'prescreen' ? 'Вероятный мусор' : 'Не прошёл оценку')}` }) : null,
            rejection ? el('small', { className: 'rejected-stage', textContent: [({ filter: 'Фильтры', prescreen: 'Быстрая оценка' })[rejection.stage] || 'Глубокая оценка', rejection.score != null ? `${rejection.score}%` : '', rejection.at ? formatAgo(rejection.at) : ''].filter(Boolean).join(' · ') }) : null,
            i.source_url ? el('a', { href: i.source_url, target: '_blank', rel: 'noopener', textContent: '↗', title: 'Открыть у источника' }) : null,
            el('div', { className: 'home-card-actions' }, rocket, add, rejection ? null : remove)));
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

// История загружается при открытии страницы и смене темы.
let rejectedRequest = 0;
let rejectedSignature = null;
async function loadRejected({ silent = false } = {}) {
    const request = ++rejectedRequest;
    const grid = $('rejected-grid');
    if (!silent) {
        rejectedSignature = null;
        grid.replaceChildren(el('p', { className: 'muted', textContent: 'Загрузка…' }));
    }
    try {
        const data = await api('studio_rejected');
        if (request !== rejectedRequest) return;
        const records = data.items || [];
        const signature = JSON.stringify(records);
        if (signature === rejectedSignature) return;
        rejectedSignature = signature;
        grid.replaceChildren(...(records.length ? records.map(record => card(record.item || {}, record)) : [el('p', { className: 'muted', textContent: 'Пока нет сохранённых отклонений. Они появятся после следующих проходов.' })]));
        if (videoObserver) grid.querySelectorAll('video').forEach(video => videoObserver.observe(video));
    } catch (error) {
        if (request === rejectedRequest && !silent) grid.replaceChildren(el('p', { className: 'muted', textContent: `Не удалось загрузить: ${error.message}` }));
    }
}
$('show-rejected').addEventListener('click', () => switchTab('rejected-tab'));

// ---- конвейер: текущее действие и счётчики прохода ----
// rank — порядок шагов сборщика; ПК-фильтр идёт в одном шаге с «Фильтрами» (stage filter)
const RANK = { collect: 0, filter: 1, prescreen: 3, analysis: 4, pool: 5 };
const PIPE_STEPS = [
    { stage: 'collect', name: 'Сбор', rank: 0, num: f => f.collected },
    { stage: 'filter', name: 'Фильтры', rank: 1, num: f => f.candidates, note: f => `отсеяно ${f.filtered}` },
    { stage: 'prescreen', name: 'Быстрая оценка', rank: 3, num: f => f.prescreen_rejected, note: f => `отсеяно за проход${f.filtered_by?.rejected_before ? ` · ранее: ${f.filtered_by.rejected_before}` : ''}` },
    { stage: 'analysis', name: 'Глубокая оценка', rank: 4, num: f => f.analyzed },
    { stage: 'pool', name: 'Добавлено за проход', rank: 5, num: f => f.added },
];
const STAGE_NAME = { collect: 'Сбор постов', filter: 'Фильтры', prescreen: 'Быстрая оценка', analysis: 'Глубокая оценка', pool: 'В пул' };

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

// Поиск источников может работать независимо от сборщика постов, в том числе в фоне.
function renderSourcePipe(d = {}) {
    const sig = JSON.stringify(d);
    if (sig === renderSourcePipe.sig) return;
    renderSourcePipe.sig = sig;
    const running = Boolean(d.running), active = Boolean(d.active), hasBatch = Number(d.target) > 0;
    const stage = d.stage || '';
    $('sourcePipeNow').textContent = running ? (d.paused ? 'Останавливается…' : 'Идёт подбор') : d.paused ? 'Поиск остановлен' : !d.enabled ? 'Автоподбор выключен' : active ? 'Ожидает продолжения' : hasBatch ? 'Цель достигнута' : 'Ещё не запускался';
    $('sourcePipeDetail').textContent = running ? (d.detail || 'Готовим поиск') : d.paused ? 'Запустите подбор источников в админке, чтобы продолжить.' : active && d.enabled ? 'Следующий фоновый проход продолжит эту партию.' : !d.enabled && active ? 'Включите автоподбор, чтобы продолжить.' : '';
    const health = `Источников темы: ${Number(d.own_sources) || 0}. Порог релевантности: ${Number(d.accepted_posts) || 0} из ${Number(d.analyzed_posts) || 0} проверенных постов.`;
    $('sourcePipeInsight').textContent = [health, hasBatch ? d.insight : '', hasBatch ? d.last_search : '', d.retry_count ? `Ожидают повтора: ${d.retry_count}.` : ''].filter(Boolean).join(' ');
    const errors = Array.isArray(d.errors) ? d.errors : [];
    $('sourcePipeErrors').hidden = errors.length === 0;
    $('sourcePipeErrors').replaceChildren(...(errors.length ? [el('b', { textContent: 'Ошибки поиска' }), ...errors.map(message => el('div', { textContent: message }))] : []));
    $('sourcePipeProgress').hidden = !hasBatch;
    if (hasBatch) {
        const found = Number(d.found) || 0, target = Number(d.target) || 0;
        const percent = Math.min(100, Math.round(found * 100 / target));
        $('sourcePipeGoal').textContent = `${found} из ${target}`;
        $('sourcePipeBar').setAttribute('aria-valuenow', String(percent));
        $('sourcePipeBar').querySelector('i').style.width = `${percent}%`;
    }
    const steps = [
        { name: 'Поиск кандидатов', value: d.pending, state: running && ['prepare', 'search', 'queue'].includes(stage) ? 'run' : hasBatch ? 'ok' : 'wait', note: 'в очереди' },
        { name: 'Проверено', value: d.checked, state: running && stage === 'check' ? 'run' : hasBatch ? 'ok' : 'wait', note: d.failed ? `ошибок ${d.failed}` : '' },
        { name: 'Отклонено', value: d.rejected, state: hasBatch ? 'ok' : 'wait' },
        { name: 'На одобрение', value: d.suggested, state: hasBatch ? 'ok' : 'wait' },
        { name: 'Добавлено автоматически', value: d.added, state: hasBatch ? 'ok' : 'wait' },
    ];
    $('sourcePipe').replaceChildren(...steps.map(step => el('div', { className: `job ${step.state}` },
        el('span', { className: `dot ${step.state}`, textContent: step.state === 'ok' ? '✓' : '' }),
        el('span', {}, step.name, step.note ? el('span', { className: 't', textContent: step.note }) : null),
        el('span', { className: 'n', textContent: hasBatch ? (Number(step.value) || 0).toLocaleString('ru-RU') : '—' }))));
}

async function loadLive() {
    const gen = ++live.gen;   // один активный опрос: ответ старого запроса не планирует новый таймер
    clearTimeout(live.timer);
    let r;
    try { r = await apiOrLogin('studio_live'); } catch (error) {
        renderPipe.sig = null;
        $('pipeNow').textContent = error.status === 401 ? 'Войдите, чтобы увидеть состояние' : 'Не удалось загрузить состояние';
        $('sourcePipeNow').textContent = error.status === 401 ? 'Войдите, чтобы увидеть состояние' : 'Не удалось загрузить состояние';
        renderSourcePipe.sig = null;
        $('feed').replaceChildren(el('li', { className: 'muted', textContent: error.status === 401 ? 'Войдите, чтобы увидеть материалы.' : 'Не удалось загрузить материалы.' }));
        if (gen === live.gen && error.status !== 401) live.timer = setTimeout(loadLive, 15000);
        return;
    }
    if (gen !== live.gen) return;
    if ($('rejected-tab').classList.contains('active')) loadRejected({ silent: true });
    try { renderLive(r); }
    catch (e) { console.error('studio_live:', e); }   // ошибка отрисовки не должна останавливать опрос
    finally { if (gen === live.gen) live.timer = setTimeout(loadLive, r.running || r.source_discovery?.running ? 4000 : 15000); }
}

function renderLive(r) {
    const items = r.items || [];

    const poolCount = Number.isFinite(Number(r.pool)) ? Math.max(0, Math.trunc(Number(r.pool))) : 0;
    $('nav-pool').textContent = poolCount ? poolCount.toLocaleString('ru-RU') : '';
    $('home-pool-count').textContent = poolCount ? `· ${poolCount.toLocaleString('ru-RU')}` : '';

    live.last = r;
    renderPipe(r);
    renderSourcePipe(r.source_discovery);
    $('feed').replaceChildren(...(items.length ? items.slice(0, 5).map(feedItem)
        : [el('li', { className: 'muted', textContent: 'Пока ничего: подборка пуста.' })]));
}

function render() {
    const list = visible();
    $('grid').querySelectorAll('video').forEach(video => videoObserver?.unobserve(video));
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
    const panel = $('live-panel');
    const placePanel = tab => {
        const excluded = tab === 'tree-tab' || tab === 'calendar-tab';
        panel.hidden = excluded;
        $('shared-layout').classList.toggle('has-shared-panel', !excluded);
    };
    document.querySelectorAll('.tab-btn[data-tab]').forEach(button => onTabOpen(button.dataset.tab, () => placePanel(button.dataset.tab)));
    placePanel(document.querySelector('.view-panel.active')?.id || 'home-tab');
    onTabOpen('home-tab', () => loadContent().catch(fail));
    onTabOpen('rejected-tab', () => loadRejected());
    if ($('rejected-tab').classList.contains('active')) loadRejected();
    onTopicChange(() => { loadContent().catch(fail); loadLive(); if ($('rejected-tab').classList.contains('active')) loadRejected(); });
    document.addEventListener('posta:pool-updated', loadLive);
    loadSources().catch(() => {});
    renderPipe({});
    loadLive();
    window.addEventListener('parser-stopped', loadLive);
    api('billing_info').then(showPlan).catch(() => {});
    $('preview-backdrop').addEventListener('click', () => $('preview').close());
    document.addEventListener('keydown', e => { if (e.key === 'Escape' && $('preview').open && $('image-viewer').hidden) $('preview').close(); }, true);
}

init();
