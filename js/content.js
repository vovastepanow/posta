// content.js — вкладка «Подбор»: тема и вторая тема, фильтры, карточка записи (карусель, видео, активность), голоса, жалобы,
// публикация в Telegram (панель 🚀 и редактор поста).
import { $, COUNT, DEBUG_MODE, NO_TEXT, api, apiUrl, bindMenu, chip, currentTopic, el, hide, log, onParserStateChange, onParserStatus, pluralRu, renderTags, safeSourcePostUrl, setCurrentTopic, setMessage, show, watchParser } from './core.js';
import { registerViewerGallery, registerViewerPoster, videoFields } from './viewer.js';
import { formatWhen, openPostEditor } from './editor.js';
import { openAccountSection } from './account.js';
import { itemCheck, videoProgress, watchVideo } from './video-pending.js';
import { articleBrief, articleCaption, cardKind, clampText } from './card.js';
import { fromRecord, recordSlides } from './post-adapters.js';
import { renderPostSheet } from './post-view.js';

const state = { item: null, slides: [], index: 0, poll: null, voting: false, preloaded: [], channels: [], nativeAccount: false };
const TOPIC_CONFIRM = 'Сменить тему поиска? Неоценённые записи по старой теме будут убраны из пула, и поиск начнётся заново. История оценок сохранится.';
export { TOPIC_CONFIRM };

/** Одна из областей вкладки: card | empty | error | searching. */
function showArea(area, message = '') {
    show('item-card-wrapper', area === 'card');
    show('item-empty', area === 'empty');
    setMessage('item-error', area === 'error' ? message : '');
    show('item-searching', area === 'searching');
    if (area === 'searching') $('search-progress-topic').textContent = topicWithQuery(currentTopic()) || 'контент';
    if (area === 'empty') $('item-empty-message').textContent = message || 'Пул контента пуст.';
    hide('item-loading');
}

/** Сколько осталось: оценку считает сервер по скорости разбора очереди; пока её нет — полоса бежит без процентов. */
function formatEta(seconds) {
    if (seconds == null) return 'считаем время…';
    if (seconds <= 0) return 'почти готово';
    if (seconds < 60) return 'осталось меньше минуты';
    const minutes = Math.round(seconds / 60);
    return `осталось ≈ ${minutes} ${pluralRu(minutes, 'минута', 'минуты', 'минут')}`;
}

/** Полоса поиска по ответу parser_status (опрос — общий, в core.js). */
function renderSearchProgress(data) {
    const p = data.running ? data.progress : null;
    const known = p?.share != null;
    $('item-searching').classList.toggle('is-indeterminate', !known);
    $('search-progress-fill').style.width = known ? `${Math.max(3, p.share * 100).toFixed(1)}%` : '';
    $('search-progress-percent').textContent = known ? `${Math.floor(p.share * 100)}%` : '';
    $('search-progress-eta').textContent = !data.running ? 'запускаем…' : p?.queued ? 'ждём очереди…' : formatEta(known ? p.eta : null);
    $('item-searching').title = data.running ? `${data.phase}${p?.added ? ` · найдено: ${p.added}` : ''}` : '';
}

// ---- Слайды: у каждого основной адрес (копия из кэша) и запасной (исходная ссылка) ----

/** Слайды записи: логика в post-adapters.js (общая с просмотрщиком постов). */
const itemSlides = item => recordSlides(item, NO_TEXT[0]);

/** Слайды записи как галерея просмотрщика. */
export function recordGallery(item, index = 0) {
    const sourceUrl = safeSourcePostUrl(item);
    return { index, onPost: item ? () => postItemToTelegram(item).catch(e => log(`Не удалось открыть редактор: ${e.message}`)) : null, postCheck: item?.post_check || null,
        items: itemSlides(item).filter(s => !s.textPost).map((s, i) => ({ kind: s.video ? 'video' : 'image', src: s.video || s.image, fallback: s.video ? s.fallbackVideo : s.fallbackImage, poster: s.image, sourceUrl, alt: 'Запись',
            ...(i === 0 ? videoFields(item, sourceUrl) : {}) })) };
}

// Старые функции больше не используются, но оставляем для совместимости

/** Текст поста без картинки: крупно, до 12 строк, «Показать полностью». */
export function textPostBlock(text) {
    return clampText(text, 12, 'text-post');   // общая обрезка с кнопкой «Показать полностью» (card.js)
}

const AI_OPEN_KEY = 'postaCardAiOpen';

/** «Почему ИИ выбрал»: свёрнут по умолчанию, состояние запоминается (localStorage в try/catch). */
function initializeCardAi() {
    const box = $('item-ai');
    try { box.open = localStorage.getItem(AI_OPEN_KEY) === '1'; } catch { /* приватный режим: по умолчанию свёрнут */ }
    box.addEventListener('toggle', () => { try { localStorage.setItem(AI_OPEN_KEY, box.open ? '1' : '0'); } catch { /* не сохранилось — не страшно */ } });
}

/** Запись подбора — тем же листом, что в просмотрщике постов (post-view.js), без ‹ ›. Текст — перевод или оригинал (state.shownText).
 *  Кнопки подбора (голоса, жалоба, 🚀) и блоки оценки стоят в карточке под листом, как раньше. */
function renderPostView(item) {
    renderPostSheet($('item-post'), fromRecord({ ...item, text: state.shownText ?? item.text }), {
        zoom: { onPost: item ? () => postItemToTelegram(item).catch(e => log(`Не удалось открыть редактор: ${e.message}`)) : null, postCheck: item.post_check || null },
        watchPending: false, link: false, stats: false,
    });
}

