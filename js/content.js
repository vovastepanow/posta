// content.js — вкладка «Подбор»: тема и вторая тема, фильтры, карточка записи (карусель, видео, активность), голоса, жалобы,
// публикация в Telegram (панель 🚀 и редактор поста).
import { $, COUNT, DEBUG_MODE, NO_TEXT, api, apiUrl, bindMenu, chip, el, hide, log, onParserStateChange, pluralRu, renderTags, safeSourcePostUrl, setMessage, show, watchParser } from './core.js';
import { openViewerGallery, registerViewerGallery, registerViewerPoster } from './viewer.js';
import { formatWhen, openPostEditor, pickDateTime } from './editor.js';
import { openAccountSection } from './account.js';

const state = { item: null, slides: [], index: 0, poll: null, voting: false, preloaded: [], telegram: null, parserStartTime: null, progressInterval: null, currentTopic: null };
const VIDEO_ERRORS = { 1: 'загрузка прервана', 2: 'сетевая ошибка', 3: 'файл не раскодировался', 4: 'формат не поддерживается или файл недоступен' };
const TOPIC_CONFIRM = 'Сменить тему поиска? Неоценённые записи по старой теме будут убраны из пула, и поиск начнётся заново. История оценок сохранится.';
export { TOPIC_CONFIRM };

/** Одна из областей вкладки: card | empty | error | searching. */
function showArea(area, message = '') {
    show('item-card-wrapper', area === 'card');
    show('item-empty', area === 'empty');
    setMessage('item-error', area === 'error' ? message : '');

    if (area === 'searching') {
        show('item-searching');
        clearInterval(state.progressInterval);
        state.parserStartTime = Date.now();
        console.log('🟢 Прогресс начат. Время:', state.parserStartTime, 'Тема:', state.currentTopic);
        updateParserProgress();
        state.progressInterval = setInterval(updateParserProgress, 2000);
    } else {
        hide('item-searching');
        clearInterval(state.progressInterval);
    }

    if (area === 'empty') $('item-empty-message').textContent = message || 'Пул контента пуст.';
    hide('item-loading');
}

/** Обновляет информацию о прогрессе парсера в UI. */
async function updateParserProgress() {
    if (!state.parserStartTime) {
        console.log('❌ parserStartTime не установлен');
        return;
    }
    
    try {
        const data = await api('parser_status');
        if (!data.running) {
            console.log('⏹️ Парсер остановлен');
            clearInterval(state.progressInterval);
            return;
        }
    } catch (e) {
        console.log('⚠️ Ошибка API:', e.message);
    }

    const elapsed = Math.floor((Date.now() - state.parserStartTime) / 1000);
    const elapsedMin = (elapsed / 60).toFixed(1);
    const remainingSeconds = Math.max(0, 2400 - elapsed);
    const remainingMin = Math.ceil(remainingSeconds / 60);
    const progressPercent = Math.min(100, Math.floor((elapsed / 2400) * 100));

    console.log(`⏱️ Прошло: ${elapsedMin}м | Осталось: ~${remainingMin}м | ${progressPercent}%`);

    const topicEl = $('parser-topic-name');
    const timeEl = $('parser-progress-time');
    const remainingEl = $('parser-progress-remaining');
    const percentEl = $('parser-progress-percent');
    const fillEl = $('parser-progress-fill');
    
    if (topicEl) topicEl.textContent = state.currentTopic || '—';
    if (timeEl) timeEl.textContent = elapsedMin;
    if (remainingEl) remainingEl.textContent = '~' + remainingMin;
    if (percentEl) percentEl.textContent = progressPercent;
    if (fillEl) fillEl.style.width = progressPercent + '%';
}
    if (percentEl) percentEl.textContent = progressPercent;
    if (fillEl) fillEl.style.width = progressPercent + '%';
}

// ---- Слайды: у каждого основной адрес (копия из кэша) и запасной (исходная ссылка) ----

