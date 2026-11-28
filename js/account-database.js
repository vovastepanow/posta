// Database browser: one loaded collection and its immutable edit scope.
import { $, api, ask, el, log, onTopicChange, safeSourcePostUrl, setMessage, watchParser } from './core.js';
import { openViewerGallery, videoFields } from './viewer.js';

const db = { databases: [], ready: null, items: [], visible: [], editing: null, currentIndex: null, view: null, abort: null, busy: false, mediaMissing: new Set() };
const dbSelected = () => [$('database-select').value, $('database-collection-select').value];
const dbStatus = (text, error = false) => { $('database-status').textContent = text; $('database-status').classList.toggle('error-msg', error); };
const option = ({ id, label }) => el('option', { value: id, textContent: label });

export function loadDatabaseOptions() {
    return db.ready ??= api('database_options').then(async d => {
        // Отфильтровываем скрытые базы (старые JSON версии)
        db.databases = d.databases || [];
        $('database-select').replaceChildren(...db.databases.map(option));
        $('database-topic-select').replaceChildren(el('option', { value: '', textContent: 'Текущая тема' }), ...(d.topics || []).map(t => el('option', { value: t.id, textContent: t.path || t.title })));
        await loadCollections();
    }).catch(error => { db.ready = null; dbStatus(`Не удалось загрузить базы: ${error.message}`, true); });
}

async function loadCollections() {
    db.currentIndex = null;
    $('database-search').value = '';
    $('database-topic-label').hidden = $('database-select').value !== 'topic_sources';
    $('database-collection-select').replaceChildren(...(db.databases.find(d => d.id === $('database-select').value)?.collections || []).map(option));
    await loadRecords();
}


async function loadRecords() {
    db.abort?.abort();
    const controller = new AbortController();
    db.abort = controller;
    const [database, collection] = dbSelected();
    const definition = db.databases.find(d => d.id === database);
    const readOnly = Boolean(definition?.read_only);
    db.view = null;
    db.items = [];
    db.visible = [];
    db.currentIndex = null;
    db.mediaMissing = new Set();
    $('database-add').hidden = readOnly;
    $('database-add').disabled = true;
    $('database-browser').hidden = true;
    $('database-count').textContent = '';
    $('database-list').replaceChildren();
    $('database-records').replaceChildren();
    if (!database || !collection || !definition) {
        dbStatus('Выберите базу для просмотра записей.');
        return;
    }
    dbStatus('Загружаю записи...');
    try {
        const d = await api('database_read', {
            params: { database, collection, ...(database === 'topic_sources' ? { topic: $('database-topic-select').value } : {}) },
            signal: controller.signal
        });
        if (db.abort !== controller) return;
        db.items = d.items || [];
        db.mediaMissing = new Set(d.media_missing || []);
        db.view = { database, collection, topic: d.topic || '', byTopic: Boolean(definition.by_topic), readOnly,
            databaseLabel: d.database_label, collectionLabel: d.collection_label };
        $('database-add').disabled = readOnly;
        dbStatus('');
    } catch (error) {
        if (db.abort !== controller || controller.signal.aborted) return;
        dbStatus(`Не удалось прочитать коллекцию: ${error.message}`, true);
        return;
    }

    if (!db.items.length) {
        const empty = db.view.topic ? `В этой коллекции нет записей по теме «${db.view.topic}».` : 'В этой коллекции пока нет записей.';
        dbStatus(empty);
        $('database-records').replaceChildren(el('div', { className: 'database-empty', textContent: empty }));
        $('database-count').textContent = '0';
        return;
    }
    renderDatabaseList();
    if (db.visible.length) selectRecord(db.visible[0]);
}