/** Активность у источника: просмотры и реакции в Telegram, ответы и размер треда на Дваче. */
function renderEngagement(e) {
    const trend = (value, base, what) => {
        if (!(base > 0)) return null;
        const title = `${what}: ${COUNT.format(value)}, обычно ~${COUNT.format(Math.round(base))}`;
        return value / base >= 1.5 ? el('span', { className: 'eng-chip eng-up', textContent: '↑ выше обычного', title }) : value / base <= 0.6 ? el('span', { className: 'eng-chip eng-down', textContent: '↓ ниже обычного', title }) : null;
    };
    const chips = [];
    if (e?.source === 'telegram' && Number.isFinite(e.views)) {
        const reactions = Object.entries(e.reactions || {}).filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1]);
        chips.push(chip('просмотры', COUNT.format(e.views), `Обычно у постов этого канала: ~${COUNT.format(Math.round(e.neighbors_median_views ?? 0))}`), trend(e.views, e.neighbors_median_views, 'Просмотры'),
            ...reactions.slice(0, 8).map(([emoji, n]) => el('span', { className: 'eng-chip eng-reaction', title: `Реакций ${emoji}: ${n}` }, el('span', { className: 'eng-emoji', textContent: emoji }), el('b', { textContent: COUNT.format(n) }))),
            reactions.length > 8 ? el('span', { className: 'eng-chip', textContent: `+${reactions.length - 8}` }) : null,
            reactions.length ? null : el('span', { className: 'eng-chip', textContent: 'реакций нет' }));
    } else if (e?.source === 'osnova') {   // статья vc.ru / dtf.ru: комментарии и реакции со страницы
        chips.push(chip('комментарии', COUNT.format(e.comments ?? 0), 'Комментарии к статье на сайте'), chip('реакции', COUNT.format(e.likes ?? 0), 'Реакции на статью на сайте'));
    } else if (e?.source === 'dvach' && Number.isFinite(e.thread_posts)) {
        const span = h => (h < 48 ? `${Math.max(1, Math.round(h))} ч` : `${Math.round(h / 24)} дн`);
        chips.push(e.is_op ? null : chip('ответы', COUNT.format(e.replies ?? 0), 'Сколько постов в треде ответили на этот пост (>>номер)'),   // у ОП-поста ответов почти как постов в треде
            chip('тред', `${COUNT.format(e.thread_posts)} ${pluralRu(e.thread_posts, 'пост', 'поста', 'постов')}${e.thread_hours ? ` за ${span(e.thread_hours)}` : ''}`),
            trend(e.thread_posts, e.board_median_thread_posts, 'Постов в треде'));
    }
    const shown = chips.filter(Boolean);
    $('item-engagement').replaceChildren(...shown);
    $('item-engagement').hidden = !shown.length;
}

function formatPostedAt(seconds) {
    if (!seconds) return '';
    const minutes = Math.max(0, Math.round((Date.now() / 1000 - seconds) / 60));
    if (minutes < 60) return `${minutes || 1} мин назад`;
    if (minutes < 2880) return `${Math.round(minutes / 60)} ч назад`;
    return minutes < 20160 ? `${Math.round(minutes / 1440)} дн назад` : new Date(seconds * 1000).toLocaleDateString('ru-RU', { day: 'numeric', month: 'long', year: 'numeric' });
}

/** Запись без ролика, который ещё качается: оверлей «Видео загружается…», карточка обновляется на месте; не вышло — следующая запись. */
function watchItemVideo(item) {
    state.videoWait?.stop();
    state.videoWait = null;
    if (!item.video_via || item.video || item.video_unavailable) return;
    // Полоса — на плитке ролика в карточке (post-view помечает её .is-video-pending); плитку рисуют позже, поэтому ищем при каждом опросе
    let bar = null;
    const tile = () => document.querySelector('#item-post .is-video-pending');
    const stop = () => { bar?.stop(); bar = null; };
    state.videoWait = watchVideo(itemCheck(item.image), {
        onProgress: p => {
            if (bar && !bar.host?.isConnected) stop();
            const host = tile();
            if (!bar && host) bar = Object.assign(videoProgress(host), { host });
            bar?.update(p);
        },
        onReady: r => { stop(); if (state.item?.image === item.image) renderCard({ ...state.item, video: r.video_local || r.video, video_local: r.video_local || '', display_video: r.video, video_unavailable: false }); },
        onFail: () => { stop(); if (state.item?.image === item.image) loadCurrentItem(); }
    });
}

