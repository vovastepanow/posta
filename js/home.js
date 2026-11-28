// Главная страница: контент, источники и состояние подбора.
// Данные — тот же API, что у приложения.
import { showPlan, showProfile } from './profile.js';
import { $, api, cleanCaption, el, formatAgo, log, onTabOpen, onTopicChange, safeSourcePostUrl, switchTab } from './core.js';
import { bindFutureDraftButton, applyRocketCheck, composePostFromItem, toggleFutureDraft } from './content.js';
import { fromRecord } from './post-adapters.js';
import { PLATFORMS, buildHomeFeed, describeFeedItem, reuseFeedItems } from './home-feed-model.js';
import { closePostView, openPostView } from './post-view.js';
import { pickDateTime } from './date-picker.js';

const state = { items: [], index: buildHomeFeed([]), filter: 'all', sort: 'new', q: '' };
const homeCards = new Map();

const firstLine = i => cleanCaption(i.text).split('\n')[0].replace(/#\S+/g, '').trim().slice(0, 90);

const videoObserver = 'IntersectionObserver' in window ? new IntersectionObserver(entries => {
    for (const entry of entries) {
        const video = entry.target;
        if (video.isConnected && entry.isIntersecting && !document.hidden) {
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

// Unobserve and release decoders only when a card actually leaves a grid.
function releaseCardMedia(node) {
    for (const video of node.querySelectorAll('video')) {
        videoObserver?.unobserve(video);
        video.pause();
        video.removeAttribute('src');
        video.load();
    }
}

function observeCardMedia(node) {
    if (videoObserver) node.querySelectorAll('video').forEach(video => videoObserver.observe(video));
}

function replaceGrid(grid, ...children) {
    releaseCardMedia(grid);
    grid.replaceChildren(...children);
    observeCardMedia(grid);
}

function reconcileFeed(rows) {
    const grid = $('grid');
    const keep = new Set(rows.map(row => row.item));
    for (const [item, node] of homeCards) {
        if (keep.has(item) && node.parentNode === grid) continue;
        releaseCardMedia(node);
        node.remove();
        homeCards.delete(item);
    }
    const nodes = rows.map(row => {
        let node = homeCards.get(row.item);
        if (!node) {
            node = card(row.item, null, row);
            homeCards.set(row.item, node);
        }
        return node;
    });
    if (!nodes.length) {
        grid.replaceChildren(el('p', { className: 'muted', textContent: 'Здесь пока пусто: лайкните посты в «Предложке» — они появятся здесь.' }));
        return;
    }
    const expected = new Set(nodes);
    for (const node of [...grid.children]) if (!expected.has(node)) node.remove();
    let next = grid.firstChild;
    for (const node of nodes) {
        if (node === next) next = next.nextSibling;
        else {
            const fresh = node.parentNode !== grid;
            grid.insertBefore(node, next);
            if (fresh) observeCardMedia(node);
        }
    }
}

function makeVideo(src, poster) {
    const video = el('video', { poster: poster || '', muted: true, loop: true, playsInline: true, preload: 'none' });
    if (!videoObserver) { video.src = src; video.autoplay = true; }
    else video.dataset.src = src;
    return video;
}

function card(i, rejection = null, row = describeFeedItem(i)) {
    const { files, video, platform } = row;
    const first = files[0];
    const media = first?.video ? makeVideo(first.video, first.image) : first?.image ? el('img', { src: first.image, alt: '', loading: 'lazy' }) : el('div', { className: 'text-only', textContent: (i.text || '').slice(0, 220) });
    const sourceUrl = safeSourcePostUrl(i);
    const ago = i.timestamp ? formatAgo(i.timestamp) : '';
    const title = firstLine(i);
    const rocket = el('button', { type: 'button', className: 'home-card-action', textContent: '🚀', ariaLabel: 'Подготовить пост', onclick: async e => {
        e.stopPropagation();
        try { await composePostFromItem(i, { onDone: () => loadContent().catch(() => {}) }); } catch (error) { log(`Не удалось открыть редактор: ${error.message}`); }
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
            video ? el('span', { className: 'tile-video-mark', ariaLabel: 'Есть видео', textContent: '▶' }) : null,
            files.length > 1 ? el('span', { className: 'tile-media-count', ariaLabel: `Медиафайлов: ${files.length}`, textContent: `▣ ${files.length}` }) : null),
        el('div', { className: 'tile-body' },
            el('b', { textContent: title }),
            el('small', { textContent: [platform, ago].filter(Boolean).join(' · ') }),
            rejection ? el('p', { className: 'rejected-reason', textContent: `Причина: ${rejection.reason || (rejection.stage === 'prescreen' ? 'Вероятный мусор' : 'Не прошёл оценку')}` }) : null,
            rejection ? el('small', { className: 'rejected-stage', textContent: [({ filter: 'Фильтры', prescreen: 'Быстрая оценка' })[rejection.stage] || 'Глубокая оценка', rejection.score != null ? `${rejection.score}%` : '', rejection.at ? formatAgo(rejection.at) : ''].filter(Boolean).join(' · ') }) : null,
            sourceUrl ? el('a', { href: sourceUrl, target: '_blank', rel: 'noopener', textContent: '↗', title: 'Открыть у источника' }) : null,
            el('div', { className: 'home-card-actions' }, rocket, add, rejection ? null : remove)));
}

// The same post viewer is used by the calendar, sources and home.
function openPreview(item) {
    const post = fromRecord(item);
    const openEditor = async scheduled => {
        let scheduledTs = 0;
        if (scheduled) {
            closePostView();
            const date = await pickDateTime({});
            if (!date) return openPreview(item);
            scheduledTs = Math.floor(date.getTime() / 1000);
        } else closePostView();
        try {
            await composePostFromItem(item, { scheduledTs, onDone: () => loadContent().catch(() => {}) });
        } catch (error) {
            log('Не удалось открыть редактор: ' + error.message);
            openPreview(item);
        }
    };
    openPostView([post], 0, {
        title: 'Просмотр поста',
        actions: () => [
            { label: 'Подготовить пост', icon: '🚀', primary: true, setup: button => applyRocketCheck(button, item.post_check, 'Подготовить пост'), run: () => openEditor(false) },
            { label: 'Добавить в будущий пост', setup: button => bindFutureDraftButton(button, () => item, { click: false }), run: () => toggleFutureDraft(item) },
            { label: 'Запланировать', icon: '🕒', run: () => openEditor(true) },
        ],
    });
}

// ---- Сводка подбора ----
const live = { timer: 0, controller: null, gen: 0 };

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
        replaceGrid(grid, el('p', { className: 'muted', textContent: 'Загрузка…' }));
    }
    try {
        const data = await api('studio_rejected');
        if (request !== rejectedRequest) return;
        const records = data.items || [];
        const signature = JSON.stringify(records);
        if (signature === rejectedSignature) return;
        rejectedSignature = signature;
        replaceGrid(grid, ...(records.length ? records.map(record => card(record.item || {}, record)) : [el('p', { className: 'muted', textContent: 'Пока нет сохранённых отклонений. Они появятся после следующих проходов.' })]));
    } catch (error) {
        if (request === rejectedRequest && !silent) replaceGrid(grid, el('p', { className: 'muted', textContent: `Не удалось загрузить: ${error.message}` }));
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
    const state = d.unavailable ? 'Статус недоступен'
        : !d.configured ? 'Тема не настроена'
        : !d.enabled ? 'Автоподбор выключен'
        : d.paused || d.job_paused ? 'Автоподбор на паузе'
        : d.needs_demand ? 'Не задан объём подбора'
        : !d.background_enabled ? 'Фоновый запуск выключен'
        : !d.worker_alive ? 'Обработчик не запущен'
        : d.scheduler_error ? 'Ошибка планировщика'
        : d.running ? 'Идёт автоподбор'
        : !d.job_exists ? 'Ожидает запуска'
        : !d.eligible ? 'Нет активного задания'
        : 'Ожидает следующего прохода';
    $('sourcePipeNow').textContent = state;
    const phase = {
        prepare: 'Подготовка', prepare_legacy: 'Перенос прежних кандидатов',
        prepare_hints: 'Подготовка поиска', prepare_projection: 'Обновление каталога',
        prepare_delivery: 'Подготовка публикаций', search: 'Поиск источников',
        review: 'Проверка источников', delivery_projection: 'Обновление каталога',
        delivery_content: 'Подбор публикаций'
    }[d.stage];
    const next = d.enabled && !d.paused && !d.job_paused && d.eligible && d.next_run_at > 0
        ? 'Следующий запуск: ' + new Date(d.next_run_at * 1000).toLocaleString('ru-RU', {day:'numeric', month:'short', hour:'2-digit', minute:'2-digit'}) : '';
    $('sourcePipeDetail').textContent = !d.configured && !d.unavailable ? 'Настройте тему в разделе «Источники».'
        : [d.mode === 'shadow' ? 'Проверочный режим' : '', d.running ? phase : next].filter(Boolean).join(' · ');
    const hasCounts = !d.unavailable && Number.isFinite(d.candidates);
    const count = n => Number.isFinite(n) ? n.toLocaleString('ru-RU') : '—';
    $('sourcePipeInsight').textContent = [
        'Источников в каталоге темы: ' + count(d.own_sources),
        hasCounts ? 'Проверено источников: ' + count(d.checked) : '',
        'Порог релевантности: ' + count(d.accepted_posts) + ' из ' + count(d.analyzed_posts) + ' постов'
    ].filter(Boolean).join('. ') + '.';
    const failure = {
        budget_exhausted: 'Достигнут лимит автоподбора.',
        news_search_disabled: 'Поиск новостей выключен.',
        news_search_unavailable: 'Поиск новостей недоступен.',
        search_unavailable: 'Поисковые сервисы недоступны.',
        topic_busy: 'Тема занята другим заданием.',
        supply_unavailable: 'Не удалось получить данные о запасе публикаций.',
        classifier_unavailable: 'Модель оценки недоступна.',
        restricted_profile: 'Тема ограничена.',
        other_error: 'Последний проход завершился ошибкой.'
    }[d.failure];
    $('sourcePipeErrors').hidden = !failure || d.running || d.unavailable;
    $('sourcePipeErrors').textContent = failure || '';
    const steps = [
        ['Кандидаты', d.candidates, d.running && d.stage === 'search'],
        ['На испытании', d.probation, d.running && d.stage === 'review'],
        ['Активные', d.active_sources, false],
        ['Временно недоступны', d.cooldown, false],
        ['Отклонены', d.rejected, false]
    ];
    $('sourcePipe').replaceChildren(...steps.map(([name, value, running]) => {
        const status = running ? 'run' : hasCounts ? 'ok' : 'wait';
        return el('div', { className: 'job ' + status },
            el('span', { className: 'dot ' + status, textContent: status === 'ok' ? '✓' : '' }),
            el('span', { textContent: name }), el('span', { className: 'n', textContent: hasCounts ? count(value) : '—' }));
    }));
}

async function loadLive() {
    const gen = ++live.gen;
    clearTimeout(live.timer);
    live.controller?.abort();
    const controller = new AbortController();
    live.controller = controller;
    let r;
    try { r = await api('studio_live', { signal: controller.signal }); } catch (error) {
        if (gen !== live.gen || controller.signal.aborted) return;
        renderPipe.sig = null;
        $('pipeNow').textContent = error.status === 401 ? 'Войдите, чтобы увидеть состояние' : 'Не удалось загрузить состояние';
        $('sourcePipeNow').textContent = error.status === 401 ? 'Войдите, чтобы увидеть состояние' : 'Не удалось загрузить состояние';
        renderSourcePipe.sig = null;
        $('feed').replaceChildren(el('li', { className: 'muted', textContent: error.status === 401 ? 'Войдите, чтобы увидеть материалы.' : 'Не удалось загрузить материалы.' }));
        if (gen === live.gen && error.status !== 401) live.timer = setTimeout(loadLive, 15000);
        return;
    } finally {
        if (live.controller === controller) live.controller = null;
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

    renderPipe(r);
    renderSourcePipe(r.source_discovery);
    $('feed').replaceChildren(...(items.length ? items.slice(0, 5).map(feedItem)
        : [el('li', { className: 'muted', textContent: 'Пока ничего: подборка пуста.' })]));
}

function render() {
    reconcileFeed(state.index.visible(state.filter, state.sort, state.q));
    const filters = state.index.filters;
    const props = id => ({ className: state.filter === id ? 'is-active' : '', onclick: () => { state.filter = id; render(); } });
    const countNode = count => el('em', { textContent: String(count) });
    $('chips').replaceChildren(...filters.map(({ id, label, count }) => el('button', { type: 'button', ...props(id) }, label, countNode(count))));
    $('side-filters').replaceChildren(...filters.slice(0, 4).map(({ id, label, count }) => el('li', props(id), el('span', { textContent: label }), countNode(count))));
}

async function loadContent() {
    const request = ++loadContent.request;
    try {
        const [proposals, info] = await Promise.all([api('proposals'), api('account_info')]);
        if (request !== loadContent.request) return;
        state.items = reuseFeedItems(state.items, proposals.items || []);
        state.index = buildHomeFeed(state.items);
        showProfile(info);
        render();
    } catch (error) {
        if (request === loadContent.request) throw error;
    }
}

loadContent.request = 0;

async function loadSources() {
    const request = ++loadSources.request;
    const r = await api('topic_sources', { params: { summary: '1' } });
    if (request !== loadSources.request) return;
    const counts = r.counts || Object.create(null);
    // Compatibility during a rolling update of the server.
    if (!r.counts) for (const s of r.sources || []) counts[s.platform] = (counts[s.platform] || 0) + 1;
    $('side-sources').replaceChildren(...(Object.keys(counts).length ? Object.entries(counts).sort((a, b) => b[1] - a[1]).map(([p, n]) =>
        el('li', {}, el('i', { textContent: PLATFORMS[p] || '◌' }), el('span', { textContent: p }), el('em', { textContent: String(n) })))
        : [el('li', { className: 'muted', textContent: 'Источников темы пока нет' })]));
}

loadSources.request = 0;

function init() {
    $('sort').onchange = e => { state.sort = e.target.value; render(); };
    $('search').oninput = e => { state.q = e.target.value; render(); };
    document.addEventListener('keydown', e => { if (e.key === '/' && document.activeElement !== $('search')) { e.preventDefault(); $('search').focus(); } });
    const fail = e => { homeCards.clear(); replaceGrid($('grid'), el('p', { className: 'muted', textContent: e.status === 401 ? 'Войдите в приложение, чтобы увидеть материалы.' : `Ошибка: ${e.message}` })); };
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
    onTopicChange(() => { loadContent().catch(fail); loadSources().catch(() => {}); loadLive(); if ($('rejected-tab').classList.contains('active')) loadRejected(); });
    document.addEventListener('posta:pool-updated', loadLive);
    loadSources().catch(() => {});
    renderPipe({});
    loadLive();
    window.addEventListener('parser-stopped', loadLive);
    api('billing_info').then(showPlan).catch(() => {});
}

init();
