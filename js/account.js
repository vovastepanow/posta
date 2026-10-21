// account.js — личный кабинет: профиль, соцсети (VK на весь сайт, бот и аккаунт Telegram), «Мои данные и куки», базы данных.
import { $, DEBUG_MODE, api, apiUrl, el, log, onTabOpen, pluralRu, switchTab, watchParser, safeSourcePostUrl } from './core.js';
import { openViewerGallery } from './viewer.js';

const VK_MESSAGES = { connected: 'VK подключён.', misconfigured: 'Настройте VK_CLIENT_ID и VK_REDIRECT_URI на сервере.', error: 'Не удалось подключить VK. Проверьте настройки OAuth и журнал PHP.' };
const COLLECTIONS = { liked: 'Нравится', disliked: 'Не нравится', undecided: 'Не знаю', complaints: 'Жалобы' };
const sections = {};
let current = 'profile';
let vkMessage = '';
let qrTimer = 0;

export function openAccountSection(name) {
    current = name;
    if (!$('account-tab').classList.contains('active')) return switchTab('account-tab');   // покажет текущий раздел сама
    document.querySelectorAll('[data-account-section]').forEach(b => b.classList.toggle('is-active', b.dataset.accountSection === name));
    document.querySelectorAll('.account-section').forEach(s => { s.hidden = s.dataset.section !== name; });
    sections[name]?.();
}

const formatBytes = b => (b < 1024 ? `${b} Б` : b < 1048576 ? `${(b / 1024).toFixed(1)} КБ` : `${(b / 1048576).toFixed(1)} МБ`);
const formatDate = s => (s ? new Date(s * 1000).toLocaleString('ru-RU', { day: 'numeric', month: 'long', hour: '2-digit', minute: '2-digit' }) : '—');
const files = n => `${n} ${pluralRu(n, 'файл', 'файла', 'файлов')}`;
const status = (id, text) => { $(id).textContent = text; };

/** Длинное значение скрыто до клика: начало…конец. */
function revealable(value) {
    const text = String(value);
    if (text.length <= 14) return el('code', { className: 'privacy-value', textContent: text || '(пусто)' });
    const short = `${text.slice(0, 6)}…${text.slice(-4)}`;
    const b = el('button', { type: 'button', className: 'privacy-value', textContent: short, title: 'Показать целиком' });
    b.onclick = () => { const full = b.textContent === short; b.textContent = full ? text : short; b.title = full ? 'Скрыть' : 'Показать целиком'; };
    return b;
}

const table = (headers, rows, empty) => rows.length ? el('table', { className: 'privacy-table' }, el('thead', {}, el('tr', {}, ...headers.map(h => el('th', { textContent: h })))),
    el('tbody', {}, ...rows.map(cells => el('tr', {}, ...cells.map(c => el('td', {}, c)))))) : el('p', { className: 'account-muted', textContent: empty });

/** Кнопка с занятостью на время действия; ошибка — в статус $statusId. */
async function busy(button, statusId, action) {
    if (button) button.disabled = true;
    try { return await action(); } catch (error) { status(statusId, error.message); } finally { if (button) button.disabled = false; }
}

// ---- Профиль ----

function renderProfile(info) {
    const counts = info.counts || {};
    const votes = Object.values(counts).reduce((a, b) => a + b, 0);
    const stat = (value, label) => el('div', { className: 'account-stat' }, el('strong', { textContent: String(value) }), el('span', { textContent: label }));
    const copy = el('button', { type: 'button', className: 'account-secondary', textContent: 'Скопировать ID' });
    copy.onclick = async () => {
        copy.textContent = await navigator.clipboard.writeText(info.user_id).then(() => 'Скопировано', () => 'Не удалось скопировать');
        setTimeout(() => { copy.textContent = 'Скопировать ID'; }, 1500);
    };
    const body = $('account-profile-body');
    body.classList.remove('is-loading', 'status-msg');
    body.replaceChildren(
        el('div', { className: 'account-profile-head' }, el('div', { className: 'account-avatar', textContent: info.user_id.slice(0, 2).toUpperCase(), ariaHidden: 'true' }),
            el('div', {}, el('h3', { textContent: 'Ваш профиль' }), el('p', { className: 'account-muted' }, 'ID ', revealable(info.user_id)),
                el('p', { className: 'account-muted', textContent: `Тема поиска: «${info.topic}» · последний заход: ${formatDate(info.last_seen)}` })), copy),
        el('div', { className: 'account-stats' }, stat(info.pool, `${pluralRu(info.pool, 'запись', 'записи', 'записей')} ждёт оценки`), stat(votes, pluralRu(votes, 'оценка', 'оценки', 'оценок')),
            ...Object.entries(COLLECTIONS).map(([k, label]) => stat(counts[k] ?? 0, label))),
        el('p', { className: 'account-muted', textContent: `Профиль держится на куке в этом браузере. В другом браузере сайт заведёт новый профиль. Профили без заходов дольше ${info.retention_days} дней удаляются.` }));
}