function renderCard(item, archiveInfo = null) {
    Object.assign(state, { item, index: 0, showOriginal: false });
    watchItemVideo(item);
    applyRocketCheck($('item-rocket'), item.post_check);
    state.preloaded = (item.preload || []).map(src => Object.assign(new Image(), { decoding: 'async', src }));   // следующие записи грузятся заранее
    const [relevance, rating] = [Number(item.relevance ?? 0), item.rating == null ? null : Number(item.rating)];
    $('item-rating').textContent = rating === null ? '' : `${rating}%`;
    $('item-rating').hidden = rating === null;
    $('item-relevance').textContent = `По теме: ${relevance}%`;
    // Свой контент: подбора и оценки не было — жалобы на «ошибку подбора» и проценты не показываем
    $('complaint-button').hidden = Boolean(item.own);
    if (item.own) { $('item-rating').hidden = true; $('item-relevance').textContent = 'Свой контент'; }
    renderTags($('item-tags'), item.tags_info, t => { if (confirm(`Искать по теме «${t.title}»? Тема подбора изменится.`)) saveTopic(t.title, false, 'topic-notice').then(() => refreshTopicLabel()).catch(error => log(`Не удалось сменить тему: ${error.message}`)); });
    $('item-source').textContent = `Источник: ${item.source || 'неизвестно'}${item.found_by ? ` · ${item.found_by}` : ''}`;
    const posted = formatPostedAt(item.posted_at);
    setMessage('item-date', posted && `Опубликовано: ${posted}`);
    // «Свежак!»: пост опубликован меньше двух часов назад
    document.querySelector('#item-card-wrapper .card')?.classList.toggle('is-fresh', Number(item.posted_at) > 0 && Date.now() / 1000 - Number(item.posted_at) < 2 * 3600);
    renderEngagement(item.engagement);
    const url = safeSourcePostUrl(item);
    if (url) $('item-source-link').href = url; else $('item-source-link').removeAttribute('href');
    $('item-source-link').hidden = !url;
    // Текст поста под медиа; у текстового поста без картинки он уже показан вместо медиа
    state.showOriginal = false;   // новая запись — сначала перевод (если галочка включена)
    applyTextMode(item);
    $('item-why-text').textContent = item.why_relevant || 'Нет объяснения от ИИ';
    $('item-desc').replaceChildren(el('strong', { textContent: 'Описание ИИ:' }), ` ${item.description || 'Анализ отсутствует'}`);

    // Если это архивный пост - добавляем CSS класс для анимаций (весь контент генерируется в CSS)
    const cardEl = $('item-card-wrapper').querySelector('.card');
    if (archiveInfo?.archive) {
        const runtime = archiveInfo.parser_runtime || 0;
        const runtimeText = runtime < 60 ? `${runtime}с` : `${Math.floor(runtime / 60)}м ${runtime % 60}с`;
        cardEl.classList.add('is-archive');
        setMessage('item-error', '');
        log(`🔥 Архивный пост (парсер работает ${runtimeText})`);
    } else {
        cardEl.classList.remove('is-archive');
        setMessage('item-error', '');
    }

    showArea('card');
    const source = item.source || 'источник не указан';
    log(`Показана запись: ${url ? `[${source}](${url})` : source}, по теме ${relevance}%${rating === null ? '' : `, прогноз ${rating}%`}.`);
}

/** Первая запись пула; пока парсер ищет — повтор каждые 3 с. */
export async function loadCurrentItem() {
    clearTimeout(state.poll);
    try {
        const data = await api('get_current_item');
        if (data.status === 'loading') {
            state.item = null;
            showArea('searching');
            // Показываем время работы парсера если оно известно
            if (data.parser_runtime > 0) {
                const runtimeText = data.parser_runtime < 60 ? `${Math.floor(data.parser_runtime)}с` : `${Math.floor(data.parser_runtime / 60)}м`;
                log(`⏱️ Парсер работает ${runtimeText}...`);
            }
            watchParser();
            state.poll = setTimeout(loadCurrentItem, 3000);
            return;
        }
        // Без картинки запись всё же показываем, если это статья (текст и заголовок) или текстовый пост
        if (data.status === 'empty' || (!data.image && !data.article && !data.text_post)) {
            Object.assign(state, { item: null, slides: [] });
            showArea('empty', data.message);
            log(data.message || 'Пул контента пуст.');
            return;
        }
        // Архивный пост (парсер медленный) или обычный
        renderCard(data, data.archive ? data : null);
    } catch (error) {
        log(`Ошибка загрузки записи: ${error.message}`);
        showArea('error', `Не удалось загрузить запись: ${error.message}`);
    }
}

async function searchForContent() {
    $('item-search-button').disabled = true;
    clearTimeout(state.poll);
    showArea('searching');
    try { await api('refresh_content', { body: null }); watchParser(); await loadCurrentItem(); }
    catch (error) { showArea('error', `Не удалось запустить поиск: ${error.message}`); }
    finally { $('item-search-button').disabled = false; }
}

/** Голос: на сервер уходит только адрес картинки, данные записи сервер берёт из пула. */
async function vote(type, details = {}) {
    if (!state.item || state.voting) return;
    state.voting = true;
    $('content-tab').classList.add('is-busy');
    try {
        await api('vote', { body: { vote: type, image: state.item.item_key || state.item.image, ...details } });
        if (type === 'wrong' && details.reason === 'match_error') {
            log('🚩 Жалоба учтена: ИИ увидит эту запись как пример «не по теме», источник теряет рейтинг, а каждые 5 жалоб уточняют профиль темы.');
        }
        await loadCurrentItem();
    }
    catch (error) { log(`Голос не сохранён: ${error.message}`); setMessage('item-error', `Не удалось сохранить оценку: ${error.message}`); }
    finally { state.voting = false; $('content-tab').classList.remove('is-busy'); }
}

// ---- Тема, вторая тема, фильтры ----

const FILTERS = { fresh_only: 'filter-fresh', active_sources_only: 'filter-active', video_only: 'filter-video', media_only: 'filter-media', media_no_text: 'filter-media-no-text', auto_translate: 'filter-translate' };
let topicFormat = null;   // формат текущей темы (topic_info): нужен подсказке про «Текст поста обязателен»
let mediaOnlyBeforeNoText = false;   // «Только с медиа» до того, как включили «Только медиа»: вернём при снятии

/** «Только медиа» включает «Только с медиа»: та отмечена и неактивна. Подсказка — если формат темы требует текст. */
function syncMediaFilters() {
    const [only, noText] = [$('filter-media'), $('filter-media-no-text')];
    if (noText.checked) only.checked = true;
    only.disabled = noText.checked;
    $('media-format-hint').hidden = !(noText.checked && topicFormat?.text === 'required');
}

/** Галочка «Только медиа»: запоминаем «Только с медиа» при включении и возвращаем при снятии. */
function onMediaNoTextToggle() {
    const [only, noText] = [$('filter-media'), $('filter-media-no-text')];
    if (noText.checked) mediaOnlyBeforeNoText = only.checked;
    else only.checked = mediaOnlyBeforeNoText;
    syncMediaFilters();
    saveFilters();
}

