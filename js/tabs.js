// tabs.js — вкладки «Предложка» (лайки и «не знаю»), «Дерево» (выбор и смена темы) и «Тест ИИ» (оценка одной картинки).
import { $, DEBUG_MODE, NO_TEXT, VIDEO_ICON, api, el, hide, isTabActive, log, onTabOpen, safeSourcePostUrl, setMessage, show } from './core.js';
import { createProgress } from './progress.js';
import { cachedVideoUrl, releaseVideoSource, storeVideo } from './video-cache.js';
import { openViewerGallery } from './viewer.js';
import { TOPIC_CONFIRM, applyRocketCheck, loadCurrentItem, postItemToTelegram, recordGallery, refreshTopicLabel, saveTopic } from './content.js';

// ---- «Предложка» ----

const proposals = { items: [], filter: 'all' };
// Ролики в карточках автовоспроизводятся, пока видны (с запасом 100px). Ушедший из экрана ролик освобождается полностью.
const visibleVideos = new Set();
const playingVideos = new Set();
function stopVideo(v) {
    playingVideos.delete(v);
    const src = v.getAttribute('src');
    if (src) { v.pause(); v.removeAttribute('src'); v.load(); releaseVideoSource(src); }
}
function forgetVideo(v) {
    visibleVideos.delete(v);
    stopVideo(v);
    videoObserver.unobserve(v);
}
const loadingVideos = new Set();
/** Свежая копия из кэша играет мгновенно. Если копии нет, ролик сразу играет по сети, а копия качается в фоне для следующих открытий. */
async function startVideo(v) {
    loadingVideos.add(v);
    const cached = await cachedVideoUrl(v.dataset.src);
    loadingVideos.delete(v);
    if (!visibleVideos.has(v)) return releaseVideoSource(cached);   // ушёл из экрана, пока проверяли кэш
    if (!cached) storeVideo(v.dataset.src);
    v.src = cached || v.dataset.src;
    playingVideos.add(v);
    v.play().catch(() => {
        stopVideo(v);
        v.closest('.proposal-tile')?.classList.remove('is-loading');   // не запустился: плитка не должна висеть в загрузке
    });
}
function playVisibleVideos() {
    for (const v of visibleVideos) if (!playingVideos.has(v) && !loadingVideos.has(v)) startVideo(v);
}
const videoObserver = new IntersectionObserver(entries => {
    entries.forEach(({ target: v, isIntersecting }) => {
        if (isIntersecting) visibleVideos.add(v);
        else { visibleVideos.delete(v); stopVideo(v); }
    });
    playVisibleVideos();
}, { rootMargin: '100px' });

function mediaTile(slide, onOpen, extra) {
    const tile = el('button', { className: 'proposal-tile is-loading', type: 'button', title: 'Открыть', onclick: e => { e.stopPropagation(); onOpen(); } });   // глобальный обработчик просмотрщика не должен открыть картинку без галереи
    const settled = () => tile.classList.remove('is-loading');
    // У ролика картинка — только постер. Без постера браузер скачал бы весь видеофайл как изображение.
    const poster = slide.kind === 'video' ? (slide.poster || '') : (slide.poster || slide.src || '');
    if (poster) {
        const image = el('img', { src: poster, alt: '', loading: 'lazy', decoding: 'async', onload: settled });
        image.onerror = () => {
            const backup = slide.kind !== 'video' && slide.fallback;
            if (backup && image.getAttribute('src') !== backup) return void (image.src = backup);   // копия из кэша не открылась — исходная ссылка
            image.hidden = true;
            settled();
            tile.classList.add('is-broken');
        };
        tile.append(image);
    }
    if (slide.kind === 'video') {
        if (!poster) settled();
        const video = el('video', { poster: slide.poster || '', muted: true, loop: true, playsInline: true, preload: 'metadata' });
        Object.assign(video.dataset, { src: slide.src, fallback: slide.fallback || '' });
        video.addEventListener('playing', settled, { once: true });
        video.onerror = () => {
            if (video.dataset.fallback && video.getAttribute('src') !== video.dataset.fallback) { releaseVideoSource(video.getAttribute('src')); video.src = video.dataset.fallback; return void video.play().catch(() => stopVideo(video)); }
            forgetVideo(video);
            video.remove();
        };
        tile.append(video, el('span', { className: 'video-mark', innerHTML: VIDEO_ICON }));
        videoObserver.observe(video);
    }
    if (extra > 0) tile.append(el('span', { className: 'proposal-more', textContent: `+${extra}` }));
    return tile;
}