function renderExposure(info) {
    const exposed = info.exposed || [];
    $('account-exposure').hidden = !exposed.length;
    if (exposed.length) $('account-exposure').replaceChildren(el('strong', { textContent: 'Защита данных не работает' }),
        el('p', { className: 'account-muted', textContent: `Apache не читает .htaccess, поэтому по HTTP отдаются служебные файлы: ${exposed.join(', ')}. Это каталог источников, история оценок и сам репозиторий (.git) — их может скачать любой, кто видит сайт в сети. Токены ботов на диске зашифрованы. Чтобы закрыть всё, выполните один раз в терминале:` }),
        el('code', { className: 'account-command', textContent: info.fix_command }));
}

// ---- Соцсети ----

async function loadVkStatus() {
    try {
        const d = await api('vk_oauth_status');
        $('vk-oauth-button').textContent = d.connected ? 'Переподключить VK' : 'Подключить VK';
        status('vk-oauth-status', vkMessage || (!d.configured ? 'Для OAuth задайте VK_CLIENT_ID и VK_REDIRECT_URI на сервере.' : d.connected ? `VK подключён${d.user_id ? ` · ID ${d.user_id}` : ''}.` : 'Подключите VK, чтобы подбирать паблики.'));
        vkMessage = '';
    } catch (error) { status('vk-oauth-status', `Статус VK недоступен: ${error.message}`); }
}

/** После возврата с VK ID в адресе ?vk=<результат>: показать и убрать из адреса. */
export function consumeVkRedirectResult() {
    const result = new URLSearchParams(location.search).get('vk');
    if (!result) return false;
    const url = new URL(location.href);
    url.searchParams.delete('vk');
    url.hash = '';
    history.replaceState({}, '', url);
    status('vk-oauth-status', vkMessage = VK_MESSAGES[result] || VK_MESSAGES.error);
    return true;
}

function renderTelegram(t) {
    $('telegram-connected').hidden = !t;
    $('telegram-form').hidden = Boolean(t);
    if (!t) return;
    const channel = t.chat_username ? `@${t.chat_username}` : t.chat_title;
    $('telegram-summary').replaceChildren(el('strong', { textContent: `Подключён канал «${t.chat_title || channel}»` }),
        el('span', { className: 'account-muted', textContent: `${channel ? `${channel} · ` : ''}бот @${t.bot} · токен ${t.token} · с ${formatDate(t.connected_at)}` }));
}

async function loadTelegramQueue() {
    const data = await api('telegram_queue').catch(() => null);
    const box = $('telegram-queue');
    const [scheduled, published] = [data?.scheduled || [], data?.published || []];
    box.hidden = !data?.connected || (!scheduled.length && !published.length);
    if (box.hidden) return;
    const row = (when, entry, extra, action = null) => el('div', { className: 'queue-row' }, el('div', { className: 'queue-main' }, el('strong', { textContent: formatDate(when) }),
        el('span', { textContent: `${files(entry.media)}${entry.text ? ' · ' + entry.text : ''}` }), extra), action);
    box.replaceChildren(
        ...(scheduled.length ? [el('h4', { textContent: `Запланировано: ${scheduled.length}` }), ...scheduled.map(p => {
            const text = { telegram: 'в «Отложенных» Telegram — уйдёт само', failed: `не отправлено (${p.error})`, sending: 'отправляется…' }[p.status] || (p.error ? `будет повтор ${formatDate(p.publish_at)}: ${p.error}` : 'ждёт времени');
            const cancel = el('button', { type: 'button', className: 'account-secondary', textContent: p.status === 'sending' ? '…' : 'Отменить', disabled: p.status === 'sending' });
            cancel.onclick = async () => {
                cancel.disabled = true;
                await api('telegram_cancel', { body: { id: p.id } }).then(() => log('Отложенный пост отменён.'), e => log(`Не удалось отменить пост: ${e.message}`));
                loadTelegramQueue();
            };
            return row(p.publish_at, p, el('span', { className: `account-muted${p.status === 'failed' || p.error ? ' queue-error' : ''}`, textContent: text }), cancel);
        })] : []),
        ...(published.length ? [el('h4', { textContent: 'Последние отправленные' }), ...published.map(e => row(e.at, e, e.status !== 'ok' ? el('span', { className: 'account-muted queue-error', textContent: `не отправлено: ${e.error}` })
            : e.url ? el('a', { href: e.url, target: '_blank', rel: 'noopener noreferrer', textContent: 'Открыть в Telegram ↗' }) : el('span', { className: 'account-muted', textContent: 'отправлено' })))] : []));
}