/** Строка «Ищем: …», её правка и чипы «Уточнить» для текущей темы (TOPICS_SEARCH_PROMPT.md, части B.6 и D.4). */
function modeSentence(f) {
    const what = f.media === 'required' ? (f.text === 'required' ? 'посты с картинкой или видео и текстом' : 'посты с картинкой или видео')
        : (f.text === 'required' || f.judge === 'text' ? 'текстовые посты, картинка не обязательна' : 'посты с картинкой или видео, картинка не обязательна');
    const judge = { both: 'судим по картинке и подписи', image: 'судим по картинке', text: 'судим по тексту' }[f.judge] || 'судим по картинке и подписи';
    return `Ищем: ${what} · ${judge}`;
}

async function renderTopicInfo() {
    const info = await api('topic_info');
    const box = $('topic-mode-box');
    box.hidden = false;
    $('topic-mode').textContent = modeSentence(info.format) + (info.overridden ? ' (изменено вами)' : '');
    $('mode-media').checked = info.format.media === 'required';
    $('mode-text').checked = info.format.text === 'required';
    $('mode-judge').value = info.format.judge;
    topicFormat = info.format;
    syncMediaFilters();
    const chips = $('topic-children');
    const parts = [];
    if (info.parent) parts.push(el('button', { className: 'topic-link', type: 'button', textContent: `← вся тема «${info.parent.title}»`, onclick: () => saveTopic(info.parent.title, false, 'topic-notice').then(() => refreshTopicLabel()) }));
    if (info.children.length) {
        parts.push(el('span', { className: 'topic-children-label', textContent: 'Уточнить:' }));
        parts.push(...info.children.map(c => el('button', { className: 'tag-chip is-topic-chip', type: 'button', textContent: c.title,
            onclick: () => saveTopic(c.title, false, 'topic-notice').then(() => refreshTopicLabel()) })));
        if (info.children_total > info.children.length) parts.push(el('span', { className: 'topic-children-more', textContent: `ещё ${info.children_total - info.children.length}…` }));
    }
    chips.replaceChildren(...parts);
    chips.hidden = !parts.length;
}

export async function refreshTopicLabel() {
    try {
        const data = await api('settings');
        const topic = data.topic || '';
        $('topic-input').value = $('topic-input').dataset.saved = topic;
        $('topic2-input').value = $('topic2-input').dataset.saved = data.topic2 || '';
        $('query-input').value = $('query-input').dataset.saved = data.query || '';
        setCurrentTopic(topic, data.topic2 || '', data.query || '');
        renderTopicInfo().catch(() => { $('topic-mode-box').hidden = true; });
        for (const [key, id] of Object.entries(FILTERS)) $(id).checked = ['active_sources_only', 'auto_translate'].includes(key) ? data.filters?.[key] !== false : Boolean(data.filters?.[key]);
        syncMediaFilters();
        $('filter-adult').checked = Boolean(data.adult_allowed);

        // Тема в заголовке: одна кнопка в стиле вкладки и выпадающий список всех тем
        try {
            const topics = (await api('topics')).topics || [];
            renderTopicSwitch(topics, topic);
            $('topic-options').replaceChildren(...topics.map(t => el('option', { value: t.title, label: t.path })));
        } catch { /* подсказок не будет */ }
    } catch (error) { log(`Не удалось загрузить тему поиска: ${error.message}`); }
}

let topicSwitch = null;

/** Меню создаётся один раз, при каждом обновлении меняются подпись кнопки и список. */
function renderTopicSwitch(topics, currentTopic) {
    if (!topicSwitch) {
        const label = el('span', { className: 'topic-switch-label' });
        const caret = el('span', { className: 'topic-switch-caret', textContent: '▾' });
        caret.setAttribute('aria-hidden', 'true');
        const button = el('button', { className: 'tab-btn topic-switch-button', type: 'button', title: 'Выбрать тему поиска' }, label, caret);
        button.setAttribute('aria-haspopup', 'menu');
        button.setAttribute('aria-expanded', 'false');
        const menu = el('div', { className: 'topic-switch-menu', role: 'menu', hidden: true });
        $('topic-tabs').replaceChildren(el('div', { className: 'topic-switch' }, button, menu));
        bindMenu(button, menu, item => submitTopic(item.dataset.topic));
        topicSwitch = { label, menu };
    }
    topicSwitch.label.textContent = currentTopic || 'Тема';
    topicSwitch.menu.replaceChildren(...topics.map(t => {
        const item = el('button', { type: 'button', role: 'menuitem', textContent: t.title, title: t.path || '', className: t.title === currentTopic ? 'is-current' : '' });
        item.dataset.topic = t.title;
        return item;
    }));
}

/** Сохраняет тему (общий код с вкладкой «Дерево»): показывает, во что превратился ввод, и даёт возразить. Возвращает ответ сервера. */
export async function saveTopic(topic, forceNew, notice) {
    const data = await api('save_topic', { body: { topic, force_new: forceNew } });
    if (data.matched && data.typed && data.topic.toLowerCase() !== data.typed.toLowerCase()) {
        $(`${notice}-text`).textContent = `«${data.typed}» — это тема «${data.topic}».`;
        $(`${notice.replace('notice', 'force-new')}`).dataset.typed = data.typed;
        show(notice);
    }
    log(`Тема поиска: «${data.topic}».`);
    if (data.created && data.topic_info?.parent) log(`Новая тема «${data.topic}» — частная тема внутри «${data.topic_info.parent.title}».`);
    if (data.discovery) log('У темы пока мало своих источников — сначала ищу новые (поиск в Telegram, VK, на Дваче). Ход поиска — в журнале.');
    watchParser();
    refreshTopicLabel();
    return data;
}