function renderDatabaseList() {
    const query = $('database-search').value.trim().toLocaleLowerCase();
    db.visible = [];
    for (const [index, record] of db.items.entries()) {
        if (!query || JSON.stringify(record).toLocaleLowerCase().includes(query)) db.visible.push(index);
    }
    $('database-count').textContent = query ? `${db.visible.length} из ${db.items.length}` : String(db.items.length);
    const items = db.visible.map(idx => {
        const record = db.items[idx];
        const label = record && typeof record === 'object' ? (record.name || record.title || record.text || record.key || record.value || `Запись #${idx + 1}`) : String(record);
        const subtitle = record && typeof record === 'object' ? [record.platform || record.source, record.handle || record.topic || record.id].filter(Boolean).join(' · ') : '';
        const btn = el('button', { type: 'button', className: 'database-list-item', role: 'option' }, el('span', { className: 'db-item-number', textContent: String(idx + 1).padStart(2, '0') }), el('span', { className: 'db-item-copy' }, el('strong', { textContent: String(label) }), el('small', { textContent: subtitle || 'Запись' })));
        btn.dataset.index = String(idx);
        btn.addEventListener('click', () => selectRecord(idx));
        return btn;
    });
    $('database-list').replaceChildren(...items);
    if (!items.length && db.items.length) $('database-list').append(el('div', { className: 'db-no-results', textContent: 'По вашему запросу ничего не найдено.' }));
    updateListSelection();
}

function updateListSelection() {
    $('database-list').querySelectorAll('.database-list-item').forEach(btn => {
        const active = Number(btn.dataset.index) === db.currentIndex;
        btn.classList.toggle('active', active);
        btn.setAttribute('aria-selected', String(active));
    });
}

function selectRecord(index) {
    if (!db.view || !db.visible.includes(index)) return;
    db.currentIndex = index;
    updateListSelection();
    renderDatabaseBrowser();
}

function renderDatabaseBrowser() {
    const idx = db.currentIndex;
    const record = db.items[idx];
    const view = db.view;

    $('database-browser').hidden = false;
    $('database-records').replaceChildren();

    // Хлебные крошки
    $('database-breadcrumb').replaceChildren(
        el('span', { textContent: view.databaseLabel }),
        el('span', { className: 'separator', textContent: '›' }),
        el('span', { textContent: view.collectionLabel }),
        el('span', { className: 'separator', textContent: '›' }),
        view.topic ? el('span', { textContent: `тема «${view.topic}»` }) : null,
        view.topic ? el('span', { className: 'separator', textContent: '›' }) : null,
        el('span', { textContent: `Запись #${idx + 1} / ${db.items.length}` })
    );

    // Кнопки навигации
    $('database-prev').disabled = $('database-next').disabled = db.visible.length < 2;

    // Отключаем редактирование для SQL таблиц
    for (const id of ['database-edit', 'database-delete']) {
        $(id).disabled = view.readOnly;
        $(id).title = view.readOnly ? 'Это представление доступно только для просмотра' : '';
    }

    // Представление записи
    renderRecordDetails(record, idx);
}

function renderRecordDetails(record, index) {
    const object = record !== null && typeof record === 'object' && !Array.isArray(record);
    const fields = object ? Object.entries(record) : [['value', record]];
    const container = el('div', { className: 'database-record-content' });
    const media = !db.mediaMissing.has(index) ? dbMediaItem(record) : null;
    if (media) {
        const preview = media.kind === 'image'
            ? el('img', { src: media.src, alt: '', loading: 'lazy' })
            : el('video', { src: media.src, controls: true });
        const wrapper = el('div', { className: 'database-media-preview' }, preview);
        if (media.kind === 'image') {
            wrapper.addEventListener('click', () => openViewerGallery([media], 0));
            wrapper.title = 'Открыть изображение';
        }
        container.append(wrapper);
    }
    const rows = el('div', { className: 'database-fields' }, ...fields.map(([key, value]) => renderRecordField(key, value)));
    container.append(rows);
    $('database-record-view').replaceChildren(container);
}