/** Блок «Отложенные посты средствами Telegram»: ключи (если не сохранены), телефон, код, пароль, QR. */
function renderTelegramUser(info, botConnected = true) {
    $('tgu-card').hidden = !botConnected;
    if (!botConnected) return;
    const keys = Boolean(info?.app_keys);
    $('tgu-keys').hidden = $('tgu-step-keys').hidden = keys;
    $('tgu-api-id').required = $('tgu-api-hash').required = !keys;
    const [connected, awaiting] = [Boolean(info?.connected), !info?.connected && Boolean(info?.awaiting_code)];
    $('tgu-connected').hidden = !connected;
    $('tgu-start').hidden = connected || awaiting || !$('tgu-qr').hidden;
    $('tgu-confirm').hidden = !awaiting;
    if (connected) {
        $('tgu-summary').replaceChildren(el('strong', { textContent: `Аккаунт ${info.name || ''}${info.username ? ' @' + info.username : ''} подключён` }),
            el('span', { className: 'account-muted', textContent: `с ${formatDate(info.connected_at)} · отложенные посты ставятся в «Отложенные» канала` }),
            info.warning ? el('span', { className: 'queue-error', textContent: info.warning }) : null);
    } else if (awaiting) {
        $('tgu-code').closest('label').hidden = info.qr;
        if (info.qr) { $('tgu-password-row').hidden = false; $('tgu-resend').hidden = true; return status('tgu-status', 'Вход по QR принят. В аккаунте включена двухэтапная защита: введите пароль.'); }
        const where = { app: 'в приложение Telegram: ищите сообщение от «Telegram» (служебные уведомления) в чатах на телефоне или компьютере, где вы уже вошли', sms: 'по SMS',
            call: 'голосовым звонком', flashcall: 'звонком (последние цифры номера — код)', missedcall: 'звонком (последние цифры номера — код)' }[info.code_type] || 'способом, который выбрал Telegram';
        status('tgu-status', `Код отправлен на ${info.phone} ${where}.`);
        $('tgu-resend').hidden = !info.next_type;
        $('tgu-resend').textContent = { sms: 'Отправить код по SMS', call: 'Позвонить с кодом', flashcall: 'Код звонком', missedcall: 'Код звонком' }[info.next_type] || 'Запросить код другим способом';
    }
}

/** «8 (999) 123-45-67», «9991234567» → «+7 999 123-45-67»; номера других стран с «+» — без лишних знаков. */
export function normalizePhone(value) {
    const raw = String(value || '').trim();
    const d = raw.replace(/\D/g, '');
    if (!d) return raw;
    if (raw.startsWith('+') && !d.startsWith('7')) return `+${d}`;
    const n = d.length === 11 && '78'.includes(d[0]) ? d.slice(1) : d.length === 10 ? d : null;
    return n ? `+7 ${n.slice(0, 3)} ${n.slice(3, 6)}-${n.slice(6, 8)}-${n.slice(8)}` : raw.startsWith('+') ? `+${d}` : raw;
}

function stopQr() { clearInterval(qrTimer); $('tgu-qr').hidden = true; }

/** QR-вход: код на экране, раз в 2 с спрашиваем сервер, отсканировали ли его. */
async function startQr() {
    status('tgu-status', 'Готовлю QR-код…');
    try { await api('telegram_user_qr_start', { body: { api_id: $('tgu-api-id').value, api_hash: $('tgu-api-hash').value } }); } catch (error) { return status('tgu-status', error.message); }
    $('tgu-start').hidden = true;
    $('tgu-qr').hidden = false;
    $('tgu-qr-image').replaceChildren();
    status('tgu-status', 'Жду, пока вы отсканируете код в приложении Telegram…');
    let polling = false;
    clearInterval(qrTimer);
    qrTimer = setInterval(async () => {
        if (polling) return;
        polling = true;
        const d = await api('telegram_user_qr_status').catch(() => null).finally(() => { polling = false; });
        if (!d || d.status === 'busy') return;
        if (d.status === 'waiting') return d.svg && $('tgu-qr-image').replaceChildren(el('img', { src: `data:image/svg+xml;charset=utf-8,${encodeURIComponent(d.svg)}`, alt: 'QR-код', width: 220, height: 220 }));
        stopQr();
        if (d.status === 'success') { status('tgu-status', ''); renderTelegramUser(d.telegram_user); log('Telegram: аккаунт подключён по QR-коду, отложенные посты теперь ставятся в «Отложенные» канала.'); }
        else if (d.status === 'needs_password') loadAccount();
        else {
            $('tgu-start').hidden = false;
            status('tgu-status', { expired: 'QR-код устарел: запросите новый.', none: 'Вход по QR прервался: запросите код ещё раз.' }[d.status] || d.error || 'Не удалось войти по QR-коду.');
            log(`Telegram, вход по QR: ${d.status === 'error' ? d.error : d.status}`);
        }
    }, 2000);
}