async function submitTopic(topic, forceNew = false) {
    const [buttonEl, input] = [$('topic-save'), $('topic-input')];
    buttonEl.disabled = input.disabled = true;
    hide('topic-notice');
    try {
        const data = await saveTopic(topic, forceNew, 'topic-notice');
        input.value = input.dataset.saved = data.topic;
        refreshTopicLabel();
        await loadCurrentItem();
    } catch (error) {
        setMessage('item-error', `Тема не сохранена: ${error.message}`);
    } finally { buttonEl.disabled = input.disabled = false; }
}

/** Тема с уточнением для заголовков: «Города · тюмень». */
export function topicWithQuery(t) { return t.query ? `${t.topic} · ${t.query}` : t.topic; }

/**
 * Поле «вторая тема» и «уточнение»: только для чтения, клик активирует, клик мимо или Enter сохраняет, Esc отменяет.
 * save(value) — запрос к серверу, он возвращает ответ с сохранённым значением; applied(data) — перерисовка; label — для сообщения об ошибке.
 */
function initializeInlineField(field, input, save, applied, label) {
    const deactivate = async (cancel = false) => {
        if (!field.classList.contains('is-active')) return;
        input.readOnly = true;
        field.classList.remove('is-active');
        const value = input.value = cancel ? input.dataset.saved : input.value.trim();
        if (value === input.dataset.saved) return;
        field.classList.add('is-busy');
        try {
            const data = await save(value);
            input.value = input.dataset.saved = data.value ?? '';
            applied(data);
            await loadCurrentItem();
        } catch (error) {
            input.value = input.dataset.saved;
            setMessage('item-error', `${label} не сохранена: ${error.message}`);
        } finally { field.classList.remove('is-busy'); }
    };
    field.addEventListener('click', () => {
        if (!input.readOnly) return;
        input.readOnly = false;
        field.classList.add('is-active');
        input.focus();
        input.setSelectionRange(input.value.length, input.value.length);
    });
    document.addEventListener('click', e => { if (!field.contains(e.target)) deactivate(); });
    input.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === 'Escape') { e.preventDefault(); deactivate(e.key === 'Escape'); } });
}

/** Вторая тема: контент ищется по обеим темам. */
function initializeSecondTopic() {
    initializeInlineField($('topic2-field'), $('topic2-input'),
        async value => { const data = await api('save_second_topic', { body: { topic2: value } }); return { value: data.topic2, data }; },
        ({ data }) => {
            setCurrentTopic(currentTopic().topic, data.topic2 || '', currentTopic().query);
            log(data.topic2 ? `Вторая тема: «${data.topic2}» — контент ищется по обеим.` : 'Вторая тема убрана.');
            watchParser();
        }, 'Вторая тема');
}

/** Уточнение («Города» + «тюмень»): подбор только про это. */
function initializeQueryField() {
    initializeInlineField($('query-field'), $('query-input'),
        async value => { const data = await api('save_query', { body: { query: value } }); return { value: data.query, data }; },
        ({ data }) => {
            setCurrentTopic(currentTopic().topic, currentTopic().topic2, data.query || '');
            log(data.query ? `Уточнение: «${data.query}» — ищу в теме «${currentTopic().topic}» только про это.` : 'Уточнение убрано.');
            watchParser();
        }, 'Уточнение');
}

/** Галочка «Автоперевод»: сохраняем и перерисовываем текущую запись без перезапроса поиска. */
/** Текст поста: перевод или оригинал по галочке «Автоперевод»; пометка «Переведено с …» и «Показать оригинал». */
function applyTextMode(item) {
    const on = $('filter-translate').checked;
    const hasTranslation = Boolean(item.translated_note);
    const showTranslation = on && hasTranslation && !state.showOriginal;
    const shown = showTranslation ? String(item.text || '') : String(item.text_original ?? item.text ?? '');
    state.shownText = shown.trim();
    renderPostView(item);   // лист поста: медиа, текст и статья — в просмотрщике; текст — перевод или оригинал
    const note = $('item-translate');
    const blank = !state.shownText || NO_TEXT.includes(state.shownText);   // перевод заглушки «Пост без текста» — не показываем
    note.hidden = !(on && hasTranslation) || blank;
    if (note.hidden) { note.replaceChildren(); return; }
    const toggle = el('button', { type: 'button', className: 'item-translate-toggle', textContent: state.showOriginal ? 'Показать перевод' : 'Показать оригинал',
        onclick: () => { state.showOriginal = !state.showOriginal; applyTextMode(state.item); } });
    note.replaceChildren(el('span', { textContent: `🌐 ${item.translated_note}` }), ' · ', toggle);
}

async function onTranslateToggle() {
    const box = $('filter-translate');
    box.disabled = true;
    try {
        await api('save_filters', { body: { auto_translate: box.checked } });
        const item = state.item;
        if (!item) return;
        if (box.checked && !item.translated_note && item.translated_from === undefined) return loadCurrentItem();   // перевода в ответе не было — перезапросим
        state.showOriginal = false;
        applyTextMode(item);
    } catch (error) { setMessage('item-error', `Настройка перевода не сохранена: ${error.message}`); }
    finally { box.disabled = false; }
}

async function saveFilters() {
    const boxes = Object.values(FILTERS).map($);
    const filters = Object.fromEntries(Object.entries(FILTERS).map(([key, id]) => [key, $(id).checked]));
    boxes.forEach(b => { b.disabled = true; });
    try {
        const data = await api('save_filters', { body: filters });
        for (const [key, id] of Object.entries(FILTERS)) $(id).checked = Boolean(data.filters?.[key] ?? filters[key]);
        const [fresh, active, video, mediaOnly, noText] = boxes.map(b => b.checked);
        log(`Фильтры подбора: ${fresh ? 'только новый контент (за сутки)' : 'контент за последний месяц'}, ${active ? 'только живые паблики, каналы и треды' : 'все источники, включая неактивные'}${video ? ', только с видео' : ''}${noText ? ', только медиа без подписи' : mediaOnly ? ', только с медиа' : ''}.`);
        watchParser();
        await loadCurrentItem();
    } catch (error) { setMessage('item-error', `Фильтры не сохранены: ${error.message}`); }
    finally { boxes.forEach(b => { b.disabled = false; }); syncMediaFilters(); }
}