function proposalCard(item) {
    const slides = recordGallery(item).items;
    const shown = slides.slice(0, 6);
    const open = i => { const g = recordGallery(item, i); openViewerGallery(g.items, i, g.onPost, g.postCheck); };
    const media = el('div', { className: `proposal-media mosaic-${Math.max(1, shown.length)}` }, ...shown.map((s, i) => mediaTile(s, () => open(i), i === shown.length - 1 ? slides.length - shown.length : 0)),
        slides.length > 1 ? el('span', { className: 'proposal-count', textContent: `${slides.length} ${slides.length < 5 ? 'файла' : 'файлов'}` }) : null);
    const link = safeSourcePostUrl(item);
    const post = el('button', { className: 'proposal-action', type: 'button', textContent: '🚀 Запостить', onclick: () => postItemToTelegram(item).catch(e => log(`Не удалось открыть редактор: ${e.message}`)) });
    applyRocketCheck(post, item.post_check, '🚀 Запостить в Telegram');
    const remove = el('button', { className: 'proposal-action proposal-hide', type: 'button', textContent: '✕ Убрать', title: 'Убрать из предложки (оценка в истории останется)' });
    remove.onclick = async () => {
        remove.disabled = true;
        try { await api('proposal_hide', { body: { image: item.image } }); proposals.items = proposals.items.filter(o => o !== item); renderProposals(); }
        catch (error) { remove.disabled = false; log(`Не удалось убрать запись: ${error.message}`); }
    };
    const text = String(item.text || '').trim();
    return el('article', { className: 'proposal-card' }, media,
        el('div', { className: 'proposal-meta' }, el('span', { className: `proposal-vote vote-${item.vote}`, textContent: { liked: '🗿 Нравится', undecided: '🤷 Не знаю' }[item.vote] || '' }),
            link ? el('a', { href: link, target: '_blank', rel: 'noopener noreferrer', textContent: item.source || 'Источник' }) : el('span', { textContent: item.source || '' })),
        text && !NO_TEXT.includes(text) ? el('p', { className: 'proposal-text', textContent: text.length > 220 ? text.slice(0, 217) + '…' : text }) : null,
        el('div', { className: 'proposal-actions' }, post, remove));
}

function renderProposals() {
    const visible = proposals.items.filter(i => proposals.filter === 'all' || i.vote === proposals.filter);
    $('proposals-count').textContent = proposals.items.length ? `${visible.length} из ${proposals.items.length}` : '';
    $('proposals-status').textContent = visible.length ? '' : proposals.items.length ? 'В этом разделе пусто.' : 'Пока пусто: сюда попадают записи, которые вы отметили «Нравится» или «Не знаю» на вкладке «Подбор».';
    $('proposals-grid').querySelectorAll('video').forEach(forgetVideo);
    $('proposals-grid').replaceChildren(...visible.map(proposalCard));
    document.querySelectorAll('[data-proposal-filter]').forEach(b => b.classList.toggle('active', b.dataset.proposalFilter === proposals.filter));
}

let proposalsPollTimer = null;

/** Полоса проверки медиа в источниках: сервер отдаёт, сколько записей проверено из скольких. */
const proposalBar = createProgress({ className: 'proposals-progress' });
/** Полоса закреплена внизу экрана, поэтому её появление не сдвигает верстку. */
function setProposalProgress(progress) {
    if (!progress?.total) return proposalBar.root.remove();
    proposalBar.set(progress.done || 0, progress.total);
    if (!proposalBar.root.isConnected) document.body.append(proposalBar.root);
}