function initializeTelegram() {
    $('telegram-form').addEventListener('submit', e => { e.preventDefault(); busy($('telegram-connect'), 'telegram-status', async () => {
        status('telegram-status', 'Проверяю бота и канал в Telegram…');
        const d = await api('telegram_connect', { body: { token: $('telegram-token').value, channel: $('telegram-channel').value } });
        $('telegram-token').value = '';
        status('telegram-status', '');
        renderTelegram(d.telegram);
        loadAccount();
        log(`Telegram: подключён канал «${d.telegram.chat_title}».`);
    }); });
    $('telegram-disconnect').addEventListener('click', e => {   // подтверждение в самой кнопке: первое нажатие спрашивает
        const b = e.currentTarget;
        const reset = () => { b.dataset.armed = ''; b.textContent = 'Отключить'; };
        if (b.dataset.armed !== '1') { b.dataset.armed = '1'; b.textContent = 'Точно отключить?'; return setTimeout(reset, 4000); }
        busy(b, 'telegram-status', async () => { await api('telegram_disconnect', { body: null }); renderTelegram(null); log('Telegram отключён.'); }).finally(reset);
    });
    const phone = $('tgu-phone');
    phone.addEventListener('blur', () => { phone.value = normalizePhone(phone.value); });
    phone.addEventListener('paste', () => setTimeout(() => { phone.value = normalizePhone(phone.value); }));
    $('tgu-start').addEventListener('submit', e => { e.preventDefault(); phone.value = normalizePhone(phone.value); busy(e.submitter, 'tgu-status', async () => {
        status('tgu-status', 'Запрашиваю код у Telegram…');
        const d = await api('telegram_user_start', { body: { api_id: $('tgu-api-id').value, api_hash: $('tgu-api-hash').value, phone: phone.value } });
        $('tgu-api-id').value = $('tgu-api-hash').value = '';
        $('tgu-password-row').hidden = true;
        renderTelegramUser(d.telegram_user);
    }); });
    $('tgu-confirm').addEventListener('submit', e => { e.preventDefault(); busy(e.submitter, 'tgu-status', async () => {
        status('tgu-status', 'Проверяю код и права в канале…');
        const d = await api('telegram_user_confirm', { body: { code: $('tgu-code').value, password: $('tgu-password').value } });
        if (d.status === 'needs_password') { $('tgu-password-row').hidden = false; $('tgu-password').focus(); return status('tgu-status', 'В аккаунте включена двухэтапная защита: введите пароль.'); }
        $('tgu-code').value = $('tgu-password').value = '';
        status('tgu-status', '');
        renderTelegramUser(d.telegram_user);
        log('Telegram: аккаунт подключён, отложенные посты теперь ставятся в «Отложенные» канала.');
    }); });
    $('tgu-resend').addEventListener('click', e => busy(e.currentTarget, 'tgu-status', async () => {
        status('tgu-status', 'Запрашиваю код другим способом…');
        renderTelegramUser((await api('telegram_user_resend', { body: null })).telegram_user);
    }));
    $('tgu-disconnect').addEventListener('click', () => busy(null, 'tgu-status', async () => {
        renderTelegramUser((await api('telegram_user_disconnect', { body: null })).telegram_user);
        status('tgu-status', 'Отключено. Чтобы завершить вход совсем, закройте сессию «Postmaker» в Telegram → Настройки → Устройства.');
        log('Аккаунт Telegram отключён.');
    }));
    $('tgu-restart').addEventListener('click', () => busy(null, 'tgu-status', async () => { renderTelegramUser((await api('telegram_user_disconnect', { body: null })).telegram_user); status('tgu-status', ''); }));
    $('tgu-qr-button').addEventListener('click', startQr);
    $('tgu-qr-cancel').addEventListener('click', () => { stopQr(); $('tgu-start').hidden = false; status('tgu-status', ''); });
}

// ---- Мои данные и куки ----

function renderPrivacy(info) {
    const visible = Object.fromEntries(document.cookie.split(';').map(p => p.trim()).filter(Boolean).map(p => { const i = p.indexOf('='); return [decodeURIComponent(p.slice(0, i)), decodeURIComponent(p.slice(i + 1))]; }));
    const cookies = info.cookies.map(c => [el('code', { textContent: c.name }), revealable(c.value), c.lifetime,
        c.http_only ? el('span', { className: 'privacy-badge', textContent: 'HttpOnly', title: 'Скрипты страницы её не видят — только сервер' }) : 'видна скриптам', c.purpose]);
    for (const [name, value] of Object.entries(visible)) if (!info.cookies.some(c => c.name === name)) cookies.push([el('code', { textContent: name }), revealable(value), '—', 'видна скриптам', 'Есть в браузере, но сервер её не получает.']);
    $('privacy-cookies').replaceChildren(table(['Кука', 'Значение', 'Срок', 'Доступ', 'Зачем'], cookies, 'Браузер не отправляет этому сайту ни одной куки.'));
    renderStorage();
    const total = info.files.reduce((a, f) => a + f.size, 0);
    $('privacy-server-note').textContent = `Папка вашего профиля: ${files(info.files.length)}, ${formatBytes(total)}.`
        + (info.session_keys.length ? ` В сессии PHP: ${info.session_keys.join(', ')}.` : ' Сессии PHP нет.') + ' Общие для всех данные (каталог источников, кэш картинок и анализов) к профилю не привязаны.';
    $('privacy-files').replaceChildren(table(['Файл', 'Размер', 'Изменён', 'Что внутри'], info.files.map(f => [el('code', { textContent: f.name }), formatBytes(f.size), formatDate(f.modified), f.about]), 'Файлов нет.'));
}

function renderStorage() {
    const rows = [];
    for (const [label, storage] of [['localStorage', localStorage], ['sessionStorage', sessionStorage]]) {
        let keys = [];
        try { keys = Object.keys(storage); } catch { continue; }   // хранилище запрещено настройками браузера
        for (const key of keys) {
            const value = storage.getItem(key) ?? '';
            rows.push([label, el('code', { textContent: key }), revealable(value), formatBytes(new Blob([value]).size),
                el('button', { type: 'button', className: 'account-secondary', textContent: 'Удалить', onclick: () => { storage.removeItem(key); renderStorage(); } })]);
        }
    }
    $('privacy-storage').replaceChildren(table(['Где', 'Ключ', 'Значение', 'Размер', ''], rows, 'Пусто: сайт ничего не хранит в localStorage и sessionStorage.'));
}

