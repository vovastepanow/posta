// tabs.js — вкладки «Предложка» (лайки и «не знаю»), «Дерево» (выбор и смена темы) и «Тест ИИ» (оценка одной картинки).
import { $, DEBUG_MODE, NO_TEXT, VIDEO_ICON, api, el, hide, log, onTabOpen, safeSourcePostUrl, setMessage, show } from './core.js';
import { openViewerGallery } from './viewer.js';
import { TOPIC_CONFIRM, applyRocketCheck, loadCurrentItem, postItemToTelegram, recordGallery, refreshTopicLabel, saveTopic } from './content.js';

// ---- «Предложка» ----

const proposals = { items: [], filter: 'all', refreshed: false };
// Видео в карточках играют, только пока видны: иначе десятки роликов качаются разом
const videoObserver = new IntersectionObserver(entries => entries.forEach(({ target: v, isIntersecting }) => {
    if (!isIntersecting) return v.pause();
    if (!v.getAttribute('src')) v.src = v.dataset.src;
    v.play().catch(() => {});
}), { rootMargin: '100px' });

function mediaTile(slide, onOpen, extra) {
    const tile = el('button', { className: 'proposal-tile is-loading', type: 'button', title: 'Открыть', onclick: onOpen });
    const settled = () => tile.classList.remove('is-loading');
    const image = el('img', { src: slide.poster || slide.src || '', alt: '', loading: 'lazy', decoding: 'async', onload: settled });
    image.onerror = () => {
        const backup = slide.kind !== 'video' && slide.fallback;
        if (backup && image.getAttribute('src') !== backup) return void (image.src = backup);   // копия из кэша не открылась — исходная ссылка
        image.hidden = true;
        settled();
        tile.classList.add('is-broken');
    };
    tile.append(image);
    if (slide.kind === 'video') {
        const video = el('video', { poster: slide.poster || '', muted: true, loop: true, playsInline: true, preload: 'metadata' });
        Object.assign(video.dataset, { src: slide.src, fallback: slide.fallback || '' });
        video.addEventListener('playing', settled, { once: true });
        video.onerror = () => {
            if (video.dataset.fallback && video.getAttribute('src') !== video.dataset.fallback) { video.src = video.dataset.fallback; return void video.play().catch(() => {}); }
            videoObserver.unobserve(video);
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
    $('proposals-grid').querySelectorAll('video').forEach(v => videoObserver.unobserve(v));
    $('proposals-grid').replaceChildren(...visible.map(proposalCard));
    document.querySelectorAll('[data-proposal-filter]').forEach(b => b.classList.toggle('active', b.dataset.proposalFilter === proposals.filter));
}

async function loadProposals() {
    try {
        const status = $('proposals-status');
        status.textContent = 'Загружаю предложки...';
        status.style.opacity = '0.7';

        const start = Date.now();
        const data = await api('proposals');
        const elapsed = Date.now() - start;

        proposals.items = data.items || [];
        renderProposals();

        status.textContent = `${proposals.items.length} предложек${elapsed > 1000 ? ` (${(elapsed/1000).toFixed(1)}с)` : ''}${data.refreshing ? ' • обновляю...' : ''}`;
        status.style.opacity = '1';

        if (data.refreshing && !proposals.refreshed) {
            proposals.refreshed = true;
            setTimeout(loadProposals, 8000);
        }
    } catch (error) {
        $('proposals-status').textContent = `Не удалось загрузить предложку: ${error.message}`;
        $('proposals-status').style.opacity = '1';
    }
}

// ---- «Дерево»: общие темы — корни, частные — внутри; справа — панель выбранной темы ----

const tree = { topics: [], current: null, selected: null, busy: false, parentOpen: false };

function renderTree() {
    const ids = new Set(tree.topics.map(t => t.id));
    const children = new Map();
    const roots = [];
    for (const t of tree.topics) t.parent?.id && ids.has(t.parent.id) ? children.set(t.parent.id, [...(children.get(t.parent.id) || []), t]) : roots.push(t);
    const byTitle = (a, b) => a.title.localeCompare(b.title, 'ru');
    roots.sort((a, b) => (b.scope === 'broad') - (a.scope === 'broad') || byTitle(a, b));
    const nodeFor = t => {
        const kids = (children.get(t.id) || []).sort(byTitle);
        const label = el('span', { className: 'tree-label', title: t.about || t.title, onclick: () => { Object.assign(tree, { selected: t.id, parentOpen: false }); renderTree(); renderDetail(); } },
            el('span', { className: 'tree-icon', textContent: tree.current === t.id ? '📍' : t.scope === 'broad' ? '🗂' : '🔹' }), el('span', { className: 'tree-name', textContent: t.title }),
            el('span', { className: `tree-count${t.own_sources ? '' : ' is-empty'}`, textContent: String(t.own_sources || 0), title: 'Свои источники темы' }),
            t.uses_parent_sources ? el('span', { className: 'tree-note', textContent: 'берёт источники общей' }) : null);
        const item = el('li', { className: ['tree-node', `tree-${t.scope}`, tree.current === t.id && 'is-current', tree.selected === t.id && 'is-selected'].filter(Boolean).join(' ') });
        item.append(kids.length ? el('details', { className: 'tree-branch', open: t.scope === 'broad' || tree.selected === t.id }, el('summary', {}, label), el('ul', { className: 'tree-children' }, ...kids.map(nodeFor))) : label);
        return item;
    };
    $('tree-root').replaceChildren(el('ul', { className: 'tree' }, ...roots.map(nodeFor)));
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
        parentEditor(t));
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