/** Полоса загрузки самой вкладки: сначала запрос к серверу (объём неизвестен), потом картинки в видимой части списка. */
const listBar = createProgress({ className: 'proposals-loader' });
/** Полоса над карточками: появляется на время загрузки и гаснет сразу после 100%. */
function showListBar() {
    listBar.root.classList.remove('is-fading');
    $('proposals-grid').prepend(listBar.root);   // внутри сетки и абсолютно: сетка не сдвигается
}
function hideListBar() {
    listBar.root.classList.add('is-fading');
    setTimeout(() => { if (listBar.root.classList.contains('is-fading')) listBar.root.remove(); }, 300);
}
let mediaTimer = null;
let loadTimer = null;
let loadPercent = 0;
let responded = false;
/** Полоса только растёт, поэтому скачков назад не бывает. */
function setLoadPercent(value) {
    loadPercent = Math.max(loadPercent, Math.min(100, value));
    listBar.percent(loadPercent);
}
/** 0–30%: ожидание ответа сервера. Объём заранее неизвестен, поэтому плавно замедляется к 30%. */
function startLoadBar() {
    clearInterval(loadTimer);
    loadPercent = 0;
    responded = false;
    const started = Date.now();
    showListBar();
    listBar.percent(0);
    loadTimer = setInterval(() => {
        if (!responded) setLoadPercent(30 * (1 - Math.exp(-(Date.now() - started) / 1200)));
    }, 100);
}
/** 30–100%: доля картинок в видимой части, которые уже загрузились. */
function trackProposalMedia() {
    clearInterval(mediaTimer);
    clearInterval(loadTimer);
    responded = true;
    setLoadPercent(30);
    const inView = t => { const r = t.getBoundingClientRect(); return r.bottom > 0 && r.top < innerHeight && r.right > 0 && r.left < innerWidth; };
    const started = Date.now();
    const finish = () => { clearInterval(mediaTimer); setLoadPercent(100); setTimeout(hideListBar, 400); };
    const tick = () => {
        const tiles = [...$('proposals-grid').querySelectorAll('.proposal-tile')].filter(inView);
        if (!tiles.length || Date.now() - started > 20000) return finish();
        const done = tiles.filter(t => !t.classList.contains('is-loading')).length;
        setLoadPercent(30 + 70 * done / tiles.length);
        if (done === tiles.length) finish();
    };
    mediaTimer = setInterval(tick, 150);
    tick();
}

async function loadProposals() {
    clearTimeout(proposalsPollTimer);
    clearInterval(mediaTimer);
    try {
        const status = $('proposals-status');
        if (!proposals.items.length) status.textContent = 'Загружаю предложки...';
        status.style.opacity = '0.7';
        startLoadBar();

        const start = Date.now();
        const data = await api('proposals');
        const elapsed = Date.now() - start;

        proposals.items = data.items || [];
        renderProposals();

        status.textContent = `${proposals.items.length} предложек${elapsed > 1000 ? ` (${(elapsed/1000).toFixed(1)}с)` : ''}`;
        trackProposalMedia();
        if (data.refreshing) {
            status.append(' • проверяю медиа в источниках');
            setProposalProgress(data.progress);
        }
        status.style.opacity = '1';

        if (data.refreshing) pollProposalProgress();
    } catch (error) {
        clearInterval(mediaTimer);
        clearInterval(loadTimer);
        listBar.root.remove();
        $('proposals-status').textContent = `Не удалось загрузить предложку: ${error.message}`;
        $('proposals-status').style.opacity = '1';
    }
}

/** Пока сервер достраивает медиа, обновляет полосу раз в 250 мс (лёгкий запрос). По окончании перечитывает список один раз. */
async function pollProposalProgress() {
    clearTimeout(proposalsPollTimer);
    if (!isTabActive('proposals-tab')) return;
    try {
        const data = await api('proposals_progress');
        setProposalProgress(data.progress);
        if (data.refreshing) proposalsPollTimer = setTimeout(pollProposalProgress, 250);
        else loadProposals();
    } catch { /* сеть моргнула — следующий опрос не нужен, список обновит кнопка «↻» */ }
}

// ---- «Дерево»: общие темы — корни, частные — внутри; справа — панель выбранной темы ----

const tree = { topics: [], current: null, selected: null, busy: false, parentOpen: false };

/** Всё дерево развёрнуто сразу: общая тема — карточка, частные — кнопки внутри. Клик открывает тему в панели справа. */
function selectTopic(id) {
    Object.assign(tree, { selected: id, parentOpen: false });
    renderTree();
    renderDetail();
}