/** Галочка «Показывать чувствительный и 18+ контент»: включение — после подтверждения возраста; темы и пул пересобираются. */
async function saveAdult() {
    const box = $('filter-adult');
    const allowed = box.checked;
    if (allowed && !confirm('Показывать чувствительный контент и материалы 18+ («Для взрослых», «Жесть»)?\n\nВам должно быть 18 лет. Публикуя такие материалы в каналы, вы сами отвечаете за соблюдение правил Telegram, ВК и закона.')) { box.checked = false; return; }
    box.disabled = true;
    try {
        const data = await api('adult_allow', { body: { allowed } });
        log(data.adult_allowed ? 'Чувствительный контент и 18+ включены.' : `Чувствительный контент и 18+ скрыты${data.topic_reset ? ': тема возвращена к теме по умолчанию' : ''}.`);
        await refreshTopicLabel();
        watchParser();
        await loadCurrentItem();
    } catch (error) { box.checked = !allowed; setMessage('item-error', `Настройка не сохранена: ${error.message}`); }
    finally { box.disabled = false; }
}

// ---- Публикация ----

/** Ракета по предварительной проверке поста: перечёркнута (нельзя) или с жёлтой точкой (замечания). */
export function applyRocketCheck(buttonEl, check, idleTitle = 'Запостить') {
    const problems = [...(check?.problems || []), ...(check?.warnings || [])];
    const blocked = check?.status === 'blocked';
    buttonEl.classList.toggle('rocket-blocked', blocked);
    buttonEl.classList.toggle('rocket-warn', !blocked && problems.length > 0);
    buttonEl.title = problems.length ? `${blocked ? 'Запостить нельзя' : 'Есть замечания'}: ${problems.join(' · ')}` : idleTitle;
}

function logPostCheck(check) {
    const problems = [...(check?.problems || []), ...(check?.warnings || [])];
    if (problems.length) log(`🚀 ${check.status === 'blocked' ? 'Этот пост нельзя опубликовать как есть' : 'Замечания к публикации'}: ${problems.join('; ')}.`);
}

/** Цель поста канала: первый канал — tg:main (канал по умолчанию), остальные — tg:<id>. */
function channelTarget(channel, index) {
    const name = channel.title || (channel.username ? '@' + channel.username : channel.id);
    return { value: index === 0 ? 'tg:main' : `tg:${channel.id}`, label: `Канал «${name}»` + (index === 0 && state.channels.length > 1 ? ' (по умолчанию)' : ''), hashtags: channel.hashtags || 'off' };
}
const telegramTargets = () => state.channels.map(channelTarget);

/** Канал по цели поста: tg:main — первый, tg:<id> — конкретный. */
const channelOfTarget = value => (value === 'tg:main' ? state.channels[0] : state.channels.find(c => c.id === value.slice(3)));

/** ИИ-подпись и стиль канала для редактора: цель поста → запросы к серверу; стиль канала кэшируется в state.channels. */
const channelParam = value => (value === 'tg:main' ? '' : value.slice(3));
const keepStyle = (value, style) => { const c = channelOfTarget(value); if (c) c.style = style; return style; };
const captionTools = () => ({
    style: value => channelOfTarget(value)?.style || {},
    generate: async (value, context) => (await api('ai_caption', { body: { channel: channelParam(value), ...context } })).text,
    excerpt: async url => (await api('news_excerpt', { body: { url } })).text,
    save: async (value, summary) => keepStyle(value, (await api('channel_style_save', { body: { channel: channelParam(value), summary } })).style),
    /** Изучение идёт на сервере в фоне (минуты на CPU): запускаем и опрашиваем, пока не освободится. */
    learn: async (value, onWait) => {
        const channel = channelParam(value);
        let style = keepStyle(value, (await api('channel_style_learn', { body: { channel } })).style);
        while (style.busy) {
            onWait?.();
            await new Promise(resolve => setTimeout(resolve, 3000));
            style = keepStyle(value, (await api('channel_style', { params: { channel } })).style);
        }
        return style;
    },
});

/** Что редактор умеет по расписанию: есть ли слоты у канала цели, есть ли личный аккаунт, запрос ближайшего слота. */
const scheduleOptions = () => ({
    native: state.nativeAccount,
    slots: value => Object.keys(channelOfTarget(value)?.schedule?.slots || {}).length > 0,
    nextSlot: value => api('telegram_next_slot', { params: { channel: value === 'tg:main' ? '' : value.slice(3) } }),
});

/** Подключённые каналы и личный аккаунт: нужны редактору поста (куда постить, слоты, «Отложенные»). */
async function refreshTelegramTarget() {
    if (DEBUG_MODE) return;
    try { const data = await api('telegram_queue'); state.channels = data.connected ? data.channels || [] : []; state.nativeAccount = Boolean(data.native_account); } catch { /* остаётся прежний список */ }
}

/** Название ресурса для журнала. */
const targetName = value => {
    const c = channelOfTarget(value);
    return c ? `Канал «${c.title || '@' + c.username}»` : 'Канал';
};

/** Текст поста по умолчанию: оригинал записи или описание ИИ. Так же, как в редакторе. */
const defaultText = item => {
    const original = String(item.text || '').trim();
    return (original && !NO_TEXT.includes(original) ? original : '') || item.description || '';
};