async function loadAccount() {
    if (DEBUG_MODE) return void ($('account-profile-body').textContent = 'В демо-режиме личного кабинета нет: данные живут только в сессии демо.');
    try {
        const info = await api('account_info');
        renderProfile(info);
        renderTelegram(info.telegram);
        renderTelegramUser(info.telegram_user, Boolean(info.telegram));
        renderExposure(info);
        if (info.telegram) loadTelegramQueue();
        renderPrivacy(info);
    } catch (error) {
        $('account-profile-body').classList.remove('is-loading', 'status-msg');
        $('account-profile-body').textContent = `Не удалось загрузить кабинет: ${error.message}`;
    }
}

// ---- Базы данных: записи JSON-файлов, правка с проверкой «запись не изменилась» на сервере ----

const db = { databases: [], ready: null, items: [], columns: [], editing: null, currentIndex: null, database: null, collection: null, isSqlTable: false };
const dbSelected = () => [$('database-select').value, $('database-collection-select').value];
const dbStatus = (text, error = false) => { $('database-status').textContent = text; $('database-status').classList.toggle('error-msg', error); };
const option = ({ id, label }) => el('option', { value: id, textContent: label });

function loadDatabaseOptions() {
    return db.ready ??= api('database_options').then(async d => {
        db.databases = d.databases || [];
        $('database-select').replaceChildren(...db.databases.map(option));
        await loadCollections();
    }).catch(error => { db.ready = null; dbStatus(`Не удалось загрузить базы: ${error.message}`, true); });
}

async function loadCollections() {
    db.currentIndex = null;
    $('database-collection-select').replaceChildren(...(db.databases.find(d => d.id === $('database-select').value)?.collections || []).map(option));
    await loadRecords();
}


async function loadRecords() {
    const [database, collection] = dbSelected();
    if (!database || !collection) return;
    db.database = database;
    db.collection = collection;
    db.isSqlTable = false;
    dbStatus('Загружаю записи...');
    try {
        const d = await api('database_read', { params: { database, collection } });
        db.items = d.items || [];
        db.columns = d.columns || [];
        db.mediaMissing = new Set(d.media_missing || []);
        db.databaseLabel = d.database_label;
        db.collectionLabel = d.collection_label;
        // Проверяем, является ли это SQL таблицей
        db.isSqlTable = db.databases.find(d => d.id === database)?.is_sql_table || false;
        dbStatus('');
    } catch (error) { db.items = []; db.columns = []; dbStatus(`Не удалось прочитать коллекцию: ${error.message}`, true); }

    if (!db.items.length) {
        $('database-status').textContent = 'В этой коллекции пока нет записей.';
        $('database-records').replaceChildren(el('div', { className: 'database-empty', textContent: 'В этой коллекции пока нет записей.' }));
        $('database-browser').hidden = true;
        $('database-list').replaceChildren();
        return;
    }

    renderDatabaseList();
    if (db.currentIndex === null) selectRecord(0);
}

function renderDatabaseList() {
    const items = db.items.map((record, idx) => {
        const label = record && typeof record === 'object' ? (record.text ? record.text.slice(0, 50) : record.name || record.title || `Запись #${idx + 1}`) : String(record).slice(0, 50);
        const btn = el('button', { type: 'button', className: 'database-list-item', textContent: `#${idx + 1}: ${label}` });
        btn.addEventListener('click', () => selectRecord(idx));
        return btn;
    });
    $('database-list').replaceChildren(...items);
    updateListSelection();
}

function updateListSelection() {
    document.querySelectorAll('.database-list-item').forEach((btn, idx) => {
        btn.classList.toggle('active', idx === db.currentIndex);
    });
}

function selectRecord(index) {
    if (index < 0 || index >= db.items.length) return;
    db.currentIndex = index;
    updateListSelection();
    renderDatabaseBrowser();
}

function renderDatabaseBrowser() {
    const idx = db.currentIndex;
    const record = db.items[idx];

    $('database-browser').hidden = false;
    $('database-records').replaceChildren();

    // Хлебные крошки
    $('database-breadcrumb').replaceChildren(
        el('span', { textContent: db.databaseLabel }),
        el('span', { className: 'separator', textContent: '›' }),
        el('span', { textContent: db.collectionLabel }),
        el('span', { className: 'separator', textContent: '›' }),
        el('span', { textContent: `Запись #${idx + 1} / ${db.items.length}` })
    );

    // Кнопки навигации
    $('database-prev').disabled = idx <= 0;
    $('database-next').disabled = idx >= db.items.length - 1;

    // Отключаем редактирование для SQL таблиц
    $('database-edit').disabled = db.isSqlTable;
    $('database-delete').disabled = db.isSqlTable;
    if (db.isSqlTable) {
        $('database-edit').title = 'Редактирование системных таблиц недопустимо';
        $('database-delete').title = 'Удаление из системных таблиц недопустимо';
    } else {
        $('database-edit').title = '';
        $('database-delete').title = '';
    }

    // Представление записи
    renderRecordDetails(record, idx);
}