function renderTree() {
    const ids = new Set(tree.topics.map(t => t.id));
    const children = new Map();
    const broads = [];
    for (const t of tree.topics) {
        if (t.parent?.id && ids.has(t.parent.id)) children.set(t.parent.id, [...(children.get(t.parent.id) || []), t]);
        else broads.push(t);
    }
    const byTitle = (a, b) => a.title.localeCompare(b.title, 'ru');
    broads.sort((a, b) => (b.scope === 'broad') - (a.scope === 'broad') || byTitle(a, b));
    const stateClass = t => ['', tree.current === t.id && 'is-current', tree.selected === t.id && 'is-selected'].filter(Boolean);
    const chip = t => el('button', { type: 'button', className: ['tree-chip', ...stateClass(t)].join(' '), title: t.about || t.title, onclick: () => selectTopic(t.id) },
        el('span', { className: 'tree-chip-name', textContent: t.title }),
        t.own_sources ? el('span', { className: 'tree-count', textContent: String(t.own_sources), title: 'Свои источники темы' }) : null,
        tree.current === t.id ? el('span', { className: 'tree-chip-mark', textContent: '📍', 'aria-hidden': 'true' }) : null);
    const group = t => {
        const kids = (children.get(t.id) || []).sort(byTitle);
        return el('section', { className: ['tree-group', ...stateClass(t)].join(' ') },
            el('button', { type: 'button', className: 'tree-group-head', title: t.about || t.title, onclick: () => selectTopic(t.id) },
                el('span', { className: 'tree-group-name', textContent: t.title }),
                t.own_sources ? el('span', { className: 'tree-count', textContent: String(t.own_sources), title: 'Свои источники темы' }) : null),
            kids.length ? el('div', { className: 'tree-chips' }, ...kids.map(chip)) : el('p', { className: 'tree-group-empty', textContent: 'Частных тем пока нет' }));
    };
    $('tree-root').replaceChildren(...broads.map(group));
    $('tree-current-name').textContent = tree.topics.find(t => t.id === tree.current)?.title || '—';
    $('tree-status').textContent = tree.topics.length ? '' : 'Тем пока нет: введите первую тему выше.';
}

function renderDetail() {
    const t = tree.topics.find(x => x.id === tree.selected);
    if (!t) return $('tree-detail').replaceChildren(el('p', { className: 'tree-detail-empty', textContent: 'Выберите тему в дереве: здесь её описание, критерии отбора и действия.' }));
    const row = (label, value) => value ? el('div', { className: 'tree-row' }, el('dt', { textContent: label }), el('dd', {}, value)) : null;
    const sources = t.uses_parent_sources ? `${t.own_sources || 0} своих. Пока берутся источники общей темы «${t.parent.title}».`
        : t.own_sources ? `${t.own_sources} своих — по ним идёт сбор.` : 'Своих пока нет: автоподбор ищет их, как только тема становится текущей.';
    const action = (className, textContent, onclick, title = '') => el('button', { className, type: 'button', textContent, onclick, title });
    $('tree-detail').replaceChildren(el('h3', { className: 'tree-detail-title', textContent: t.title }),
        el('p', { className: 'tree-detail-kind', textContent: (t.scope === 'broad' ? '🗂 общая тема' : `🔹 частная тема${t.parent ? ` в «${t.parent.title}»` : ''}`) + (t.id === tree.current ? ' · 📍 сейчас выбрана' : '') }),
        el('dl', { className: 'tree-facts' }, row('О теме', t.about), row('Подходит', t.criteria), row('Не подходит', t.exclude), row('Вопрос к картинке', t.question),
            row('Ещё называют', t.aliases?.join(', ')), row('Источники', sources), row('Частные темы', t.children?.join(', ')), t.profile ? null : row('Профиль', 'ещё не составлен: напишется при следующем запуске сборщика.')),
        el('div', { className: 'tree-actions' }, t.id === tree.current ? null : action('primary-button', 'Сделать текущей', () => choose(t.title)),
            action('secondary-button', 'Сменить общую тему', () => { tree.parentOpen = !tree.parentOpen; $('tree-parent-editor').hidden = !tree.parentOpen; if (tree.parentOpen) $('tree-parent-editor').querySelector('select').focus(); }),
            action('secondary-button', 'Пересоставить профиль', async () => {
                try { await api('topic_reprofile', { body: { id: t.id } }); log(`Профиль темы «${t.title}» пересоставится при следующем запуске сборщика.`); await loadTree(); }
                catch (error) { setMessage('tree-status', `Профиль не сброшен: ${error.message}`); }
            }, 'Критерии и план поиска напишутся заново')),
        parentEditor(t), el('div', { className: 'tree-record' }, el('p', { className: 'tree-detail-empty', textContent: 'Загружаю полную запись…' })));
    loadTopicRecord(t.id);
}

