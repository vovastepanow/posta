// content.js — вкладка «Подбор»: тема и вторая тема, фильтры, карточка записи (карусель, видео, активность), голоса, жалобы,
// публикация в Telegram (панель 🚀 и редактор поста).
import { $, COUNT, DEBUG_MODE, NO_TEXT, api, apiUrl, bindMenu, chip, currentTopic, el, hide, log, onParserStateChange, onParserStatus, pluralRu, renderTags, safeSourcePostUrl, setCurrentTopic, setMessage, show, watchParser } from './core.js';
import { openViewerGallery, registerViewerGallery, registerViewerPoster } from './viewer.js';
import { formatWhen, openPostEditor } from './editor.js';
import { openAccountSection } from './account.js';

const state = { item: null, slides: [], index: 0, poll: null, voting: false, preloaded: [], channels: [], nativeAccount: false };
const VIDEO_ERRORS = { 1: 'загрузка прервана', 2: 'сетевая ошибка', 3: 'файл не раскодировался', 4: 'формат не поддерживается или файл недоступен' };
const TOPIC_CONFIRM = 'Сменить тему поиска? Неоценённые записи по старой теме будут убраны из пула, и поиск начнётся заново. История оценок сохранится.';
export { TOPIC_CONFIRM };

/** Одна из областей вкладки: card | empty | error | searching. */
function showArea(area, message = '') {
    show('item-card-wrapper', area === 'card');
    show('item-empty', area === 'empty');
    setMessage('item-error', area === 'error' ? message : '');
    show('item-searching', area === 'searching');
    if (area === 'searching') $('search-progress-topic').textContent = currentTopic().topic || 'контент';
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

// Старые функции больше не используются, но оставляем для совместимости

function renderSlide() {
    const grid = $('item-media-grid');
    grid.replaceChildren();

    if (!state.slides.length) {
        grid.appendChild(el('div', { className: 'media-item', style: 'grid-column: 1 / -1; padding: 24px;' },
            el('div', { textContent: 'Нет медиа' })));
        return;
    }

    // Отображаем ВСЕ медиа элементы в грид
    for (const slide of state.slides) {
        const container = el('div', { className: 'media-item' });

        if (slide.video) {
            const video = el('video', {
                className: 'card-video',
                muted: true,
                loop: true,
                autoplay: true,
                playsinline: true,
                preload: 'auto',
                poster: slide.image || '',
                'aria-label': 'Видео: нажмите, чтобы смотреть со звуком'
            });

            video.src = slide.video;
            video.onerror = () => {
                if (slide.fallbackVideo && video.src !== slide.fallbackVideo) {
                    video.src = slide.fallbackVideo;
                    return;
                }
                video.hidden = true;
                // Постер ролика — мелкое размытое превью: в альбоме рядом обычно та же картинка в хорошем качестве
                if (grid.children.length > 1) return void container.remove();
                if (slide.image) {
                    const img = el('img', { alt: 'Запись', src: slide.image });
                    img.onerror = () => { img.hidden = true; };
                    container.replaceChildren(img);
                }
            };

            video.play().catch(() => {});
            container.appendChild(video);

            // Иконка видео
            const videoMark = el('span', { className: 'video-mark', title: 'Видео: клик откроет со звуком' },
                el('svg', { viewBox: '0 0 24 24', 'aria-hidden': 'true' },
                    el('rect', { x: '2', y: '6', width: '13', height: '12', rx: '3', fill: 'currentColor' }),
                    el('path', { d: 'M16.5 10.2 21 7.6v8.8l-4.5-2.6z', fill: 'currentColor' })));
            container.appendChild(videoMark);
        } else if (slide.image) {
            const img = el('img', { alt: 'Запись', src: slide.image });
            img.onerror = () => {
                if (slide.fallbackImage && img.src !== slide.fallbackImage) {
                    img.src = slide.fallbackImage;
                    return;
                }
                img.hidden = true;
            };

            Object.assign(img.dataset, {
                gallery: 'card',
                fallback: slide.fallbackImage || '',
                sourceUrl: safeSourcePostUrl(state.item)
            });

            container.appendChild(img);
        }

        grid.appendChild(container);
    }
}

function changeSlide(delta) {
    // Больше не используется в режиме сетки, но оставляем для совместимости
    if (!state.slides.length) return;
    state.index = (state.index + delta + state.slides.length) % state.slides.length;
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
    // «Свежак!»: пост опубликован меньше двух часов назад
    document.querySelector('#item-card-wrapper .card')?.classList.toggle('is-fresh', Number(item.posted_at) > 0 && Date.now() / 1000 - Number(item.posted_at) < 2 * 3600);
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
    try {
        await api('vote', { body: { vote: type, image: state.item.image, ...details } });
        if (type === 'wrong' && details.reason === 'match_error') {
            log('🚩 Жалоба учтена: ИИ увидит эту запись как пример «не по теме», источник теряет рейтинг, а каждые 5 жалоб уточняют профиль темы.');
        }
        await loadCurrentItem();
    }
    catch (error) { log(`Голос не сохранён: ${error.message}`); setMessage('item-error', `Не удалось сохранить оценку: ${error.message}`); }
    finally { state.voting = false; $('content-tab').classList.remove('is-busy'); }
}

// ---- Тема, вторая тема, фильтры ----

const FILTERS = { fresh_only: 'filter-fresh', active_sources_only: 'filter-active', video_only: 'filter-video' };

export async function refreshTopicLabel() {
    try {
        const data = await api('settings');
        const topic = data.topic || '';
        $('topic-input').value = $('topic-input').dataset.saved = topic;
        $('topic2-input').value = $('topic2-input').dataset.saved = data.topic2 || '';
        setCurrentTopic(topic, data.topic2 || '');
        for (const [key, id] of Object.entries(FILTERS)) $(id).checked = key === 'active_sources_only' ? data.filters?.[key] !== false : Boolean(data.filters?.[key]);

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
            setCurrentTopic(currentTopic().topic, data.topic2 || '');
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

/** Цель поста канала: первый канал — tg:main (канал по умолчанию), остальные — tg:<id>. */
function channelTarget(channel, index) {
    const name = channel.title || (channel.username ? '@' + channel.username : channel.id);
    return { value: index === 0 ? 'tg:main' : `tg:${channel.id}`, label: `Канал «${name}»` + (index === 0 && state.channels.length > 1 ? ' (по умолчанию)' : '') };
}
const telegramTargets = () => state.channels.map(channelTarget);

/** Канал по цели поста: tg:main — первый, tg:<id> — конкретный. */
const channelOfTarget = value => (value === 'tg:main' ? state.channels[0] : state.channels.find(c => c.id === value.slice(3)));

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
    form.append('channel', post.target === 'tg:main' ? '' : post.target.slice(3));   // пусто — канал по умолчанию
    form.append('mode', post.mode || (post.scheduledTs ? 'native' : 'now'));
    if (post.scheduledTs) form.append('scheduled_ts', String(post.scheduledTs));
    if (post.poll) form.append('poll', JSON.stringify(post.poll));
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
    let result;
    try {
        result = uploading
            ? await postFormWithUpload('telegram_publish', form, percent => {
                if (!uploading) return;
                uploading = percent < 100;
                report({ stage: 'Загрузка на сервер', percent });
            })
            : await api('telegram_publish', { body: form });
    } finally {
        done = true;
        clearInterval(poll);
    }
    log(result.scheduled
        ? `🚀 ${targetName(post.target)}: пост запланирован на ${formatWhen(new Date(result.publish_at * 1000))}, ${result.native ? 'он в «Отложенных» Telegram' : 'он в очереди Posta и уйдёт сам'}.`
        : `🚀 ${targetName(post.target)}: пост опубликован${result.url ? `: [открыть в Telegram](${result.url})` : ''}.`);
}

/** Один пост во все отмеченные ресурсы по очереди: сбой одного не останавливает остальные. Если упали все — исключение (редактор останется открытым). */
async function publishToTargets(post, onProgress = null) {
    const failed = [];
    for (const target of post.targets) {
        try {
            await publishToTelegram({ ...post, target }, onProgress);
        } catch (e) {
            failed.push(e.message);
            log(`✖ ${targetName(target)}: ${e.message}`);
        }
    }
    if (failed.length === post.targets.length) throw new Error(failed.join('; '));
}

/** Единое окно публикации для всех кнопок «Запостить»: редактор поста; ресурсы выбираются в нём (по умолчанию — основной канал). */
export async function postItemToTelegram(item) {
    logPostCheck(item.post_check);
    await refreshTelegramTarget();
    if (!state.channels.length) {
        log('Telegram не подключён: бот и канал подключаются в «Личном кабинете» → «Соцсети».');
        return openAccountSection('social');
    }
    const original = String(item.text || '').trim();
    openPostEditor({
        key: item.image || item.display_image || '', picked: ['tg:main'], targets: telegramTargets(), schedule: scheduleOptions(),
        scheduledTs: 0, scheduledAt: '', description: item.description || '', original: original && !NO_TEXT.includes(original) ? original : '',
        media: itemMedia(item),   // файлы, которые Telegram точно не примет, в редактор не попадают
        onSubmit: (post, onProgress) => publishToTargets(post, onProgress)
    });
}

export function initializeContentTab({ onAddItem }) {
    bindMenu($('complaint-button'), $('complaint-menu'), item => vote('wrong', { reason: item.dataset.reason }));
    const cardGallery = () => recordGallery(state.item);
    // Обработка клика по видео/изображению в грид-контейнере
    const mediaGrid = $('item-media-grid');
    mediaGrid.addEventListener('click', e => {
        const mediaEl = e.target.closest('img, video');
        if (mediaEl) {
            const g = cardGallery();
            const index = Array.from(mediaGrid.querySelectorAll('img, video')).indexOf(mediaEl);
            openViewerGallery(g.items, Math.max(0, index), g.onPost, g.postCheck);
        }
    });
    registerViewerGallery('card', cardGallery);
    registerViewerPoster(({ kind, src, poster }) => {
        const item = kind === 'video'
            ? { image: poster || src, display_image: poster || src, video: src, display_video: src, text: '', post_check: null }
            : { image: src, display_image: src, text: '', post_check: null };
        return postItemToTelegram(item).catch(e => log(`Ошибка при публикации: ${e.message}`));
    });
    // Видео не запустилось само (фоновая вкладка, запрет автозапуска) — возобновляем при возврате и первом нажатии
    const resume = () => {
        const videos = $('item-media-grid').querySelectorAll('video');
        for (const v of videos) {
            if (!v.hidden && v.paused && v.getAttribute('src')) v.play().catch(() => {});
        }
    };
    document.addEventListener('visibilitychange', () => document.visibilityState === 'visible' && resume());
    for (const type of ['pointerdown', 'keydown']) document.addEventListener(type, resume, { passive: true });
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
    Object.values(FILTERS).forEach(id => $(id).addEventListener('change', saveFilters));
    initializeSecondTopic();
    onParserStateChange(running => { if (!running && !state.item) loadCurrentItem(); });
    onParserStatus(renderSearchProgress);
    if (DEBUG_MODE) { hide('item-search-button'); hide('item-add-button'); }
    refreshTopicLabel();
}