function renderRecordDetails(record, index) {
    const isMedia = db.mediaMissing.has(index);
    const media = !isMedia ? dbMediaItem(record) : null;
    const isObject = record && typeof record === 'object' && !Array.isArray(record);

    // Главный контейнер с улучшенным стилем
    const container = el('div', { className: 'database-record-item-improved' });

    // Верхняя часть: медиа и основная информация рядом
    const topSection = el('div', { className: 'database-record-top' });

    // Медиа в красивом контейнере (займёт место слева)
    if (media) {
        const mediaEl = media.kind === 'image'
            ? el('img', { src: media.src, alt: media.alt || '', className: 'database-record-item-media-large' })
            : el('video', { src: media.src, className: 'database-record-item-media-large', controls: true });

        const mediaWrapper = el('div', { className: 'database-record-item-media-wrapper-large' }, mediaEl);

        // Клик на медиа для открытия просмотрщика
        mediaWrapper.addEventListener('click', () => {
            const items = [{
                kind: media.kind,
                src: media.src,
                fallback: media.src,
                poster: media.kind === 'video' ? (record.image || '') : ''
            }];
            openViewerGallery(items, 0);
        });

        topSection.appendChild(mediaWrapper);
    } else if (isMedia) {
        // Показываем плейсхолдер если нет медиа
        const placeholder = el('div', { className: 'database-record-no-media' }, el('span', { textContent: 'Нет медиа' }));
        topSection.appendChild(placeholder);
    }

    // Основная информация рядом с медиа
    const infoSection = el('div', { className: 'database-record-info-section' });

    // Заголовок записи
    if (isObject) {
        const title = record.title || record.name || record.text;
        if (title) {
            const titleEl = el('h3', { className: 'database-record-title', textContent: String(title).slice(0, 100) });
            infoSection.appendChild(titleEl);
        }
    }

    // Приоритетные поля показываем красиво в строк
    const priorityFields = ['description', 'why_relevant', 'source', 'topic', 'tags', 'relevance', 'rating'];
    const priorityInfo = el('div', { className: 'database-record-priority-fields' });

    for (const fieldName of priorityFields) {
        if (isObject && fieldName in record && record[fieldName]) {
            const value = record[fieldName];
            const fieldEl = renderPriorityField(fieldName, value);
            if (fieldEl) priorityInfo.appendChild(fieldEl);
        }
    }

    if (priorityInfo.children.length > 0) {
        infoSection.appendChild(priorityInfo);
    }

    topSection.appendChild(infoSection);
    container.appendChild(topSection);

    // Нижняя часть: остальные поля в аккордеон или скрытом блоке
    if (isObject) {
        const fields = Object.entries(record);
        const technicalFields = [];

        for (const [key, value] of fields) {
            if (key === 'image_local' || key === 'image' || key === 'video_local' || key === 'video') continue;
            if (priorityFields.includes(key) && value) continue;
            technicalFields.push([key, value]);
        }

        if (technicalFields.length > 0) {
            const expandable = el('details', { className: 'database-record-expandable' },
                el('summary', { textContent: '📋 Все поля' }),
                el('div', { className: 'database-record-all-fields' },
                    ...technicalFields.map(([key, value]) => renderRecordField(key, value))
                )
            );
            container.appendChild(expandable);
        }
    }

    $('database-record-view').replaceChildren(container);
}

function renderPriorityField(key, value) {
    if (value === null || value === undefined || value === '') return null;

    let displayValue;
    const labels = {
        description: '📝',
        why_relevant: '💡',
        source: '📍',
        topic: '🏷',
        tags: '🔖',
        relevance: '📊',
        rating: '⭐'
    };

    if (Array.isArray(value)) {
        displayValue = el('span', { className: 'database-priority-value', textContent: value.join(', ') });
    } else if (typeof value === 'object') {
        displayValue = el('span', { className: 'database-priority-value', textContent: JSON.stringify(value) });
    } else {
        const text = String(value);
        if (/^https?:\/\//.test(text)) {
            displayValue = el('a', { href: text, target: '_blank', rel: 'noopener noreferrer', className: 'database-priority-value database-priority-link', textContent: text.slice(0, 50) + (text.length > 50 ? '…' : '') });
        } else {
            displayValue = el('span', { className: 'database-priority-value', textContent: text.slice(0, 150) });
        }
    }

    return el('div', { className: 'database-priority-field' },
        el('span', { className: 'database-priority-label', textContent: labels[key] || '•' }),
        el('div', { className: 'database-priority-content' },
            el('strong', { textContent: formatFieldLabel(key) }),
            displayValue
        )
    );
}

function formatFieldLabel(key) {
    const labels = {
        description: 'Описание',
        why_relevant: 'Почему релевантно',
        source: 'Источник',
        topic: 'Тема',
        tags: 'Метки',
        relevance: 'Релевантность',
        rating: 'Рейтинг'
    };
    return labels[key] || key;
}