const TOPIC_LABELS = { id: 'ID', title: 'Название', scope: 'Тип', parent: 'Общая тема', aliases: 'Ещё называют', about: 'Описание', classified: 'Классифицирована', smart: 'Классифицирована ИИ', manual: 'Создана вручную', created_at: 'Создана' };
const PROFILE_LABELS = { criteria: 'Подходит', exclude: 'Не подходит', question: 'Вопрос к картинке', queries: 'Поисковые запросы', telegram: 'Поиск в Telegram', v: 'Версия профиля', smart: 'Профиль составлен ИИ', at: 'Профиль составлен' };
const TIME_FIELDS = ['created_at', 'at'];

/** Значение поля для показа: читаемо, а сложные структуры целиком, как JSON. */
function recordValue(key, value) {
    if (value === null || value === undefined || value === '') return '—';
    if (typeof value === 'boolean') return value ? 'да' : 'нет';
    if (TIME_FIELDS.includes(key)) return value ? new Date(value * 1000).toLocaleString('ru-RU') : '—';
    if (key === 'scope') return value === 'broad' ? 'общая тема' : 'частная тема';
    if (Array.isArray(value) && value.every(x => x === null || typeof x !== 'object')) return value.length ? value.join(', ') : '—';
    if (typeof value === 'object') return el('pre', { textContent: JSON.stringify(value, null, 2) });
    return String(value);
}

/** Все поля записи темы из базы, включая профиль. Неизвестные поля тоже выводятся, под своим именем. */
function recordRows(topic) {
    const rows = [];
    const add = (label, key, value) => rows.push(el('div', { className: 'tree-row' }, el('dt', { textContent: label }), el('dd', {}, recordValue(key, value))));
    for (const [key, value] of Object.entries(topic)) {
        if (key === 'profile') continue;
        const shown = key === 'parent' && value ? (tree.topics.find(x => x.id === value)?.title ?? value) : value;
        add(TOPIC_LABELS[key] || key, key, shown);
    }
    for (const [key, value] of Object.entries(topic.profile || {})) add(PROFILE_LABELS[key] || `Профиль: ${key}`, key, value);
    return rows;
}

async function loadTopicRecord(id) {
    const box = () => $('tree-detail').querySelector('.tree-record');
    try {
        const { topic } = await api('topic_record', { params: { id } });
        if (tree.selected !== id) return;   // пока грузилось, выбрали другую тему
        box()?.replaceChildren(el('h4', { className: 'tree-record-title', textContent: 'Полная запись из базы' }), el('dl', { className: 'tree-facts' }, ...recordRows(topic)));
    } catch (error) {
        if (tree.selected === id) box()?.replaceChildren(el('p', { className: 'tree-detail-empty', textContent: `Запись не загрузилась: ${error.message}` }));
    }
}

function parentEditor(t) {
    const select = el('select', { className: 'tree-parent-select', ariaLabel: 'Общая тема' }, el('option', { value: '', textContent: '— это сама общая тема —' }),
        ...tree.topics.filter(x => x.scope === 'broad' && x.id !== t.id).map(x => el('option', { value: x.id, textContent: x.title })), el('option', { value: '__new', textContent: 'Новая общая тема…' }));
    select.value = t.scope === 'broad' ? '' : t.parent?.id || '__new';
    const input = el('input', { className: 'tree-parent-new', type: 'text', maxLength: 40, placeholder: 'Название новой общей темы', autocomplete: 'off', hidden: select.value !== '__new' });
    select.onchange = () => { input.hidden = select.value !== '__new'; if (!input.hidden) input.focus(); };
    const save = el('button', { className: 'secondary-button', type: 'button', textContent: 'Готово' });
    save.onclick = async () => {
        const parent = select.value === '__new' ? input.value.trim() : select.value;
        if (select.value === '__new' && !parent) return input.focus();
        save.disabled = true;
        try {
            const { topic_info: i } = await api('topic_parent', { body: { id: t.id, parent } });
            log(i.parent ? `Тема «${i.title}» теперь внутри «${i.parent.title}».` : `Тема «${i.title}» теперь общая.`);
            await loadTree();
        } catch (error) { setMessage('tree-status', `Общая тема не сохранена: ${error.message}`); }
        finally { save.disabled = false; }
    };
    input.onkeydown = e => { if (e.key === 'Enter') { e.preventDefault(); save.click(); } };
    return el('div', { className: 'tree-parent-editor', id: 'tree-parent-editor', hidden: !tree.parentOpen }, select, input, save);
}

function setTreeBusy(busy, message = '') {
    tree.busy = $('tree-save').disabled = $('tree-input').disabled = busy;
    $('tree-root').classList.toggle('is-busy', busy);
    setMessage('tree-status', message);
}

