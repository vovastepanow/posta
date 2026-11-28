// Источники: автопилот, каталог, добавление и исключение выбранных источников.
import { $, ask, COUNT, DEBUG_MODE, api, chip, el, hide, isTabActive, log, onParserStateChange, onTabOpen, onTopicChange, currentTopic } from './core.js';

import { initRegistry, loadRegistry } from './registry.js';
import { initializeSourceTools, loadSourceTools } from './source-tools.js';
import { initializeAutopilot, loadAutopilot } from './source-autopilot.js';

const topicSources = { sources: [], topic: '', filter: 'all', controller: null, generation: 0 };

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
    drainTopicTitles();
}
function drainTopicTitles() {
    while (topicTitles.active < 2 && topicTitles.queue.length) {
        const job = topicTitles.queue.shift();
        if (job.generation !== topicTitles.generation || !job.card.isConnected) continue;
        topicTitles.active++;
        let timer;
        const controller = new AbortController();
        timer = setTimeout(() => controller.abort(), 35000);
        api('rss_source_title', { body: { source_id: job.source.source_id }, signal: controller.signal }).then(data => {
            if (job.generation !== topicTitles.generation || !job.card.isConnected || !data.name) return;
            job.source.name = data.name;
            job.source.needs_title = false;
            const node = job.card.querySelector('.source-head-text strong');
            if (node) { node.textContent = data.name; node.title = data.name; }
        }).catch(() => {}).finally(() => { clearTimeout(timer); topicTitles.active--; drainTopicTitles(); });
    }
}

function topicSourceCard(source, topic) {
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
    const action = (label, run, extra = {}) => el('button', {
        className: 'proposal-action', type: 'button', textContent: label, ...extra,
        onclick: async event => {
            const button = event.currentTarget;
            if (button.disabled || topicSources.topic !== topic || !topicSources.sources.includes(source)) return;
            button.disabled = true;
            try { await run(); }
            catch (error) { log(`Не удалось изменить источник: ${error.message}`); }
            finally { button.disabled = false; }
        }
    });
    const mutate = async (endpoint, body) => {
        if (topicSources.topic !== topic || !topicSources.sources.includes(source)) throw new Error('Тема изменилась. Обновите список источников.');
        await api(endpoint, { body: { ...body, expected_topic: topic } });
        if (topicSources.topic === topic) await loadTopicSources();
    };
    if (!DEBUG_MODE && ['RSS', 'Telegram', 'ВКонтакте', 'Одноклассники', 'TikTok'].includes(source.platform)) {
        actions.append(action(source.pinned ? 'Открепить' : 'Закрепить для меня',
            () => mutate('source_personal_pin', { source_id: source.source_id, pinned: !source.pinned })));
    }
    if (source.muted) {
        card.classList.add('is-frozen');
        card.querySelector('.source-status')?.replaceChildren(document.createTextNode('выключен по вашим оценкам'));
    }
    if (source.muted || !DEBUG_MODE) {
        actions.append(action(source.muted ? 'Включить' : 'Исключить для меня',
            () => mutate('source_mute', { source_id: source.source_id, on: Boolean(source.muted) })));
    }
    if (!DEBUG_MODE && source.own && !source.autopilot_managed) {
        actions.append(action('✕ Убрать', async () => {
            if (!(await ask('Посты из него перестанут попадать в подбор по этой теме.', { title: `Убрать «${source.name}» из темы «${topic}»?`, ok: 'Убрать', danger: true }))) return;
            card.classList.add('is-removing');
            try { await mutate('remove_topic_source', { platform: source.platform, id: source.id, name: source.name }); }
            finally { card.classList.remove('is-removing'); }
        }, { className: 'proposal-action proposal-hide', title: `Убрать из темы «${topic}»` }));
    }
    card.append(actions);
    if (source.needs_title) {
        card._titleSource = source;
        if (topicTitles.observer) topicTitles.observer.observe(card);
        else { const generation = topicTitles.generation; requestAnimationFrame(() => queueTopicTitle(card, source, generation)); }
    }
    return card;
}