function renderRecordField(key, value) {
    let displayValue;
    if (value === null || value === undefined || value === '') {
        displayValue = el('span', { className: 'database-record-field-value', textContent: '(пусто)' });
    } else if (typeof value === 'boolean') {
        displayValue = el('span', { className: 'database-record-field-value', textContent: value ? 'да' : 'нет' });
    } else if (DB_TIME_FIELDS.includes(key) && Number(value) > 0) {
        displayValue = el('span', { className: 'database-record-field-value', textContent: new Date(Number(value) * 1000).toLocaleString('ru-RU') });
    } else if (typeof value === 'object') {
        displayValue = el('code', { className: 'database-record-field-value code', textContent: JSON.stringify(value, null, 2) });
    } else {
        const text = String(value);
        const isUrl = /^https?:\/\//.test(text);
        if (isUrl && text.length > DB_TEXT_LIMIT) {
            displayValue = el('a', { href: text, target: '_blank', rel: 'noopener noreferrer', className: 'database-record-field-value', title: text, textContent: `${text.slice(0, DB_TEXT_LIMIT - 1)}…` });
        } else if (isUrl) {
            displayValue = el('a', { href: text, target: '_blank', rel: 'noopener noreferrer', className: 'database-record-field-value', textContent: text });
        } else if (text.length > DB_TEXT_LIMIT) {
            displayValue = el('span', { className: 'database-record-field-value', textContent: `${text.slice(0, DB_TEXT_LIMIT - 1)}…`, title: text });
        } else {
            displayValue = el('span', { className: 'database-record-field-value', textContent: text });
        }
    }

    return el('div', { className: 'database-record-field' },
        el('div', { className: 'database-record-field-label', textContent: key }),
        displayValue
    );
}

const DB_TIME_FIELDS = ['posted_at', 'timestamp', 'pooled_at', 'created_at', 'at'];
/** Поля, которые помещаются в таблицу. Полная запись — в «Ред.» (там JSON со всеми полями). Порядок — как в базе. */
const DB_TABLE_FIELDS = ['image_local', 'topic', 'tags', 'source', 'text', 'description', 'relevance', 'rating', 'rating_assigned', 'posted_at', 'pooled_at', 'timestamp'];
const DB_TEXT_LIMIT = 200;