function itemSlides(item) {
    if (!item) return [];
    const text = item.text || item.description || NO_TEXT[0];
    if (item.gallery?.length > 1) return item.gallery.map(f => ({ image: f.display_image || f.image, fallbackImage: f.image, video: f.video ? f.display_video || f.video : '', fallbackVideo: f.video || '', text }));
    if (item.video || item.display_video) return [{ image: item.display_image || item.image || '', fallbackImage: item.image || '', video: item.display_video || item.video, fallbackVideo: item.video || '', text }];
    const images = item.images?.length ? item.images : [item.image].filter(Boolean);
    return images.map((image, i) => ({ image: i === 0 ? item.display_image || image : image, fallbackImage: image, text: item.captions?.[i] || item.text || '' }));
}

/** Слайды записи как галерея просмотрщика. */
export function recordGallery(item, index = 0) {
    const sourceUrl = safeSourcePostUrl(item);
    return { index, onPost: item ? () => postItemToTelegram(item).catch(e => log(`Не удалось открыть редактор: ${e.message}`)) : null, postCheck: item?.post_check || null,
        items: itemSlides(item).map(s => ({ kind: s.video ? 'video' : 'image', src: s.video || s.image, fallback: s.video ? s.fallbackVideo : s.fallbackImage, poster: s.image, sourceUrl, alt: 'Запись' })) };
}

function setImage(image, slide) {
    const wrap = image.parentElement;
    wrap.classList.add('is-loading');
    image.onload = () => wrap.classList.remove('is-loading');
    image.onerror = () => {
        if (slide.fallbackImage && image.getAttribute('src') !== slide.fallbackImage) { image.src = slide.fallbackImage; return; }
        image.onerror = null;
        wrap.classList.remove('is-loading');
        image.hidden = true;
        if ($('item-video').hidden) { show('item-media-fallback'); log(`Картинка не загрузилась: ${slide.fallbackImage || slide.image}`); }
    };
    image.src = slide.image;
}

/** Видео в карточке играет само без звука; не открылось — исходная ссылка, потом обложка. */
function renderCardVideo(slide) {
    const video = $('item-video');
    video.onerror = null;
    if (!slide.video) { video.pause(); video.removeAttribute('src'); video.load(); video.hidden = true; return; }
    const wrap = video.parentElement;
    video.onerror = () => {
        const reason = VIDEO_ERRORS[video.error?.code] || 'неизвестная ошибка';
        if (slide.fallbackVideo && video.getAttribute('src') !== slide.fallbackVideo) { log(`Видео в карточке не открылось (${reason}), пробую исходную ссылку.`); video.src = slide.fallbackVideo; return; }
        log(`Видео в карточке не воспроизводится (${reason}): ${slide.fallbackVideo || slide.video}`);
        video.onerror = null;
        wrap.classList.remove('is-loading');
        video.hidden = true;
        $('item-image').hidden = !slide.image;
    };
    wrap.classList.add('is-loading');
    video.addEventListener('loadeddata', () => wrap.classList.remove('is-loading'), { once: true });
    Object.assign(video, { poster: slide.image || '', muted: true, hidden: false, src: slide.video });
    video.play().catch(e => log(`Браузер не запустил видео сам (${e.name}). Оно начнётся после первого клика или нажатия клавиши; если нет — проверьте разрешение автовоспроизведения для этого сайта и расширения, блокирующие видео.`));
}

function renderSlide() {
    const slide = state.slides[state.index] || { image: '', text: 'Нет изображения' };
    const image = $('item-image');
    const isVideo = Boolean(slide.video);
    hide('item-media-fallback');
    renderCardVideo(slide);
    image.classList.toggle('is-video-thumb', isVideo);
    Object.assign(image.dataset, { gallery: 'card', fallback: slide.fallbackImage || '', sourceUrl: safeSourcePostUrl(state.item) });
    image.alt = isVideo ? 'Видео: нажмите, чтобы смотреть' : 'Запись';
    $('item-video-mark').hidden = !isVideo;
    image.hidden = !slide.image || (isVideo && !$('item-video').hidden);
    if (slide.image) setImage(image, slide); else image.removeAttribute('src');
    $('item-slide-caption').textContent = slide.text || state.item?.text || NO_TEXT[0];
    const many = state.slides.length > 1;
    for (const b of [$('carousel-prev'), $('carousel-next')]) b.hidden = b.disabled = !many;
    $('item-slide-position').textContent = many ? `${state.index + 1} / ${state.slides.length}` : '';
}

