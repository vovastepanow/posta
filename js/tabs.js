// tabs.js — вкладки «Предложка» (лайки и «не знаю»), «Дерево» (выбор и смена темы) и «Тест ИИ» (оценка одной картинки).
import { $, DEBUG_MODE, NO_TEXT, VIDEO_ICON, api, el, hide, isTabActive, log, onTabOpen, onTopicChange, renderTags, safeSourcePostUrl, setMessage, show } from './core.js';
import { createProgress } from './progress.js';
import { cachedVideoUrl, releaseVideoSource, storeVideo } from './video-cache.js';
import { openViewerGallery } from './viewer.js';
import { articleCover, cardKind, shortText } from './card.js';
import { openArticleReader } from './article.js';
import { TOPIC_CONFIRM, applyRocketCheck, loadCurrentItem, postItemToTelegram, recordGallery, refreshTopicLabel, saveTopic, textPostBlock } from './content.js';

/** Публикация из «Предложки»: успех отмечает запись лайком, и карточка уходит из списка сразу (как после «Убрать»). */
function postFromProposal(item) {
    return postItemToTelegram(item, { onDone: result => {
        if (!result?.marked) return;
        proposals.items = proposals.items.filter(o => o !== item);
        renderProposals();
    } }).catch(e => log(`Не удалось открыть редактор: ${e.message}`));
}
import { itemCheck, loadImage, videoProgress, watchVideo } from './video-pending.js';

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

/** Ролик ещё качается: по готовности подпись уходит, запись получает ролик — окно просмотра покажет его. */
function watchProposalVideo(tile, item) {
    const bar = videoProgress(tile);   // полная полоса со стадией: вместо пометки «Видео загружается…»
    watchVideo(itemCheck(item.image), {
        onProgress: p => bar.update(p),
        onReady: r => { bar.stop(); item.video = r.video_local || r.video; item.display_video = r.video; tile.querySelector('.proposal-pending')?.remove(); },
        onFail: () => { bar.stop(); tile.querySelector('.proposal-pending')?.replaceWith(el('span', { className: 'proposal-pending', textContent: 'Видео не скачалось' })); }
    });
}

function mediaTile(slide, onOpen, extra, item) {
    if (slide.kind === 'text') return el('div', { className: 'proposal-tile is-text' }, textPostBlock(slide.text));   // текст без картинки
    const tile = el('button', { className: 'proposal-tile is-loading', type: 'button', title: 'Открыть', onclick: e => { e.stopPropagation(); onOpen(); } });   // глобальный обработчик просмотрщика не должен открыть картинку без галереи
    const settled = () => tile.classList.remove('is-loading');
    // Ролик TikTok/Shorts ещё качается в фоне: до этого показываем обложку с пометкой
    if (['ytdlp', 'tgu'].includes(item?.video_via) && !item.video) {
        tile.append(el('span', { className: 'proposal-pending', textContent: 'Видео загружается…' }));
        if (item.image) watchProposalVideo(tile, item);
    }
    // У ролика картинка — только постер. Без постера браузер скачал бы весь видеофайл как изображение.
    const poster = slide.kind === 'video' ? (slide.poster || '') : (slide.poster || slide.src || '');
    if (poster) {
        const image = el('img', { alt: '', decoding: 'async', onload: settled });
        loadImage(image, poster, tile, { compact: true });   // дольше двух секунд — полоса на плитке
        image.onerror = () => {
            const backup = slide.kind !== 'video' && slide.fallback;
            if (backup && image.getAttribute('src') !== backup) return void (image.src = backup);   // копия из кэша не открылась — исходная ссылка
            image.hidden = true;
            settled();
            tile.classList.add('is-broken');
            // Добавить кнопку загрузки для записей с источником
            if (item && item.source_url) {
                const label = el('span', { className: 'proposal-broken-text', textContent: proposals.refreshing ? 'Подгружается…' : 'Файл пропал' });
                const refetch = el('button', { className: 'proposal-refetch', type: 'button', textContent: '↻ Загрузить заново', onclick: async e => {
                    e.stopPropagation();
                    refetch.disabled = true;
                    label.textContent = 'Загружаю…';
                    try {
                        const result = await api('proposal_refetch', { body: { image: item.image } });
                        if (result?.gone) { proposals.items = proposals.items.filter(o => o !== item); renderProposals(); }   // источник удалил пост — показывать нечего
                        else await loadProposals();
                    } catch (error) {
                        log(`Не удалось загрузить медиа: ${error.message}`);
                        label.textContent = error.message || 'Не удалось загрузить';
                        refetch.disabled = false;
                    }
                }});
                const overlay = el('div', { className: 'proposal-broken-overlay' }, label, refetch);
                tile.append(overlay);
            }
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
            const mosaic = tile.parentElement;
            if (mosaic && mosaic.querySelectorAll('.proposal-tile').length > 1) {
                tile.remove();
                mosaic.className = mosaic.className.replace(/mosaic-\d+/, `mosaic-${Math.min(6, mosaic.querySelectorAll('.proposal-tile').length)}`);
            }
        };
        tile.append(video, el('span', { className: 'video-mark', innerHTML: VIDEO_ICON }));
        videoObserver.observe(video);
    }
    if (extra > 0) tile.append(el('span', { className: 'proposal-more', textContent: `+${extra}` }));
    return tile;
}