/** Ячейка: значение целиком, длинный текст сокращается с многоточием; полный текст — в подсказке. */
function dbCell(key, value) {
    if (value === null || value === undefined || value === '') return el('span', { className: 'db-empty-cell', textContent: '—' });
    if (typeof value === 'boolean') return value ? 'да' : 'нет';
    if (Array.isArray(value) && value.every(v => typeof v === 'string')) return value.length ? value.join(', ') : el('span', { className: 'db-empty-cell', textContent: '—' });
    if (DB_TIME_FIELDS.includes(key)) return Number(value) > 0 ? el('span', { title: new Date(Number(value) * 1000).toLocaleString('ru-RU'), textContent: new Date(Number(value) * 1000).toLocaleString('ru-RU') }) : el('span', { className: 'db-empty-cell', textContent: '—' });
    const text = typeof value === 'object' ? JSON.stringify(value) : String(value);
    const short = text.length > DB_TEXT_LIMIT ? `${text.slice(0, DB_TEXT_LIMIT - 1)}…` : text;
    if (/^https:\/\//.test(text)) return el('a', { className: 'db-link', href: text, target: '_blank', rel: 'noopener noreferrer', title: text, textContent: short });
    return el('span', { title: text, textContent: short });
}

/** Запись как элемент единого медиапросмотрщика (видео — с постером, картинка — локальная копия или исходная ссылка). */
function dbMediaItem(r) {
    if (r === null || typeof r !== 'object') return null;
    const sourceUrl = safeSourcePostUrl(r) || '';
    const video = r.video_local || r.video;
    if (video) return { kind: 'video', src: r.video_local || r.video, poster: r.image_local || '', fallback: r.video || '', sourceUrl, alt: r.text || '' };
    const image = r.image_local || r.image;
    return image ? { kind: 'image', src: image, fallback: r.image || '', sourceUrl, alt: r.text || '' } : null;
}

/** Таблица коллекции: видимые поля в порядке базы, миниатюра открывает общий просмотрщик, в конце — действия. */
function recordsTable(items, columns, missing = new Set()) {
    const shown = columns.filter(c => DB_TABLE_FIELDS.includes(c) && c !== 'image_local');
    const media = items.map((r, i) => ({ i, item: missing.has(i) ? null : dbMediaItem(r) })).filter(x => x.item);
    const openMedia = i => openViewerGallery(media.map(x => x.item), Math.max(0, media.findIndex(x => x.i === i)));
    const button = (className, textContent, onclick) => el('button', { type: 'button', className, textContent, onclick });
    const head = el('tr', {}, el('th', { className: 'db-num', textContent: '#' }), el('th', { className: 'db-picture', textContent: 'image_local' }),
        ...shown.map(c => el('th', { textContent: c })), el('th', { className: 'db-actions-col', textContent: 'действия' }));
    const rows = items.map((r, i) => {
        const object = r !== null && typeof r === 'object' && !Array.isArray(r) ? r : { value: r };
        const link = safeSourcePostUrl(object);
        const picture = missing.has(i) ? el('span', { className: 'db-no-file', textContent: 'нет файла', title: 'Локальная копия удалена очисткой раньше. Исходная ссылка может быть недоступна.' })
            : dbMediaItem(object)
            ? el('img', { className: 'db-thumb', src: object.image_local || object.image, alt: '', loading: 'lazy', title: 'Открыть просмотр', onclick: e => { e.stopPropagation(); openMedia(i); },   // глобальный обработчик просмотрщика не должен открыть картинку вместо видео
                // локальной копии нет (удалена раньше) — показываем исходную ссылку
                onerror: e => { if (object.image && e.currentTarget.getAttribute('src') !== object.image) e.currentTarget.src = object.image; } })
            : el('span', { className: 'db-empty-cell', textContent: '—' });
        return el('tr', {}, el('td', { className: 'db-num', textContent: String(i + 1) }), el('td', { className: 'db-picture' }, picture),
            ...shown.map(c => el('td', {}, dbCell(c, object[c]))),
            el('td', { className: 'db-actions' }, button('database-edit-button', 'Ред.', () => openDialog('Редактировать запись', r, i)),
                button('database-delete-button', 'Удалить', () => confirm(`Удалить запись #${i + 1}? Это действие нельзя отменить.`) && change('database_delete', { index: i, expected: r })),
                link ? el('a', { className: 'database-source-link', href: link, target: '_blank', rel: 'noopener noreferrer', textContent: 'Пост ↗' }) : null));
    });
    return el('div', { className: 'db-table-wrap' }, el('table', { className: 'db-table' }, el('thead', {}, head), el('tbody', {}, ...rows)));
}

/** Заготовка новой записи под коллекцию. */
function template(database, collection) {
    const now = Math.floor(Date.now() / 1000);
    return database === 'content_pool' ? { image: '', text: '', description: '', why_relevant: '', relevance: 0, rating: 0, source: '', board: '', thread_id: '', post_id: '', source_url: '' }
        : database === 'content_history' ? { image: '', text: '', description: '', rating_assigned: 0, source: '', timestamp: now }
        : { '2ch_boards': 'b', vk_publics: { id: '', name: '' }, telegram_channels: { id: '', name: '' }, source_votes: { source_id: '', platform: '', name: '', vote: 'approve', timestamp: now } }[collection] ?? {};
}

function openDialog(title, record, index = null) {
    db.editing = index;
    $('database-dialog-title').textContent = title;
    $('database-record-json').value = JSON.stringify(record, null, 2);
    $('database-edit-dialog').showModal();
}

async function change(action, extra) {
    const [database, collection] = dbSelected();
    try { await api(action, { body: { database, collection, ...extra } }); $('database-edit-dialog').close(); db.currentIndex = null; await loadRecords(); return true; }
    catch (error) { dbStatus(`Не удалось ${action === 'database_delete' ? 'удалить' : 'сохранить'} запись: ${error.message}`, true); return false; }
}

/** «Добавить вручную» на пустом пуле: редактор с заготовкой записи пула. */
export async function openNewItemEditor() {
    if (DEBUG_MODE) return;
    openAccountSection('databases');
    await loadDatabaseOptions();
    $('database-select').value = 'content_pool';
    await loadCollections();
    openDialog('Добавить запись', template('content_pool', 'items'));
}

function initializeDatabases(onReset) {
    $('database-select').addEventListener('change', loadCollections);
    $('database-collection-select').addEventListener('change', loadRecords);
    $('database-refresh').addEventListener('click', loadRecords);
    $('database-add').addEventListener('click', () => openDialog('Добавить запись', template(...dbSelected())));
    $('database-prev').addEventListener('click', () => selectRecord(db.currentIndex - 1));
    $('database-next').addEventListener('click', () => selectRecord(db.currentIndex + 1));
    $('database-edit').addEventListener('click', () => {
        if (db.currentIndex !== null) openDialog('Редактировать запись', db.items[db.currentIndex], db.currentIndex);
    });
    $('database-delete').addEventListener('click', () => {
        if (db.currentIndex !== null) {
            const idx = db.currentIndex;
            if (confirm(`Удалить запись #${idx + 1}? Это действие нельзя отменить.`)) {
                change('database_delete', { index: idx, expected: db.items[idx] });
            }
        }
    });
    $('database-cancel').addEventListener('click', () => $('database-edit-dialog').close());
    $('database-edit-form').addEventListener('submit', e => {
        e.preventDefault();
        let record;
        try { record = JSON.parse($('database-record-json').value); } catch (error) { return dbStatus(`Некорректный JSON: ${error.message}`, true); }
        db.editing === null ? change('database_add', { record }) : change('database_update', { record, index: db.editing, expected: db.items[db.editing] });
    });
    $('database-reset').addEventListener('click', e => {
        if (!confirm('Очистить ваши данные? Пул контента и история оценок станут пустыми, сохранённые картинки удалятся. Тема поиска и общий каталог источников останутся. Это действие нельзя отменить.')) return;
        busy(e.currentTarget, 'database-status', async () => {
            const d = await api('database_reset', { body: null });
            log(`Базы очищены. Поиск контента: ${['started', 'running'].includes(d.parser) ? 'запущен' : 'не запустился'}.`);
            watchParser();
            onReset?.();
            db.currentIndex = null;
            await loadRecords();
        });
    });
}

export function initializeAccountTab({ onDatabasesReset } = {}) {
    document.querySelectorAll('[data-account-section]').forEach(b => b.addEventListener('click', () => openAccountSection(b.dataset.accountSection)));
    $('vk-oauth-button').addEventListener('click', () => location.assign(apiUrl('vk_oauth_start')));
    initializeTelegram();
    Object.assign(sections, { profile: loadAccount, privacy: loadAccount, social: () => { loadVkStatus(); loadAccount(); }, databases: loadDatabaseOptions });
    onTabOpen('account-tab', () => openAccountSection(current));
    if (DEBUG_MODE) document.querySelector('[data-account-section="databases"]').hidden = true;
    else initializeDatabases(onDatabasesReset);
}