async function choose(text, forceNew = false) {
    const typed = text.trim();
    const current = tree.topics.find(t => t.id === tree.current);
    if (!typed || tree.busy || (!forceNew && current?.title.toLowerCase() === typed.toLowerCase()) || !confirm(TOPIC_CONFIRM)) return;
    hide('tree-notice');
    setTreeBusy(true, 'Нейросеть определяет тему: название и общую тему (до минуты)…');
    try {
        const data = await saveTopic(typed, forceNew, 'tree-notice');
        $('tree-input').value = '';
        tree.current = tree.selected = data.topic_info?.id ?? null;
        await loadTree();
        await refreshTopicLabel();
        await loadCurrentItem();
    } catch (error) { setMessage('tree-status', `Тема не сохранена: ${error.message}`); }
    finally { setTreeBusy(false); }
}

async function loadTree() {
    if (DEBUG_MODE) return setMessage('tree-status', 'В демо-режиме дерево тем не показывается.');
    try {
        const data = await api('topics');
        Object.assign(tree, { topics: data.topics || [], current: data.current ?? null });
        if (!tree.topics.some(t => t.id === tree.selected)) tree.selected = tree.current;
        $('tree-options').replaceChildren(...tree.topics.map(t => el('option', { value: t.title, label: t.path })));
        renderTree();
        renderDetail();
    } catch (error) { setMessage('tree-status', `Не удалось загрузить темы: ${error.message}`); }
}

// ---- «Тест ИИ» ----

let previewUrl = '';
function setPreview(source) {
    if (previewUrl) URL.revokeObjectURL(previewUrl);
    previewUrl = source instanceof File ? URL.createObjectURL(source) : '';
    const url = previewUrl || (typeof source === 'string' ? source : '');
    if (url) $('test-image').src = url; else $('test-image').removeAttribute('src');
}

async function analyze(e) {
    e.preventDefault();
    const [file, url] = [$('image-file').files[0], $('image-url').value.trim()];
    setMessage('test-error', '');
    hide('test-result');
    if (!file && !url) return setMessage('test-error', 'Добавьте ссылку или выберите файл изображения.');
    if (file?.size > 10 * 1024 * 1024) return setMessage('test-error', 'Файл больше 10 МБ. Выберите изображение поменьше.');
    $('analyze-button').disabled = true;
    show('test-loading');
    try {
        const body = new FormData();
        file ? body.append('image_file', file) : body.append('image_url', url);
        const d = await api('analyze_image', { body });
        if (!file) setPreview(url);
        setMessage('test-rating', d.rating == null ? '' : `${Number(d.rating)}%`);
        $('test-relevance').textContent = `По теме: ${Number(d.relevance)}%`;
        $('test-model').textContent = `Тема «${d.topic}» · ${d.rating == null ? '' : 'прогноз по вашим оценкам · '}${d.model}`;
        $('test-description').replaceChildren(el('strong', { textContent: 'Описание ИИ:' }), ` ${d.description || 'Описание не получено'}`);
        $('test-why').textContent = d.why_relevant ? `Почему по теме: ${d.why_relevant}` : '';
        show('test-result');
    } catch (error) { setMessage('test-error', `Не удалось оценить изображение: ${error.message}`); }
    finally { hide('test-loading'); $('analyze-button').disabled = false; }
}

export function initializeTabs() {
    document.querySelectorAll('[data-proposal-filter]').forEach(b => b.addEventListener('click', () => { proposals.filter = b.dataset.proposalFilter; renderProposals(); }));
    $('proposals-refresh').addEventListener('click', loadProposals);
    onTabOpen('proposals-tab', loadProposals);

    $('tree-form').addEventListener('submit', e => { e.preventDefault(); choose($('tree-input').value); });
    $('tree-force-new').addEventListener('click', e => choose(e.currentTarget.dataset.typed || $('tree-input').value, true));
    $('tree-refresh').addEventListener('click', loadTree);
    if (DEBUG_MODE) { hide('tree-form'); hide('tree-refresh'); }
    onTabOpen('tree-tab', loadTree);

    $('image-file').addEventListener('change', e => { if (e.target.files[0]) $('image-url').value = ''; setPreview(e.target.files[0] || null); hide('test-result'); });
    $('image-url').addEventListener('input', () => { if ($('image-url').value) { $('image-file').value = ''; setPreview(null); } });
    $('analyze-form').addEventListener('submit', analyze);
}