/** Кнопки записи в предложке: «Запостить» (с проверкой ракеты) и «Убрать». */
function proposalActions(item) {
    const post = el('button', { className: 'proposal-action', type: 'button', textContent: '🚀 Запостить', onclick: () => postFromProposal(item) });
    applyRocketCheck(post, item.post_check, '🚀 Запостить в Telegram');
    const remove = el('button', { className: 'proposal-action proposal-hide', type: 'button', textContent: '✕ Убрать', title: 'Убрать из предложки (оценка в истории останется)' });
    remove.onclick = async () => {
        remove.disabled = true;
        try { await api('proposal_hide', { body: { image: item.image } }); proposals.items = proposals.items.filter(o => o !== item); renderProposals(); }
        catch (error) { remove.disabled = false; log(`Не удалось убрать запись: ${error.message}`); }
    };
    return el('div', { className: 'proposal-actions' }, post, remove);
}

/** Статья в предложке: обложка (или плашка), заголовок до двух строк, сайт · ⏱ N мин, анонс до трёх строк; клик — читалка. */
function proposalArticleCard(item) {
    const a = item.article || {};
    const read = () => openArticleReader(item, { onPost: () => postFromProposal(item) });
    const lead = shortText(a.lead || '', 200);
    return el('article', { className: 'proposal-card proposal-article' },
        el('div', { className: 'proposal-article-open', onclick: read }, articleCover(item)),
        el('div', { className: 'proposal-meta' }, el('span', { className: `proposal-vote vote-${item.vote}`, textContent: { liked: '🗿 Нравится', undecided: '🤷 Не знаю' }[item.vote] || '' }),
            el('span', { textContent: [a.site, a.minutes ? `⏱ ${a.minutes} мин` : ''].filter(Boolean).join(' · ') })),
        el('h3', { className: 'proposal-article-title', textContent: a.title || shortText(item.text, 120) }),
        lead ? el('p', { className: 'proposal-text', textContent: lead }) : null,
        el('button', { type: 'button', className: 'proposal-article-read', textContent: 'Читать статью', onclick: read }),
        proposalActions(item));
}

function proposalCard(item) {
    if (cardKind(item) === 'article') return proposalArticleCard(item);
    const slides = item.text_post && !item.image && !item.display_image ? [{ kind: 'text', text: item.text || '', textPost: true }] : recordGallery(item).items;
    const shown = slides.slice(0, 6);
    const refetch = item.source_url ? async () => {
        const result = await api('proposal_refetch', { body: { image: item.image } });
        if (result?.gone) { proposals.items = proposals.items.filter(o => o !== item); renderProposals(); } else await loadProposals();
    } : null;
    const open = i => { const g = recordGallery(item, i); openViewerGallery(g.items.map(s => ({ ...s, refetch })), i, g.onPost, g.postCheck); };
    const media = el('div', { className: `proposal-media mosaic-${Math.max(1, shown.length)}` }, ...shown.map((s, i) => mediaTile(s, () => open(i), i === shown.length - 1 ? slides.length - shown.length : 0, item)),
        slides.length > 1 ? el('span', { className: 'proposal-count', textContent: `${slides.length} ${slides.length < 5 ? 'файла' : 'файлов'}` }) : null);
    const link = safeSourcePostUrl(item);
    const text = String(item.text || '').trim();
    return el('article', { className: 'proposal-card' }, media,
        el('div', { className: 'proposal-meta' }, el('span', { className: `proposal-vote vote-${item.vote}`, textContent: { liked: '🗿 Нравится', undecided: '🤷 Не знаю' }[item.vote] || '' }),
            link ? el('a', { href: link, target: '_blank', rel: 'noopener noreferrer', textContent: item.source || 'Источник' }) : el('span', { textContent: item.source || '' })),
        text && !NO_TEXT.includes(text) ? el('p', { className: 'proposal-text', textContent: shortText(text, 220) }) : null,
        proposalActions(item));
}