/** Поля записей в базах: [название, что хранится]. Нет в списке — показываем имя поля как есть. */
const DB_FIELDS = {
    image: ['Картинка (ссылка)', 'Адрес картинки у источника; по нему же записи считаются повторами'],
    images: ['Картинки (ссылки)', 'Все картинки поста по адресам источника'],
    image_local: ['Картинка', 'Копия картинки на нашем диске'],
    video: ['Видео (ссылка)', 'Адрес видео у источника'],
    video_local: ['Видео (копия)', 'Копия видео на нашем диске'],
    gallery: ['Альбом', 'Остальные картинки и видео этого поста'],
    video_unavailable: ['Видео недоступно', 'Источник не отдаёт видео этого поста'],
    media_checked: ['Медиа проверено', 'Файлы поста уже проверялись'],
    media_cached: ['Медиа сохранено', 'Копия файлов лежит у нас на диске'],
    media_gone: ['Удалено у источника', 'Источник удалил пост, своя копия не сохранилась'],
    text: ['Текст поста', 'Подпись из источника'],
    captions: ['Подписи', 'Тексты поста в том порядке, в каком идут картинки'],
    description: ['Описание ИИ', 'Что на картинке, по мнению модели'],
    why_relevant: ['Почему подходит', 'Объяснение модели, почему пост подходит теме'],
    relevance: ['Подходит теме, %', 'Оценка модели: насколько пост подходит теме (0–100)'],
    rating: ['Оценка ИИ', 'Итоговая оценка поста моделью (0–100)'],
    rating_assigned: ['Оценка ИИ', 'Оценка, которую модель поставила посту до вашего голоса'],
    topic: ['Тема', 'Тема поиска, под которую подобран пост'],
    tags: ['Метки', 'Темы из дерева, к которым ИИ отнёс пост'],
    format: ['Формат', 'Мем, фото, видео и т. п. — определяет ИИ'],
    source: ['Источник', 'Канал, паблик или доска, откуда взят пост'],
    source_key: ['Код источника', 'Служебный код источника, например telegram:имя_канала'],
    source_url: ['Ссылка на пост', 'Адрес поста у источника'],
    url: ['Ссылка', 'Адрес поста'],
    board: ['Доска', 'Доска 2ch'],
    thread_id: ['Тред', 'Номер треда на Дваче'],
    post_id: ['Номер поста', 'Номер поста в источнике'],
    posted_at: ['Опубликован', 'Когда пост вышел у источника'],
    pooled_at: ['В пуле с', 'Когда пост попал в пул'],
    timestamp: ['Когда оценено', 'Время вашего голоса или добавления записи'],
    complaint_reason: ['Причина жалобы', 'Почему вы отметили пост как неподходящий'],
    engagement: ['Активность', 'Просмотры, реакции и ответы на момент сбора'],
    phash: ['Отпечаток картинки', 'Короткий код картинки: по нему находятся одинаковые и похожие картинки'],
    proposal_hidden: ['Скрыто из «Предложки»', 'Пост убран из «Предложки», оценка осталась'],
    proposal_dead: ['Недоступно для «Предложки»', 'Пост нельзя показать в «Предложке»: источник его удалил'],
    image_key: ['Ключ картинки', 'Адрес картинки, по которому ищутся повторы'],
    user_id: ['Владелец', 'Служебный номер пользователя'],
    collection: ['Коллекция', 'Лайки, дизлайки, без решения или жалобы'],
    position: ['Порядок', 'Место записи в списке'],
    id: ['Код', 'Служебный номер записи'],
    name: ['Название', ''], title: ['Название', ''], key: ['Ключ', ''], value: ['Значение', ''],
    scope: ['Широта темы', 'Широкая или узкая тема'], parent: ['Родитель', 'Тема выше в дереве'], aliases: ['Синонимы', 'Другие названия темы'],
    about: ['О теме', 'Описание темы для ИИ'], classified: ['Разобрана', 'Тема уже привязана к дереву'], smart: ['Умная', 'Тема ищется по смыслу, а не по словам'],
    profile: ['Профиль вкуса', 'Что нравится и не нравится по этой теме'], manual: ['Создана вручную', ''], seed: ['Из готового дерева', ''], nsfw: ['18+', ''],
    created_at: ['Создано', ''], at: ['Время', ''], status: ['Статус', ''], error: ['Ошибка', ''], channel: ['Канал', 'Код канала'], channel_title: ['Название канала', ''],
    scheduled_for: ['Запланировано на', ''], media: ['Файлов', 'Сколько файлов в посте'],
    platform: ['Площадка', ''], vote: ['Голос', 'approve — источник нравится, reject — нет'], reason: ['Причина', ''], score: ['Баллы', 'Оценка источника'],
    topics: ['Темы', ''], frozen_until: ['Заморожен до', 'Источник временно не опрашивается'], frozen_reason: ['Причина заморозки', ''],
    avatar: ['Аватар', ''], activity: ['Активность', ''], preview: ['Превью', ''], audience: ['Аудитория', 'Подписчики'], auto: ['Найден автоматически', ''],
    source_id: ['Код источника', ''], handle: ['Адрес', ''], subscribers: ['Подписчики', ''],
};
const dbFieldLabel = key => DB_FIELDS[key]?.[0] || key;