function renderTopicSources() {
    const { sources, topic, filter } = topicSources;
    const visible = sources.filter(s => filter === 'all' || s.platform === filter);
    $('topic-sources-title').textContent = topic ? `Источники темы «${topic}»` : 'Источники темы';
    $('topic-sources-count').textContent = sources.length ? `${visible.length} из ${sources.length}` : '';
    $('topic-sources-status').textContent = visible.length ? '' : sources.length ? 'На этой площадке источников темы нет.' : 'У темы пока нет источников. Автопилот добавит подходящие источники автоматически.';
    const generation = ++topicTitles.generation;
    topicTitles.observer?.disconnect();
    topicTitles.queue = [];
    topicTitles.observer = 'IntersectionObserver' in window ? new IntersectionObserver(entries => {
        for (const entry of entries) if (entry.isIntersecting && entry.target._titleSource) {
            topicTitles.observer?.unobserve(entry.target);
            queueTopicTitle(entry.target, entry.target._titleSource, generation);
        }
    }, { rootMargin: '250px' }) : null;
    $('topic-sources-grid').replaceChildren(...visible.map(source => topicSourceCard(source, topic)));
    document.querySelectorAll('[data-topic-source-filter]').forEach(b => b.classList.toggle('active', b.dataset.topicSourceFilter === filter));
}

async function loadTopicSources() {
    if (!$('topic-sources-section').open || !$('source-tools-dialog').open) return;
    topicSources.controller?.abort();
    const controller = new AbortController();
    topicSources.controller = controller;
    const generation = ++topicSources.generation;
    try {
        const data = await api('topic_sources', { signal: controller.signal });
        if (generation !== topicSources.generation) return;
        Object.assign(topicSources, { sources: data.sources || [], topic: data.topic || '' });
        renderTopicSources();
    } catch (error) {
        if (generation === topicSources.generation && !controller.signal.aborted) $('topic-sources-status').textContent = `Не удалось загрузить источники: ${error.message}`;
    } finally { if (topicSources.controller === controller) topicSources.controller = null; }
}

function invalidateTopicSources() {
    topicSources.controller?.abort();
    ++topicSources.generation;
    Object.assign(topicSources, { topic: '', sources: [] });
    closeSourcePreview();
    renderTopicSources();
    if (isTabActive('source-tab')) loadTopicSources();
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
        const topic = currentTopic().topic;
        const result = await api('add_topic_source', { body: { link, ...(topic ? { expected_topic: topic } : {}) } });
        status.textContent = result.added ? `Добавлен: ${result.name} (свежих постов с картинками: ${result.posts})` : `Уже в теме: ${result.name}`;
        input.value = '';
        await loadTopicSources();
    } catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; }
}

export function initializeSourceTab() {
    const toolsDialog = $('source-tools-dialog');
    const openTools = () => {
        if (toolsDialog.open) return;
        toolsDialog.showModal();
        loadSourceTools();
        loadTopicSources();
        if ($('registry-section').open) loadRegistry();
    };
    $('source-tools-open').addEventListener('click', openTools);
    $('source-tools-close').addEventListener('click', () => toolsDialog.close());
    initializeSourceTools();
    $('source-add-form').addEventListener('submit', addSource);
    initializeAutopilot();
    document.querySelectorAll('[data-topic-source-filter]').forEach(b => b.addEventListener('click', () => { topicSources.filter = b.dataset.topicSourceFilter; renderTopicSources(); }));
    $('topic-sources-refresh').addEventListener('click', loadTopicSources);
    initRegistry();
    $('topic-sources-section').addEventListener('toggle', () => { if ($('topic-sources-section').open) loadTopicSources(); });
    $('registry-section').addEventListener('toggle', () => { if ($('registry-section').open) loadRegistry(); });
    onTabOpen('source-tab', () => {
        loadAutopilot(true);
        if (toolsDialog.open) {
            loadTopicSources();
            if ($('registry-section').open) loadRegistry();
            loadSourceTools();
        }
    });
    onTopicChange(invalidateTopicSources);
    onParserStateChange(running => {
        if (!running && isTabActive('source-tab')) loadTopicSources();
    });
}