/** Медиа записи для поста: файлы, которые Telegram точно не примет (post_check), пропускаются. */
function itemMedia(item) {
    const bad = new Set(item.post_check?.bad || []);
    return itemSlides(item).filter((s, i) => !bad.has(i)).map((s, i) => ({ kind: s.video ? 'video' : 'image', src: s.video || s.image, thumb: s.image,
        name: (s.video || s.fallbackImage || s.image || '').split('/').pop().split('?')[0] || `медиа ${i + 1}` }));
}

/** POST с файлами через XMLHttpRequest: fetch не сообщает, сколько байт ушло на сервер. */
function postFormWithUpload(action, form, onUpload) {
    return new Promise((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        xhr.upload.addEventListener('progress', e => { if (e.lengthComputable) onUpload(Math.round(e.loaded / e.total * 100)); });
        xhr.addEventListener('load', () => {
            try {
                const data = JSON.parse(xhr.responseText);
                if (xhr.status >= 400) throw Object.assign(new Error(data?.message || `HTTP ${xhr.status}`), { status: xhr.status, data });
                (data?.ai_log || []).forEach(log);
                resolve(data);
            } catch (e) { reject(e); }
        });
        xhr.addEventListener('error', () => reject(new Error('Ошибка загрузки')));
        xhr.addEventListener('abort', () => reject(new Error('Загрузка отменена')));
        xhr.open('POST', apiUrl(action, { debug: DEBUG_MODE ? '1' : undefined }));
        xhr.setRequestHeader('X-Posta', '1');
        xhr.send(form);
    });
}

/**
 * Файлы из редактора уходят загрузкой, остальные медиа — адресом (кэш сайта или ссылка).
 * onProgress({stage, percent}): пока файлы идут на сервер — «Загрузка на сервер», дальше — этапы сервера из опроса (percent null — без процентов).
 */
async function publishToTelegram(post, onProgress = null) {
    const form = new FormData();
    form.append('text', post.text);
    if (post.article) form.append('article', JSON.stringify(post.article));   // статья: признак уйдёт в очередь, календарь покажет заголовок
    form.append('channel', post.target === 'tg:main' ? '' : post.target.slice(3));   // пусто — канал по умолчанию
    form.append('mode', post.mode || (post.scheduledTs ? 'native' : 'now'));
    if (post.scheduledTs) form.append('scheduled_ts', String(post.scheduledTs));
    if (post.poll) form.append('poll', JSON.stringify(post.poll));
    if (post.hashtags?.length) form.append('hashtags', JSON.stringify(post.hashtags));   // сервер оставит те, что разрешены каналу
    if (post.record) form.append('record', JSON.stringify(post.record));   // поля записи: сервер отметит её лайком после публикации
    const files = [];
    form.append('media', JSON.stringify(post.media.map(m => (m.file ? { kind: m.kind, name: m.name, upload: files.push(m.file) - 1 } : { kind: m.kind, src: m.src, name: m.name }))));
    files.forEach(f => form.append('files[]', f, f.name));
    // Сервер пишет этап и процент в файл по этому id, а мы опрашиваем его, пока идёт отправка
    const id = crypto.randomUUID().replaceAll('-', '');
    form.append('progress_id', id);
    let [uploading, done] = [files.length > 0 && !!onProgress, false];
    const report = p => { if (!done) onProgress(p); };
    const poll = onProgress && setInterval(() => {
        if (!uploading) api('telegram_publish_progress', { params: { id } }).then(p => p?.stage && report({ stage: p.stage, percent: p.percent ?? null })).catch(() => {});
    }, 700);
    const send = () => uploading
        ? postFormWithUpload('telegram_publish', form, percent => {
            if (!uploading) return;
            uploading = percent < 100;
            report({ stage: 'Загрузка на сервер', percent });
        })
        : api('telegram_publish', { body: form });
    let result;
    try {
        try {
            result = await send();
        } catch (error) {
            // Соцсеть не примет часть файлов: сервер спрашивает, публиковать ли без них
            if (!error.data?.confirm_skip || !confirm(error.message)) throw error;
            form.set('confirm_skip', '1');
            uploading = false;
            result = await send();
        }
    } finally {
        done = true;
        clearInterval(poll);
    }
    log(result.scheduled
        ? `🚀 ${targetName(post.target)}: пост запланирован на ${formatWhen(new Date(result.publish_at * 1000))}, ${result.native ? 'он в «Отложенных» Telegram' : 'он в очереди Posta и уйдёт сам'}.`
        : `🚀 ${targetName(post.target)}: пост опубликован${result.url ? `: [открыть в Telegram](${result.url})` : ''}.`);
    if (result.marked === 'liked') log('Пост отмечен лайком и убран из «Предложки».');
    return result;
}

/** Один пост во все отмеченные ресурсы по очереди: сбой одного не останавливает остальные. Если упали все — исключение (редактор останется открытым). */
async function publishToTargets(post, onProgress = null) {
    const failed = [];
    let marked = null;   // 'liked', если хоть один канал отметил запись сейчас; иначе 'already' при повторе
    for (const target of post.targets) {
        try {
            const result = await publishToTelegram({ ...post, target }, onProgress);
            if (result?.marked === 'liked' || (result?.marked === 'already' && !marked)) marked = result.marked;
        } catch (e) {
            failed.push(e.message);
            log(`✖ ${targetName(target)}: ${e.message}`);
        }
    }
    if (failed.length === post.targets.length) throw new Error(failed.join('; '));
    return { marked };
}

/** Поля записи, по которым сервер считает её ключ (тот же, что у пула и истории). */
const recordOf = item => ({ image: item.image || '', url: item.url || '', source_url: item.source_url || '', source_key: item.source_key || '',
    board: item.board || '', thread_id: item.thread_id || '', post_id: item.post_id || '', text: item.text || '', text_post: !!item.text_post });