function renderRecordField(key, value) {
    let displayValue;
    if (value === null || value === undefined || value === '') {
        displayValue = el('span', { className: 'database-record-field-value', textContent: '(пусто)' });
    } else if (typeof value === 'boolean') {
        displayValue = el('span', { className: 'database-record-field-value', textContent: value ? 'да' : 'нет' });
    } else if (DB_TIME_FIELDS.includes(key) && Number(value) > 0) {
        displayValue = el('span', { className: 'database-record-field-value', textContent: new Date(Number(value) * 1000).toLocaleString('ru-RU') });
    } else if (Array.isArray(value) && value.every(v => typeof v === 'string')) {
        displayValue = el('span', { className: 'database-record-field-value', textContent: value.length ? value.join(', ') : '—' });
    } else if (typeof value === 'object') {
        displayValue = el('code', { className: 'database-record-field-value code', textContent: JSON.stringify(value, null, 2) });
    } else {
        const text = String(value);
        displayValue = /^https?:\/\//.test(text)
            ? el('a', { href: text, target: '_blank', rel: 'noopener noreferrer', className: 'database-record-field-value', textContent: text })
            : el('span', { className: 'database-record-field-value', textContent: text });
    }

    return el('div', { className: 'database-record-field' },
        el('div', { className: 'database-record-field-label', textContent: dbFieldLabel(key), title: DB_FIELDS[key]?.[1] ? `${DB_FIELDS[key][1]} (поле ${key})` : `поле ${key}` }),
        displayValue
    );
}

const DB_TIME_FIELDS = ['posted_at', 'timestamp', 'pooled_at', 'created_at', 'at'];
/** Запись как элемент единого медиапросмотрщика (видео — с постером, картинка — локальная копия или исходная ссылка). */
function dbMediaItem(r) {
    if (r === null || typeof r !== 'object') return null;
    const sourceUrl = safeSourcePostUrl(r) || '';
    const video = r.video_local || r.video;
    if (video) return { kind: 'video', src: r.video_local || r.video, poster: r.image_local || '', fallback: r.video || '', sourceUrl, alt: r.text || '' };
    const image = r.image_local || r.image;
    return image ? { kind: 'image', src: image, fallback: r.image || '', sourceUrl, alt: r.text || '', ...videoFields(r, sourceUrl) } : null;
}

/** Заготовка новой записи под коллекцию. */
function template(database, collection) {
    const now = Math.floor(Date.now() / 1000);
    return database === 'content_pool' ? { image: '', text: '', description: '', why_relevant: '', relevance: 0, rating: 0, source: '', board: '', thread_id: '', post_id: '', source_url: '' }
        : database === 'content_history' ? { image: '', text: '', description: '', rating_assigned: 0, source: '', timestamp: now }
        : { '2ch_boards': 'b', vk_publics: { id: '', name: '' }, telegram_channels: { id: '', name: '' }, rss_feeds: { id: '', name: '' }, ok_groups: { id: '', name: '' }, tiktok_accounts: { id: '', name: '' }, source_votes: { source_id: '', platform: '', name: '', vote: 'approve', timestamp: now } }[collection] ?? {};
}

// A dialog or confirmation must retain the record and collection the user
// actually saw, not whatever happens to occupy its index after a refresh.
function captureEdit(index = null) {
    if (!db.view || db.view.readOnly || (index !== null && !db.visible.includes(index))) return null;
    return { view: db.view, index, expected: index === null ? undefined : structuredClone(db.items[index]) };
}

function openDialog(title, record, snapshot = captureEdit()) {
    if (!snapshot) return;
    db.editing = snapshot;
    $('database-dialog-title').textContent = title;
    $('database-record-json').value = JSON.stringify(record, null, 2);
    setMessage('database-dialog-status', '');
    $('database-edit-dialog').showModal();
}

function editError(snapshot, message) {
    if (db.editing === snapshot && $('database-edit-dialog').open) setMessage('database-dialog-status', message);
    else dbStatus(message, true);
}