function changeSlide(delta) {
    if (!state.slides.length) return;
    state.index = (state.index + delta + state.slides.length) % state.slides.length;
    renderSlide();
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

function renderCard(item, archiveInfo = null) {
    Object.assign(state, { item, slides: itemSlides(item), index: 0 });
    renderSlide();
    applyRocketCheck($('item-rocket'), item.post_check);
    state.preloaded = (item.preload || []).map(src => Object.assign(new Image(), { decoding: 'async', src }));   // следующие записи грузятся заранее
    const [relevance, rating] = [Number(item.relevance ?? 0), item.rating == null ? null : Number(item.rating)];
    $('item-rating').textContent = rating === null ? '' : `${rating}%`;
    $('item-rating').hidden = rating === null;
    $('item-relevance').textContent = `По теме: ${relevance}%`;
    renderTags($('item-tags'), item.tags_info);
    $('item-source').textContent = `Источник: ${item.source || 'неизвестно'}`;
    const posted = formatPostedAt(item.posted_at);
    setMessage('item-date', posted && `Опубликовано: ${posted}`);
    renderEngagement(item.engagement);
    const url = safeSourcePostUrl(item);
    if (url) $('item-source-link').href = url; else $('item-source-link').removeAttribute('href');
    $('item-source-link').hidden = !url;
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
            // Получаем текущую тему
            try {
                const settings = await api('settings');
                state.currentTopic = settings.topic || 'подбор контента';
                console.log('📍 Тема получена:', state.currentTopic);
            } catch (e) {
                state.currentTopic = 'подбор контента';
                console.log('⚠️ Ошибка при получении темы:', e.message);
            }
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
        if (data.status === 'empty' || !data.image) {
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
    try { await api('vote', { body: { vote: type, image: state.item.image, ...details } }); await loadCurrentItem(); }
    catch (error) { log(`Голос не сохранён: ${error.message}`); setMessage('item-error', `Не удалось сохранить оценку: ${error.message}`); }
    finally { state.voting = false; $('content-tab').classList.remove('is-busy'); }
}

// ---- Тема, вторая тема, фильтры ----

const FILTERS = { fresh_only: 'filter-fresh', active_sources_only: 'filter-active', video_only: 'filter-video' };

export async function refreshTopicLabel() {
    try {
        const data = await api('settings');
        const currentTopic = data.topic || '';
        $('topic-input').value = $('topic-input').dataset.saved = currentTopic;
        $('topic2-input').value = $('topic2-input').dataset.saved = data.topic2 || '';
        for (const [key, id] of Object.entries(FILTERS)) $(id).checked = key === 'active_sources_only' ? data.filters?.[key] !== false : Boolean(data.filters?.[key]);

        // Тема в заголовке: одна кнопка в стиле вкладки и выпадающий список всех тем
        try {
            const topics = (await api('topics')).topics || [];
            renderTopicSwitch(topics, currentTopic);
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

/** Вторая тема: поле только для чтения, клик активирует, клик мимо или Enter сохраняет, Esc отменяет. */
function initializeSecondTopic() {
    const [field, input] = [$('topic2-field'), $('topic2-input')];
    const deactivate = async (cancel = false) => {
        if (!field.classList.contains('is-active')) return;
        input.readOnly = true;
        field.classList.remove('is-active');
        const value = input.value = cancel ? input.dataset.saved : input.value.trim();
        if (value === input.dataset.saved) return;
        field.classList.add('is-busy');
        try {
            const data = await api('save_second_topic', { body: { topic2: value } });
            input.value = input.dataset.saved = data.topic2 || '';
            log(data.topic2 ? `Вторая тема: «${data.topic2}» — контент ищется по обеим.` : 'Вторая тема убрана.');
            watchParser();
            await loadCurrentItem();
        } catch (error) {
            input.value = input.dataset.saved;
            setMessage('item-error', `Вторая тема не сохранена: ${error.message}`);
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

async function saveFilters() {
    const boxes = Object.values(FILTERS).map($);
    const filters = Object.fromEntries(Object.entries(FILTERS).map(([key, id]) => [key, $(id).checked]));
    boxes.forEach(b => { b.disabled = true; });
    try {
        const data = await api('save_filters', { body: filters });
        for (const [key, id] of Object.entries(FILTERS)) $(id).checked = Boolean(data.filters?.[key] ?? filters[key]);
        const [fresh, active, video] = boxes.map(b => b.checked);
        log(`Фильтры подбора: ${fresh ? 'только новый контент (за сутки)' : 'контент за последний месяц'}, ${active ? 'только живые паблики, каналы и треды' : 'все источники, включая неактивные'}${video ? ', только с видео' : ''}.`);
        watchParser();
        await loadCurrentItem();
    } catch (error) { setMessage('item-error', `Фильтры не сохранены: ${error.message}`); }
    finally { boxes.forEach(b => { b.disabled = false; }); }
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

async function refreshTelegramTarget() {
    if (DEBUG_MODE) return;
    try { const data = await api('telegram_queue'); state.telegram = data.connected ? data.channel : null; } catch { /* остаётся прежняя подпись */ }
    document.querySelector('#post-target option[value="tg:main"]').textContent = state.telegram ? channelLabel() : 'Мой канал в Telegram (не подключён)';
}
const channelLabel = () => `Канал «${state.telegram.title || '@' + state.telegram.username}»`;

/** Файлы из редактора уходят загрузкой, остальные медиа — адресом (кэш сайта или ссылка). С отслеживанием прогресса если есть файлы. */
async function publishToTelegram(post, onProgress = null) {
    const form = new FormData();
    form.append('text', post.text);
    if (post.scheduledTs) form.append('scheduled_ts', String(post.scheduledTs));
    const files = [];
    form.append('media', JSON.stringify(post.media.map(m => (m.file ? { kind: m.kind, name: m.name, upload: files.push(m.file) - 1 } : { kind: m.kind, src: m.src, name: m.name }))));
    files.forEach(f => form.append('files[]', f, f.name));

    let result;
    if (files.length && onProgress) {
        result = await new Promise((resolve, reject) => {
            const xhr = new XMLHttpRequest();
            xhr.upload.addEventListener('progress', e => {
                if (e.lengthComputable) onProgress(Math.round(e.loaded / e.total * 100));
            });
            xhr.addEventListener('load', async () => {
                try {
                    const data = JSON.parse(xhr.responseText);
                    if (xhr.status >= 400) throw Object.assign(new Error(data?.message || `HTTP ${xhr.status}`), { status: xhr.status, data });
                    (data?.ai_log || []).forEach(log);
                    resolve(data);
                } catch (e) { reject(e); }
            });
            xhr.addEventListener('error', () => reject(new Error('Ошибка загрузки')));
            xhr.addEventListener('abort', () => reject(new Error('Загрузка отменена')));
            xhr.open('POST', apiUrl('telegram_publish', { debug: DEBUG_MODE ? '1' : undefined }));
            xhr.setRequestHeader('X-Postmaker', '1');
            xhr.send(form);
        });
    } else {
        result = await api('telegram_publish', { body: form });
    }
    log(result.scheduled ? `🚀 Пост запланирован на ${post.scheduledAt}: он в «Отложенных» канала в Telegram и уйдёт сам.` : `🚀 Пост опубликован${result.url ? `: [открыть в Telegram](${result.url})` : ''}.`);
}

/** Открывает редактор поста для записи (без бота — ведёт в кабинет). */
export async function postItemToTelegram(item, { target = null, scheduledTs = 0 } = {}) {
    logPostCheck(item.post_check);
    if (!target) await refreshTelegramTarget();
    if (!state.telegram && (!target || target.value === 'tg:main')) {
        log('Telegram не подключён: бот и канал подключаются в «Личном кабинете» → «Соцсети».');
        return openAccountSection('social');
    }
    const bad = new Set(item.post_check?.bad || []);   // файлы, которые Telegram точно не примет, в редактор не попадают
    const original = String(item.text || '').trim();
    openPostEditor({
        key: item.image || item.display_image || '', target: target || { value: 'tg:main', label: channelLabel() },
        scheduledTs, scheduledAt: scheduledTs ? formatWhen(new Date(scheduledTs * 1000)) : '', description: item.description || '',
        original: original && !NO_TEXT.includes(original) ? original : '',
        media: itemSlides(item).filter((s, i) => !bad.has(i)).map((s, i) => ({ kind: s.video ? 'video' : 'image', src: s.video || s.image, thumb: s.image,
            name: (s.video || s.fallbackImage || s.image || '').split('/').pop().split('?')[0] || `медиа ${i + 1}` })),
        onSubmit: (post, onProgress) => {
            if (post.target.startsWith('tg:')) return publishToTelegram(post, onProgress);
            throw new Error('Отправка во ВКонтакте пока не готова: сейчас публикуются только посты в Telegram-канал.');
        }
    });
}

function initializePostPanel() {
    const [panel, rocket, when] = [$('post-panel'), $('item-rocket'), $('post-when')];
    let scheduled = null;   // null — сразу
    const setOpen = open => {
        panel.hidden = !open;
        rocket.setAttribute('aria-expanded', String(open));
        if (!open) return;
        refreshTelegramTarget();
        if (scheduled && scheduled.getTime() < Date.now() + 60000) scheduled = null;
        when.textContent = scheduled ? formatWhen(scheduled) : 'Сразу';
    };
    rocket.addEventListener('click', () => setOpen(panel.hidden));
    when.addEventListener('click', async () => {
        const chosen = await pickDateTime({ value: scheduled });
        if (chosen !== undefined) { scheduled = chosen; when.textContent = chosen ? formatWhen(chosen) : 'Сразу'; }
    });
    document.addEventListener('click', e => { if (!panel.hidden && !e.target.closest('.rocket-wrap')) setOpen(false); });
    document.addEventListener('keydown', e => { if (e.key === 'Escape' && !panel.hidden) setOpen(false); });
    panel.addEventListener('submit', e => {
        e.preventDefault();
        const target = $('post-target');
        if (!target.value) return target.focus();
        if (!state.item) return;
        setOpen(false);
        postItemToTelegram(state.item, { target: { value: target.value, label: target.selectedOptions[0].textContent }, scheduledTs: scheduled ? Math.floor(scheduled.getTime() / 1000) : 0 });
    });
}

export function initializeContentTab({ onAddItem }) {
    bindMenu($('complaint-button'), $('complaint-menu'), item => vote('wrong', { reason: item.dataset.reason }));
    $('carousel-prev').addEventListener('click', () => changeSlide(-1));
    $('carousel-next').addEventListener('click', () => changeSlide(1));
    const cardGallery = () => ({ ...recordGallery(state.item), index: state.index });
    $('item-video').addEventListener('click', () => { const g = cardGallery(); openViewerGallery(g.items, g.index, g.onPost, g.postCheck); });
    registerViewerGallery('card', cardGallery);
    registerViewerPoster(({ kind, src, poster }) => {
        const item = kind === 'video'
            ? { image: poster || src, display_image: poster || src, video: src, display_video: src, text: '', post_check: null }
            : { image: src, display_image: src, text: '', post_check: null };
        return postItemToTelegram(item).catch(e => log(`Ошибка при публикации: ${e.message}`));
    });
    // Видео не запустилось само (фоновая вкладка, запрет автозапуска) — возобновляем при возврате и первом нажатии
    const resume = () => { const v = $('item-video'); if (!v.hidden && v.paused && v.getAttribute('src')) v.play().catch(() => {}); };
    document.addEventListener('visibilitychange', () => document.visibilityState === 'visible' && resume());
    for (const type of ['pointerdown', 'keydown']) document.addEventListener(type, resume, { passive: true });
    initializePostPanel();
    document.querySelectorAll('#content-tab [data-vote]').forEach(b => b.addEventListener('click', () => vote(b.dataset.vote)));
    $('item-search-button').addEventListener('click', searchForContent);
    $('item-add-button').addEventListener('click', onAddItem);
    $('topic-form').addEventListener('submit', e => {
        e.preventDefault();
        const topic = $('topic-input').value.trim();
        if (topic && topic !== $('topic-input').dataset.saved && confirm(TOPIC_CONFIRM)) submitTopic(topic);
    });
    $('topic-force-new').addEventListener('click', e => submitTopic(e.currentTarget.dataset.typed || $('topic-input').value.trim(), true));
    Object.values(FILTERS).forEach(id => $(id).addEventListener('change', saveFilters));
    initializeSecondTopic();
    onParserStateChange(running => { if (!running && !state.item) loadCurrentItem(); });
    if (DEBUG_MODE) { hide('item-search-button'); hide('item-add-button'); }
    refreshTopicLabel();
}