/** Единое окно публикации для всех кнопок «Запостить»: редактор поста; ресурсы выбираются в нём (по умолчанию — основной канал). */
/** Адрес источника новости (сайт или RSS-лента с записью): https или ''. */
const newsSourceUrl = item => (String(item.source_key || '').startsWith('rss:') && String(item.source_url || '').startsWith('https://') ? item.source_url : '');

/** Редактор поста. Необязательно: scheduledTs и channel (id канала) — время и канал уже выбраны (календарь, слот); onDone — после постановки/публикации. */
export async function postItemToTelegram(item, { scheduledTs = 0, channel = '', onDone = null } = {}) {
    logPostCheck(item.post_check);
    await refreshTelegramTarget();
    if (!state.channels.length) {
        log('Telegram не подключён: бот и канал подключаются в «Личном кабинете» → «Соцсети».');
        return openAccountSection('social');
    }
    // В редакторе — тот текст, который видно сейчас (перевод или оригинал после «Показать оригинал»)
    const shownNow = item === state.item && state.shownText !== undefined ? state.shownText : item.text;
    // Статья: подпись как при публикации новости — заголовок, выжимка и источник (card.js)
    const original = cardKind(item) === 'article' ? articleCaption(item) : String(shownNow || '').trim();
    // Новость с сайта: по умолчанию в тексте — заголовок, анонс и ссылка на источник
    const sourceUrl = newsSourceUrl(item);
    const withSource = original && !NO_TEXT.includes(original) ? original : '';
    const sourceText = sourceUrl && withSource && !withSource.includes(sourceUrl) ? `${withSource}\n\nИсточник: ${sourceUrl}` : withSource;
    // Цель канала: первый канал — tg:main (как в telegramTargets), остальные — tg:<id>
    const target = !channel || state.channels[0]?.id === channel ? 'tg:main' : `tg:${channel}`;
    openPostEditor({
        key: item.image || item.display_image || '', picked: [target], targets: telegramTargets(), schedule: scheduleOptions(),
        scheduledTs, scheduledAt: scheduledTs ? formatWhen(new Date(scheduledTs * 1000)) : '', description: item.description || '', original: sourceText,
        media: itemMedia(item), hashtags: item.hashtags || [],   // файлы, которые Telegram точно не примет, в редактор не попадают
        ai: captionTools(), newsUrl: sourceUrl || '',
        onSubmit: (post, onProgress) => {
            // Ссылку на источник новости не стираем молча: без неё — вопрос, по умолчанию «Да»
            if (sourceUrl && !String(post.text || '').includes(sourceUrl) && confirm('Добавить ссылку на источник?')) post.text = `${post.text || ''}\n\nИсточник: ${sourceUrl}`.trim();
            if (cardKind(item) === 'article') post.article = articleBrief(item);
            post.record = recordOf(item);
            return publishToTargets(post, onProgress).then(async result => {
                // Опубликована текущая карточка «Подбора»: запись уже лайк, следующая — как после голоса (без второго запроса)
                if (result?.marked && item === state.item) await loadCurrentItem();
                onDone?.(result);
                return result;
            });
        }
    });
}

export function initializeContentTab({ onAddItem }) {
    initializeCardAi();
    // Строка «Ищем: …»: правка формата темы для этого пользователя
    $('topic-mode-edit').addEventListener('click', () => { $('topic-mode-editor').hidden = !$('topic-mode-editor').hidden; });
    const saveMode = body => api('topic_format_save', { body }).then(() => renderTopicInfo()).catch(error => log(`Не удалось сохранить формат темы: ${error.message}`));
    $('mode-save').addEventListener('click', () => saveMode({ media: $('mode-media').checked ? 'required' : 'optional', text: $('mode-text').checked ? 'required' : 'optional', judge: $('mode-judge').value }));
    $('mode-reset').addEventListener('click', () => saveMode({ reset: true }));
    bindMenu($('complaint-button'), $('complaint-menu'), item => vote('wrong', { reason: item.dataset.reason }));
    const cardGallery = () => recordGallery(state.item);
    registerViewerGallery('card', cardGallery);
    registerViewerPoster(({ kind, src, poster }) => {
        const item = kind === 'video'
            ? { image: poster || src, display_image: poster || src, video: src, display_video: src, text: '', post_check: null }
            : { image: src, display_image: src, text: '', post_check: null };
        return postItemToTelegram(item).catch(e => log(`Ошибка при публикации: ${e.message}`));
    });
    $('item-rocket').addEventListener('click', () => state.item && postItemToTelegram(state.item).catch(e => log(`Не удалось открыть редактор: ${e.message}`)));
    document.querySelectorAll('#content-tab [data-vote]').forEach(b => b.addEventListener('click', () => vote(b.dataset.vote)));
    $('item-search-button').addEventListener('click', searchForContent);
    $('item-add-button').addEventListener('click', onAddItem);
    $('topic-form').addEventListener('submit', e => {
        e.preventDefault();
        const topic = $('topic-input').value.trim();
        if (topic && topic !== currentTopic().topic && confirm(TOPIC_CONFIRM)) submitTopic(topic);
    });
    $('topic-force-new').addEventListener('click', e => submitTopic(e.currentTarget.dataset.typed || $('topic-input').value.trim(), true));
    Object.entries(FILTERS).forEach(([key, id]) => $(id).addEventListener('change', key === 'auto_translate' ? onTranslateToggle : key === 'media_no_text' ? onMediaNoTextToggle : saveFilters));
    $('filter-adult').addEventListener('change', saveAdult);
    initializeSecondTopic();
    initializeQueryField();
    onParserStateChange(running => { if (!running && !state.item) loadCurrentItem(); });
    onParserStatus(renderSearchProgress);
    if (DEBUG_MODE) { hide('item-search-button'); hide('item-add-button'); }
    refreshTopicLabel();
}