async function change(action, snapshot, extra = {}) {
    if (!snapshot || db.busy) return false;
    if (snapshot.view !== db.view) {
        editError(snapshot, 'Список изменился. Откройте запись заново.');
        return false;
    }
    const { database, collection, topic, byTopic } = snapshot.view;
    const { index, expected } = snapshot;
    const body = { database, collection, ...extra, ...(byTopic ? { expected_topic: topic } : {}),
        ...(index === null ? {} : { index, expected }) };
    db.busy = true;
    const submit = $('database-edit-form').querySelector('[type=submit]');
    submit.disabled = true;
    try {
        await api(action, { body });
        if (db.editing === snapshot) $('database-edit-dialog').close();
        if (dbSelected().join('|') === [database, collection].join('|')) await loadRecords();
        return true;
    } catch (error) {
        editError(snapshot, `Не удалось ${action === 'database_delete' ? 'удалить' : 'сохранить'} запись: ${error.message}`);
        return false;
    } finally {
        db.busy = false;
        submit.disabled = false;
    }
}

export async function openPoolRecordEditor() {
    await loadDatabaseOptions();
    $('database-select').value = 'content_pool';
    await loadCollections();
    if (db.view?.database === 'content_pool' && db.view.collection === 'items') {
        openDialog('Добавить запись', template('content_pool', 'items'));
    }
}

export function initializeDatabases(onReset, isActive = () => true) {
    onTopicChange(() => {
        const [database] = dbSelected();
        if (!db.ready || !(db.databases.find(d => d.id === database)?.by_topic || (database === 'topic_sources' && !$('database-topic-select').value))) return;
        if (isActive()) loadRecords();
        else { db.abort?.abort(); db.abort = null; db.view = null; db.ready = null; }
    });
    $('database-select').addEventListener('change', loadCollections);
    $('database-collection-select').addEventListener('change', loadRecords);
    $('database-topic-select').addEventListener('change', loadRecords);
    $('database-search').addEventListener('input', () => {
        if (!db.view) return;
        renderDatabaseList();
        if (db.visible.length) selectRecord(db.visible.includes(db.currentIndex) ? db.currentIndex : db.visible[0]);
        else { db.currentIndex = null; $('database-browser').hidden = true; }
    });
    $('database-refresh').addEventListener('click', loadRecords);
    $('database-add').addEventListener('click', () => openDialog('Добавить запись', template(...dbSelected())));
    const navigate = delta => { const i = db.visible.indexOf(db.currentIndex); if (i >= 0) selectRecord(db.visible[(i + delta + db.visible.length) % db.visible.length]); };
    $('database-prev').addEventListener('click', () => navigate(-1));
    $('database-next').addEventListener('click', () => navigate(1));
    $('database-edit').addEventListener('click', () => {
        if (db.currentIndex === null) return;
        const snapshot = captureEdit(db.currentIndex);
        if (snapshot) openDialog('Редактировать запись', snapshot.expected, snapshot);
    });
    $('database-delete').addEventListener('click', async () => {
        if (db.currentIndex === null) return;
        const snapshot = captureEdit(db.currentIndex);
        if (!snapshot) return;
        if (await ask('Это действие нельзя отменить.', { title: `Удалить запись #${snapshot.index + 1}?`, ok: 'Удалить', danger: true })) {
            await change('database_delete', snapshot);
        }
    });
    $('database-cancel').addEventListener('click', () => $('database-edit-dialog').close());
    $('database-edit-form').addEventListener('submit', e => {
        e.preventDefault();
        let record;
        const snapshot = db.editing;
        if (!snapshot) return;
        try { record = JSON.parse($('database-record-json').value); }
        catch (error) { return editError(snapshot, `Некорректный JSON: ${error.message}`); }
        change(snapshot.index === null ? 'database_add' : 'database_update', snapshot, { record });
    });
    $('database-reset').addEventListener('click', async e => {
        const button = e.currentTarget;
        if (!(await ask('Пул контента и история оценок станут пустыми, сохранённые картинки удалятся. Тема поиска и общий каталог источников останутся. Это действие нельзя отменить.', { title: 'Очистить ваши данные?', ok: 'Очистить', danger: true }))) return;
        if (db.busy) return;
        db.busy = true;
        button.disabled = true;
        try {
            const d = await api('database_reset', { body: null });
            log(`Базы очищены. Поиск контента: ${['started', 'running'].includes(d.parser) ? 'запущен' : 'не запустился'}.`);
            watchParser();
            onReset?.();
            await loadRecords();
        } catch (error) {
            dbStatus(error.message, true);
        } finally {
            db.busy = false;
            button.disabled = false;
        }
    });
}