function renderProposals() {
    const visible = proposals.items.filter(i => proposals.filter === 'all' || i.vote === proposals.filter);
    $('proposals-count').textContent = proposals.items.length ? `${visible.length} из ${proposals.items.length}` : '';
    $('proposals-status').textContent = visible.length ? '' : proposals.items.length ? 'В этом разделе пусто.' : 'Пока пусто: сюда попадают записи, которые вы отметили «Нравится» или «Не знаю» на вкладке «Подбор».';
    $('proposals-grid').querySelectorAll('video').forEach(forgetVideo);

    // Посчитать записи с пропавшими файлами
    const brokenCount = visible.filter(i => !i.display_image && !i.display_video).length;
    const cards = visible.map(proposalCard);
    const container = [];
    if (brokenCount > 5) {
        const repairBtn = el('button', { className: 'proposal-repair', type: 'button', textContent: 'Загрузить все заново', onclick: async () => {
            repairBtn.disabled = true;
            try {
                await api('proposal_repair');
                await loadProposals();
            } catch (error) {
                log(`Ошибка при загрузке медиа: ${error.message}`);
                repairBtn.disabled = false;
            }
        }});
        container.push(el('div', { className: 'proposal-repair-notice' },
            el('p', { textContent: `У ${brokenCount} записей пропали файлы` }),
            repairBtn));
    }

    $('proposals-grid').replaceChildren(...container, ...cards);
    document.querySelectorAll('[data-proposal-filter]').forEach(b => b.classList.toggle('active', b.dataset.proposalFilter === proposals.filter));
}

let proposalsPollTimer = null;

/** Полоса проверки медиа в источниках: сервер отдаёт, сколько записей проверено из скольких. */
const proposalBar = createProgress({ className: 'proposals-progress' });
/** Полоса закреплена внизу экрана, поэтому её появление не сдвигает верстку. */
function setProposalProgress(progress) {
    if (!progress?.total || progress.done >= progress.total || !isTabActive('proposals-tab')) return proposalBar.root.remove();   // закончено или вкладка не открыта — полосы нет
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
        proposals.refreshing = !!data.refreshing;   // пока идёт достройка, пропавшие файлы не «пропали», а ещё качаются
        proposalsReloadedAt = Date.now();
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
let proposalsReloadedAt = 0;

async function pollProposalProgress() {
    clearTimeout(proposalsPollTimer);
    if (!isTabActive('proposals-tab')) return proposalBar.root.remove();   // ушли с вкладки: полоса не остаётся поверх других
    try {
        const data = await api('proposals_progress');
        setProposalProgress(data.progress);
        if (data.refreshing && Date.now() - proposalsReloadedAt > 8000) loadProposals();   // докачанные файлы появляются по ходу, а не в конце
        else if (data.refreshing) proposalsPollTimer = setTimeout(pollProposalProgress, 250);
        else loadProposals();
    } catch { /* сеть моргнула — следующий опрос не нужен, список обновит кнопка «↻» */ }
}

// ---- «Дерево»: рубрики (общие темы) слева, карточки частных тем по центру, паспорт выбранной темы справа ----
// Раскладку выбирает ширина самой вкладки (data-width на #tree-tab): консоль занимает левую колонку, поэтому по окну не угадать.

const tree = { topics: [], current: null, selected: null, rubric: null, query: '', busy: false, parentOpen: false, width: 'narrow', sheetOpen: false, tagsOpen: null };
const RUBRIC_KEY = 'postaTreeRubric';
const RUBRIC_ICONS = { adult: '🔞', shock: '💥', humor: '😂', internet: '🌐', animals: '🐾', people: '👥', games: '🎮', screen: '🎬', music: '🎵', celebs: '⭐', news: '📰', tech: '💻', money: '💰', expert: '🎓', science: '🔬', food: '🍔', travel: '✈️', style: '👗', cars: '🚗', hobby: '🎨', sport: '⚽', other: '📁' };

/** Поиск: строчные, ё → е, без «#», пробелы и дефисы → «_» — как hashtag_norm на сервере. */
const treeNorm = s => String(s ?? '').toLowerCase().replace(/ё/g, 'е').replace(/#/g, '').trim().replace(/[\s-]+/g, '_');

/** Дерево как индексы: ветки-рубрики, их частные темы, рубрика любой темы. */
function treeModel() {
    const ids = new Map(tree.topics.map(t => [t.id, t]));
    const children = new Map();
    const rubrics = [];
    for (const t of tree.topics) {
        if (t.parent?.id && ids.has(t.parent.id)) children.set(t.parent.id, [...(children.get(t.parent.id) || []), t]);
        else rubrics.push(t);
    }
    const byTitle = (a, b) => a.title.localeCompare(b.title, 'ru');
    rubrics.sort((a, b) => (b.scope === 'broad') - (a.scope === 'broad') || byTitle(a, b));
    for (const list of children.values()) list.sort(byTitle);
    const rubricOf = t => (t.parent?.id && ids.has(t.parent.id) ? ids.get(t.parent.id) : t);
    return { ids, children, rubrics, rubricOf };
}

/** Хэштеги темы для показа: рубрика первой, потом имена. */
const topicTags = t => [t.hashtags?.rubric, ...(t.hashtags?.entities || [])].filter(Boolean);

/** Совпадение с запросом: название, синонимы, описание, хэштеги и их синонимы. Пустой запрос совпадает со всем. */
function treeMatches(t, q) {
    return !q || [t.title, ...(t.aliases || []), t.about, ...topicTags(t).map(h => `#${h}`), ...(t.hashtags?.synonyms || [])].some(text => treeNorm(text).includes(q));
}

/** Текст с подсветкой найденного: массив узлов и строк для el(). Совпадение ищем по строке без замен длины. */
function treeHighlight(text, q) {
    const raw = String(text ?? '');
    const at = q ? raw.toLowerCase().replace(/ё/g, 'е').replace(/[\s-]/g, '_').indexOf(q) : -1;
    return at < 0 ? [raw] : [raw.slice(0, at), el('mark', { textContent: raw.slice(at, at + q.length) }), raw.slice(at + q.length)];
}

function readRubric() { try { return localStorage.getItem(RUBRIC_KEY); } catch { return null; } }
function rememberRubric(id) { try { localStorage.setItem(RUBRIC_KEY, id); } catch { /* приватный режим: просто не запомним */ } }

function setTreeWidth(width) {
    tree.width = width;
    $('tree-tab').dataset.width = width;
    updateTreeSheet();
}

/** Нижний лист на телефоне: открыт только в узкой раскладке и только после выбора карточки. */
function updateTreeSheet() {
    const open = tree.width === 'narrow' && tree.sheetOpen;
    $('tree-detail').classList.toggle('is-open', open);
    $('tree-backdrop').hidden = !open;
    document.body.classList.toggle('tree-sheet-open', open);
}

function closeTreeSheet() { tree.sheetOpen = false; updateTreeSheet(); }

function selectTopic(id, fromCard = false) {
    const model = treeModel();
    const t = model.ids.get(id);
    if (!t) return;
    Object.assign(tree, { selected: id, rubric: model.rubricOf(t).id, parentOpen: false });
    rememberRubric(tree.rubric);
    if (fromCard) tree.sheetOpen = tree.width === 'narrow';
    renderTree();
    renderDetail();
    updateTreeSheet();
}

/** Рубрика: её карточки и она сама в паспорте. */
function pickRubric(id) {
    Object.assign(tree, { rubric: id, selected: id });
    rememberRubric(id);
    renderTree();
    renderDetail();
}

function renderRubrics(model, q, rubricId, liveRubricId) {
    const rubrics = model.rubrics.map(r => {
        const kids = model.children.get(r.id) || [];
        const found = q ? [r, ...kids].filter(t => treeMatches(t, q)) : kids;
        return { r, found };
    }).filter(({ found }) => !q || found.length);
    $('tree-rubrics').replaceChildren(...rubrics.map(({ r, found }) => {
        const live = r.id === liveRubricId;
        return el('button', { type: 'button', className: ['tree-rubric', r.id === rubricId && 'is-active'].filter(Boolean).join(' '), title: r.about || r.title,
                'aria-pressed': String(r.id === rubricId), onclick: () => pickRubric(r.id) },
            el('span', { className: 'tree-rubric-icon', textContent: RUBRIC_ICONS[r.key] ?? '📁', 'aria-hidden': 'true' }),
            el('span', { className: 'tree-rubric-name', textContent: r.title }),
            el('span', { className: 'tree-rubric-count', textContent: String(found.length), title: q ? 'Найдено' : 'Частных тем' }),
            live ? el('span', { className: 'tree-air', textContent: 'В ЭФИРЕ' }) : null);
    }));
}

function treeCard(t, model, q) {
    const rubric = model.rubricOf(t);
    const whole = t.id === rubric.id;
    const isCurrent = tree.current === t.id;
    const nsfw = t.nsfw || rubric.nsfw;
    const tags = topicTags(t);
    const classes = ['tree-card', isCurrent && 'is-current', tree.selected === t.id && 'is-selected'].filter(Boolean).join(' ');
    return el('button', { type: 'button', className: classes, title: t.about || t.title, 'aria-pressed': String(tree.selected === t.id), onclick: () => selectTopic(t.id, true) },
        whole ? el('span', { className: 'tree-card-caption', textContent: 'Вся рубрика' }) : null,
        el('span', { className: 'tree-card-head' },
            el('span', { className: 'tree-card-name' }, ...treeHighlight(whole ? rubric.title : t.title, q)),
            nsfw ? el('span', { className: 'tree-badge is-18', textContent: '18+' }) : null,
            isCurrent ? el('span', { className: 'tree-air', textContent: '📍 В ЭФИРЕ' }) : null),
        el('span', { className: 'tree-card-about' }, ...treeHighlight(t.about, q)),
        tags.length ? el('span', { className: 'tree-card-tags' }, ...tags.slice(0, 3).map(tag => el('span', { className: 'tree-tag', textContent: `#${tag}` })),
            tags.length > 3 ? el('span', { className: 'tree-tag is-more', textContent: `+${tags.length - 3}` }) : null) : null,
        el('span', { className: t.own_sources ? 'tree-card-src' : 'tree-card-src is-muted', textContent: t.own_sources ? `📡 ${t.own_sources} своих` : 'источники рубрики',
            title: t.own_sources ? 'Свои источники темы' : 'Своих нет: берутся источники общей темы' }));
}

/** Карточки: при поиске — совпадения по всем рубрикам с заголовками, без поиска — выбранная рубрика целиком. Возвращает число карточек. */
function renderCards(model, q, rubricId) {
    const grid = (items) => el('div', { className: 'tree-grid' }, ...items.map(t => treeCard(t, model, q)));
    if (q) {
        const blocks = model.rubrics.map(r => [r, [r, ...(model.children.get(r.id) || [])].filter(t => treeMatches(t, q))])
            .filter(([, hits]) => hits.length)
            .map(([r, hits]) => el('section', { className: 'tree-block-cards' }, el('h3', { className: 'tree-cards-group', textContent: `${r.title} · найдено ${hits.length}` }), grid(hits)));
        $('tree-cards').replaceChildren(...blocks);
        return blocks.length;
    }
    const rubric = model.ids.get(rubricId);
    if (!rubric) { $('tree-cards').replaceChildren(); return 0; }
    const kids = model.children.get(rubric.id) || [];
    $('tree-cards').replaceChildren(
        el('div', { className: 'tree-cards-head' }, el('h2', { className: 'tree-rubric-title', textContent: rubric.title }), rubric.about ? el('p', { className: 'tree-rubric-about', textContent: rubric.about }) : null),
        grid([rubric, ...kids]));
    return kids.length + 1;
}

function renderTree() {
    const q = treeNorm(tree.query);
    const model = treeModel();
    if (!model.ids.has(tree.rubric)) tree.rubric = model.rubrics[0]?.id ?? null;
    const current = model.ids.get(tree.current);
    const liveRubric = current ? model.rubricOf(current).id : null;
    renderRubrics(model, q, tree.rubric, liveRubric);
    const cardCount = renderCards(model, q, tree.rubric);
    $('tree-current-name').textContent = current?.title || '—';
    if (!tree.topics.length) setMessage('tree-status', 'Тем пока нет: введите первую тему в поле выше.');
    else if (q && !cardCount) setMessage('tree-status', `Ничего не найдено. Enter — перейти к теме или создать «${tree.query.trim()}».`);
    else setMessage('tree-status', '');
}

/** Строка паспорта: подпись и значение, пустые строки не выводим. */
const treeRow = (label, value) => value ? el('div', { className: 'tree-row' }, el('dt', { textContent: label }), el('dd', {}, value)) : null;

function renderDetail() {
    const close = el('button', { type: 'button', className: 'tree-sheet-close', textContent: '✕', title: 'Закрыть', 'aria-label': 'Закрыть паспорт темы', onclick: closeTreeSheet });
    const t = tree.topics.find(x => x.id === tree.selected);
    if (!t) return $('tree-detail').replaceChildren(close, el('p', { className: 'tree-detail-empty', textContent: 'Выберите тему: здесь её описание, хэштеги и действия.' }));
    const model = treeModel();
    const rubric = model.rubricOf(t);
    const isCurrent = tree.current === t.id;
    const children = model.children.get(t.id) || [];
    const note = el('span', { className: 'tree-copied', role: 'status' });
    const sources = t.uses_parent_sources ? `${t.own_sources || 0} своих. Пока берутся источники общей темы «${t.parent.title}».`
        : t.own_sources ? `${t.own_sources} своих — по ним идёт сбор.` : 'Своих пока нет: автоподбор ищет их, как только тема становится текущей.';
    const profile = t.profile ? `составлен ${t.profile.at ? new Date(t.profile.at * 1000).toLocaleString('ru-RU') : 'недавно'}, ${t.profile.smart ? 'умной моделью' : 'быстрой моделью'}` : 'ещё не составлен: напишется при следующем запуске сборщика.';
    const action = (className, textContent, onclick, title = '') => el('button', { className, type: 'button', textContent, onclick, title });
    const copy = tag => { copyTreeText(`#${tag}`).then(() => note.textContent = `Скопировано: #${tag}`, () => note.textContent = 'Не удалось скопировать'); clearTimeout(note.timer); note.timer = setTimeout(() => { note.textContent = ''; }, 1500); };
    $('tree-detail').replaceChildren(...[close,
        el('nav', { className: 'tree-crumbs', 'aria-label': 'Путь к теме' }, t.parent
            ? el('span', {}, el('button', { type: 'button', className: 'tree-crumb', textContent: t.parent.title, onclick: () => pickRubric(t.parent.id) }), ' › ', t.title)
            : el('span', { textContent: 'Рубрика' })),
        el('h3', { className: 'tree-detail-title', textContent: t.title }),
        el('div', { className: 'tree-badges' },
            el('span', { className: 'tree-badge', textContent: t.scope === 'broad' ? 'общая тема' : 'частная тема' }),
            t.nsfw || rubric.nsfw ? el('span', { className: 'tree-badge is-18', textContent: '18+' }) : null,
            isCurrent ? el('span', { className: 'tree-badge is-current', textContent: '📍 сейчас выбрана' }) : null),
        isCurrent ? el('p', { className: 'tree-detail-empty', textContent: 'Эта тема сейчас в эфире: автоподбор ищет по ней.' })
            : el('button', { className: 'primary-button tree-primary', type: 'button', textContent: 'Сделать текущей', onclick: () => choose(t.title) }),
        t.about ? el('p', { className: 'tree-about', textContent: t.about }) : null,
        hashtagBlock(t, copy, note),
        t.aliases?.length ? el('div', { className: 'tree-block' }, el('h4', { className: 'tree-block-title', textContent: 'Другие названия' }),
            el('div', { className: 'tree-tags' }, ...t.aliases.map(a => el('span', { className: 'tree-alias', textContent: a })))) : null,
        children.length ? el('div', { className: 'tree-block' }, el('h4', { className: 'tree-block-title', textContent: 'Частные темы' }),
            el('div', { className: 'tree-tags' }, ...children.map(c => el('button', { type: 'button', className: 'tree-alias is-button', textContent: c.title, onclick: () => selectTopic(c.id) })))) : null,
        el('p', { className: 'tree-sources', textContent: sources }),
        el('details', { className: 'tree-ai' }, el('summary', { textContent: 'Как ИИ отбирает' }),
            el('dl', { className: 'tree-facts' }, treeRow('Подходит', t.criteria), treeRow('Не подходит', t.exclude), treeRow('Вопрос к картинке', t.question), treeRow('Профиль', profile))),
        el('div', { className: 'tree-actions' },
            action('secondary-button tree-small', 'Сменить общую тему', () => { tree.parentOpen = !tree.parentOpen; $('tree-parent-editor').hidden = !tree.parentOpen; if (tree.parentOpen) $('tree-parent-editor').querySelector('select').focus(); }),
            action('secondary-button tree-small', 'Пересоставить профиль', async () => {
                try { await api('topic_reprofile', { body: { id: t.id } }); log(`Профиль темы «${t.title}» пересоставится при следующем запуске сборщика.`); await loadTree(); }
                catch (error) { setMessage('tree-status', `Профиль не сброшен: ${error.message}`); }
            }, 'Критерии и план поиска напишутся заново')),
        parentEditor(t),
        el('details', { className: 'tree-debug', ontoggle: e => { if (e.currentTarget.open) loadTopicRecord(t.id); } },
            el('summary', { textContent: 'Для отладки: полная запись из базы' }),
            el('div', { className: 'tree-record' }, el('p', { className: 'tree-detail-empty', textContent: 'Раскройте, чтобы загрузить запись.' })))].filter(Boolean));
}

/** Хэштеги темы: рубрика (одна, её ставим в посты) и имена; клик копирует. «Изменить» — своя рубрика вместо словарной. */
function hashtagBlock(t, copy, note) {
    const h = t.hashtags || {};
    const edit = tree.tagsOpen === t.id;
    const save = async rubric => {
        try {
            await api('topic_rubric', { body: { id: t.id, rubric } });
            tree.tagsOpen = null;
            await loadTree();
        } catch (error) { setMessage('tree-status', `Рубрика не сохранена: ${error.message}`); }
    };
    const chip = (tag, extra = '') => el('button', { type: 'button', className: `tree-tag is-button ${extra}`.trim(), textContent: `#${tag}`, title: `Скопировать #${tag}`, onclick: () => copy(tag) });
    const toggle = h.rubric_default ? el('button', { type: 'button', className: 'tree-link', textContent: edit ? 'Отмена' : 'Изменить', onclick: () => { tree.tagsOpen = edit ? null : t.id; renderDetail(); } }) : null;
    const title = el('h4', { className: 'tree-block-title' }, h.rubric ? 'Хэштеги · рубрика ставится в посты' : 'Хэштегов нет: рубрика есть у частных тем', ' ', toggle);
    const synonyms = h.synonyms?.length ? el('details', { className: 'tree-tag-synonyms' }, el('summary', { textContent: `Узнаём ещё по ${h.synonyms.length} похожим тегам` }),
        el('p', { textContent: h.synonyms.map(x => `#${x}`).join(' ') })) : null;
    if (!edit) return el('div', { className: 'tree-block' }, title,
        h.rubric ? el('div', { className: 'tree-tags' }, chip(h.rubric, 'is-rubric'), ...(h.entities || []).map(tag => chip(tag))) : null,
        h.rubric && h.rubric !== h.rubric_default ? el('p', { className: 'tree-detail-empty', textContent: `Ваша рубрика. В словаре — #${h.rubric_default}.` }) : null, synonyms, note);
    const input = el('input', { className: 'tree-tag-input', type: 'text', maxLength: 17, value: h.rubric || '', placeholder: 'рубрика одним словом', autocomplete: 'off', ariaLabel: 'Своя рубрика' });
    const submit = () => { input.disabled = true; save(input.value.replace(/^#+/, '').trim()); };
    input.onkeydown = e => { if (e.key === 'Enter') { e.preventDefault(); submit(); } };
    return el('div', { className: 'tree-block' }, title,
        el('div', { className: 'tree-tag-add' }, input, el('button', { type: 'button', className: 'secondary-button tree-small', textContent: 'Сохранить', onclick: submit }),
            h.rubric !== h.rubric_default ? el('button', { type: 'button', className: 'secondary-button tree-small', textContent: 'Вернуть из словаря', onclick: () => save('') }) : null));
}

async function copyTreeText(text) {
    if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(text);
    const area = el('textarea', { value: text });
    document.body.append(area);
    area.select();
    const copied = document.execCommand('copy');
    area.remove();
    if (!copied) throw new Error('буфер недоступен');
}

const TOPIC_LABELS = { id: 'ID', title: 'Название', scope: 'Тип', parent: 'Общая тема', aliases: 'Ещё называют', about: 'Описание', classified: 'Классифицирована', smart: 'Классифицирована ИИ', manual: 'Создана вручную', created_at: 'Создана', hashtags: 'Хэштеги' };
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
    box()?.replaceChildren(el('p', { className: 'tree-detail-empty', textContent: 'Загружаю полную запись…' }));
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
    $('tree-layout').classList.toggle('is-busy', busy);
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
        tree.query = '';
        tree.current = tree.selected = data.topic_info?.id ?? null;
        await loadTree({ reveal: true });
        await refreshTopicLabel();
        await loadCurrentItem();
    } catch (error) { setMessage('tree-status', `Тема не сохранена: ${error.message}`); }
    finally { setTreeBusy(false); }
}

/** reveal: открыли вкладку или сменили тему — показываем текущую тему и её рубрику; иначе сохраняем выбор. */
async function loadTree({ reveal = false } = {}) {
    if (DEBUG_MODE) return setMessage('tree-status', 'В демо-режиме дерево тем не показывается.');
    try {
        const data = await api('topics');
        Object.assign(tree, { topics: data.topics || [], current: data.current ?? null });
        if (reveal) {
            const model = treeModel();
            const current = model.ids.get(tree.current);
            tree.selected = tree.current;
            tree.rubric = current ? model.rubricOf(current).id : readRubric();
        } else if (!tree.topics.some(t => t.id === tree.selected)) tree.selected = tree.current;
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
        renderTags($('test-tags'), d.tags_info);
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
    $('tree-refresh').addEventListener('click', () => loadTree());
    if (DEBUG_MODE) { hide('tree-form'); hide('tree-refresh'); }
    const search = $('tree-input');
    search.addEventListener('input', () => { tree.query = search.value; renderTree(); });   // фильтр на лету: поле и фокус не трогаем
    search.addEventListener('keydown', e => { if (e.key === 'Escape' && search.value) { e.preventDefault(); search.value = ''; tree.query = ''; renderTree(); } });
    $('tree-backdrop').addEventListener('click', closeTreeSheet);
    document.addEventListener('keydown', e => { if (e.key === 'Escape' && tree.sheetOpen) closeTreeSheet(); });
    new ResizeObserver(([entry]) => { const w = entry.contentRect.width; setTreeWidth(w >= 1000 ? 'wide' : w >= 700 ? 'mid' : 'narrow'); }).observe($('tree-tab'));
    onTabOpen('tree-tab', () => loadTree({ reveal: true }));
    // Тему сменили в шапке — открытые «Предложка» и «Дерево» перерисовываются под неё
    onTopicChange(() => { if (isTabActive('proposals-tab')) loadProposals(); if (isTabActive('tree-tab')) loadTree({ reveal: true }); });

    $('image-file').addEventListener('change', e => { if (e.target.files[0]) $('image-url').value = ''; setPreview(e.target.files[0] || null); hide('test-result'); });
    $('image-url').addEventListener('input', () => { if ($('image-url').value) { $('image-file').value = ''; setPreview(null); } });
    $('analyze-form').addEventListener('submit', analyze);
}
