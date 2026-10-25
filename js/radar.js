// radar.js — вкладка «Радар» (только владелец). Всё из Telegram (тексты, имена, названия чатов) — только через textContent.
import { $, api, el } from './core.js';
import { adminSections, initializeAdminTab } from './account.js';
import { initializeAdminControl } from './admin-control.js';

const STATUSES = { new: 'Новые', interested: 'Интересно', replied: 'Ответил', spam: 'Спам', ignored: 'Пропустить' };
const state = { settings: null, status: 'new', offset: 0, stats: null, found: [], poll: null, auto: null };

const get = (op, params = {}) => api('radar', { params: { op, ...params } });
const post = (op, body = {}) => api('radar_do', { body: { op, ...body } });
const isOpen = () => $('admin-tab').classList.contains('active') && !document.querySelector('[data-admin-section="radar"]').hidden;

function flash(message) { $('radar-flash').textContent = message || ''; }
async function guard(task) {
    try { flash(''); return await task(); } catch (error) { flash(error.message); }
}

// ---- Аккаунт и настройки ----

async function loadAll() {
    await loadAccounts();
    await loadSettings();
    if (!state.settings.account_uid) return;
    await Promise.all([loadStats(), loadLeads(true), loadRejected(), refreshLog()]);
}

async function loadAccounts() {
    const { accounts } = await get('accounts');
    $('radar-account').replaceChildren(...accounts.map(a => el('option', { value: a.uid, selected: a.selected,
        textContent: a.short + (a.created_at ? ` · с ${new Date(a.created_at * 1000).toLocaleDateString('ru-RU')}` : '') })));
    $('radar-account-empty').hidden = accounts.length > 0;
    $('radar-account-body').hidden = accounts.length === 0;
}

async function loadSettings() {
    const s = state.settings = await get('settings');
    $('radar-enabled').checked = s.enabled;
    if (s.account_uid) $('radar-account').value = s.account_uid;
    const parts = [s.last_run ? `Последняя проверка: ${new Date(s.last_run * 1000).toLocaleString('ru-RU')}, просмотрено сообщений: ${s.last_seen}` : 'Проверок ещё не было.'];
    if (s.last_error) parts.push(`Ошибка: ${s.last_error}`);
    $('radar-state').textContent = parts.join(' · ');
    if (!$('radar-queries').value) $('radar-queries').value = s.queries.join('\n');
    renderChats();
}

function saveSettings() {
    return post('settings_save', { enabled: $('radar-enabled').checked, queries: $('radar-queries').value.split('\n'),
        chats: state.settings.chats.map(c => ({ ref: c.ref, members: c.members })) });
}

// ---- Чаты ----

function renderChats() {
    const chats = state.settings.chats;
    $('radar-chats-empty').hidden = chats.length > 0;
    $('radar-chats').replaceChildren(...chats.map(chat => {
        const remove = el('button', { type: 'button', className: 'account-secondary', title: 'Убрать из радара', textContent: '✕' });
        remove.addEventListener('click', () => guard(async () => {
            state.settings.chats = state.settings.chats.filter(c => c.ref !== chat.ref);
            await saveSettings();
            await loadSettings();
        }));
        const sub = [chat.ref, chat.members ? `${chat.members} участников` : ''].filter(Boolean).join(' · ');
        return el('li', {}, el('span', { className: 'radar-grow' }, chat.title || chat.ref, el('span', { className: 'radar-sub', textContent: sub }),
            chat.error ? el('span', { className: 'radar-sub radar-bad', textContent: chat.error }) : ''), remove);
    }));
}

async function addChats(items) {
    const have = new Set(state.settings.chats.map(c => c.ref.toLowerCase()));
    for (const item of items) if (!have.has(item.ref.toLowerCase())) state.settings.chats.push({ ref: item.ref, title: item.title || '', members: item.members || 0, error: null });
    await saveSettings();
    await loadSettings();
}

function renderFound() {
    $('radar-found').replaceChildren(...state.found.map((chat, i) => el('li', {},
        el('input', { type: 'checkbox', value: String(i), id: `radar-found-${i}` }),
        el('label', { className: 'radar-grow', htmlFor: `radar-found-${i}` }, chat.title || chat.ref,
            el('span', { className: 'radar-sub', textContent: `${chat.ref} · ${chat.members} участников · по запросу «${chat.query}»` })))));
    $('radar-found-add').hidden = state.found.length === 0;
}

// ---- Проверка и журнал ----

async function refreshLog() {
    const { running, lines } = await get('scan_log');
    const log = $('radar-log');
    const atEnd = log.scrollTop + log.clientHeight >= log.scrollHeight - 20;
    log.textContent = lines.length ? lines.join('\n') : 'Журнал пуст: нажмите «Проверить сейчас».';
    if (atEnd) log.scrollTop = log.scrollHeight;
    $('radar-scan').disabled = running;
    $('radar-scan').textContent = running ? 'Идёт проверка…' : 'Проверить сейчас';
    return running;
}

function watchScan() {
    if (state.poll) return;
    state.poll = setInterval(async () => {
        if (!isOpen()) return;
        try { if (await refreshLog()) return; } catch (error) { flash(error.message); }
        clearInterval(state.poll);
        state.poll = null;
        guard(async () => { await loadSettings(); await Promise.all([loadStats(), loadLeads(true), loadRejected()]); });
    }, 2000);
}

async function runScan(auto) {
    const result = await post('scan_start', { auto });
    if (result.running) watchScan();
}

// ---- Лиды и отсеянное ----

async function loadStats() {
    const s = state.stats = await get('stats');
    $('radar-stats').textContent = `В очереди на модель: ${s.queued} · за сутки просмотрено: ${s.seen_24h}, оценено моделью: ${s.classified_24h}`;
    $('radar-filters').replaceChildren(...Object.entries(STATUSES).map(([status, label]) => {
        const b = el('button', { type: 'button', className: `account-secondary${status === state.status ? ' is-on' : ''}`, textContent: `${label} ${s.statuses[status]}` });
        b.addEventListener('click', () => { state.status = status; guard(async () => { await loadStats(); await loadLeads(true); }); });
        return b;
    }));
}

function safeLink(url, text) {
    return url && url.startsWith('https://t.me/') ? el('a', { href: url, target: '_blank', rel: 'noopener noreferrer', textContent: text }) : document.createTextNode(text);
}

function leadCard(lead) {
    const actions = el('div', { className: 'radar-row radar-wrap' });
    if (lead.message_url) actions.append(safeLink(lead.message_url, 'Открыть сообщение ↗'));
    for (const [status, label] of Object.entries(STATUSES)) {
        if (status === lead.lead_status) continue;
        const b = el('button', { type: 'button', className: 'account-secondary', textContent: label });
        b.addEventListener('click', () => guard(async () => { await post('lead_status', { id: lead.id, status }); await loadStats(); await loadLeads(true); }));
        actions.append(b);
    }
    return el('div', { className: 'radar-item' }, el('p', { textContent: lead.message }),
        el('div', { className: 'account-muted' }, `Чат: ${lead.chat_title || '—'} · Автор: `, safeLink(lead.profile_url, lead.sender_name || lead.sender_username || 'без имени'), ` · ${lead.created_at}`),
        lead.reason ? el('div', { className: 'account-muted', textContent: `Модель: ${lead.reason}` }) : '', actions);
}

async function loadLeads(reset) {
    if (reset) state.offset = 0;
    const { leads, total } = await get('leads', { status: state.status, offset: state.offset });
    if (reset) $('radar-leads').replaceChildren();
    $('radar-leads').append(...leads.map(leadCard));
    state.offset += leads.length;
    $('radar-leads-more').hidden = state.offset >= total;
    $('radar-leads-empty').hidden = total > 0;
    if (!total) $('radar-leads-empty').textContent = state.status === 'new'
        ? `Лидов пока нет. За сутки просмотрено сообщений: ${state.stats?.seen_24h ?? 0}.` : 'В этом списке пусто.';
}

async function loadRejected() {
    const { rejected } = await get('rejected');
    $('radar-rejected-empty').hidden = rejected.length > 0;
    $('radar-rejected').replaceChildren(...rejected.map(row => {
        const b = el('button', { type: 'button', className: 'account-secondary', textContent: 'Переоценить' });
        b.addEventListener('click', () => guard(async () => { await post('rejected_retry', { id: row.id }); await Promise.all([loadRejected(), loadStats()]); }));
        return el('div', { className: 'radar-item' }, el('p', { textContent: row.message }),
            el('div', { className: 'account-muted', textContent: `Чат: ${row.chat_title || '—'} · Модель: ${row.reason || 'причина не записана'}` }),
            el('div', { className: 'radar-row' }, b));
    }));
}

// ---- Запуск ----

function bind() {
    $('radar-account').addEventListener('change', () => guard(async () => {
        await post('account_select', { uid: $('radar-account').value });
        $('radar-queries').value = '';
        await loadAll();
    }));
    $('radar-enabled').addEventListener('change', () => guard(async () => { await saveSettings(); await loadSettings(); }));
    $('radar-chat-add').addEventListener('submit', e => {
        e.preventDefault();
        const ref = $('radar-chat-ref').value.trim().replace(/^(https?:\/\/)?t\.me\//i, '@');
        if (ref) guard(async () => { await addChats([{ ref: ref.startsWith('@') || /^-100\d+$/.test(ref) ? ref : `@${ref}` }]); $('radar-chat-ref').value = ''; });
    });
    $('radar-find-toggle').addEventListener('click', () => { $('radar-find').hidden = !$('radar-find').hidden; });
    $('radar-find-run').addEventListener('click', () => guard(async () => {
        $('radar-find-run').disabled = true;
        $('radar-find-state').textContent = 'Ищу в Telegram… (до минуты)';
        try {
            await saveSettings();
            state.found = (await post('chats_search', { queries: $('radar-queries').value.split('\n') })).chats;
            $('radar-find-state').textContent = state.found.length ? `Найдено групп: ${state.found.length}. Отметьте подходящие.` : 'Новых групп не нашлось: попробуйте другие запросы.';
        } catch (error) { $('radar-find-state').textContent = ''; throw error; }
        finally { $('radar-find-run').disabled = false; }
        renderFound();
    }));
    $('radar-found-add').addEventListener('click', () => guard(async () => {
        const picked = [...$('radar-found').querySelectorAll('input:checked')].map(box => state.found[Number(box.value)]);
        if (!picked.length) return flash('Отметьте хотя бы одну группу.');
        await addChats(picked);
        state.found = state.found.filter(c => !picked.includes(c));
        renderFound();
    }));
    $('radar-scan').addEventListener('click', () => guard(() => runScan(false)));
    $('radar-leads-more').addEventListener('click', () => guard(() => loadLeads(false)));
}

/** Вкладка показывается только владельцу; остальным модуль ничего не делает. */
export async function initializeRadarTab() {
    const { owner } = await api('radar_access').catch(() => ({ owner: false }));
    if (!owner) return;
    initializeAdminTab();
    initializeAdminControl();
    bind();
    adminSections.radar = () => guard(async () => { await loadAll(); if (await refreshLog()) watchScan(); });
    // Пока открыто приложение и радар включён — проверка раз в 10 минут (время сверяет сервер)
    state.auto = setInterval(() => { if (state.settings?.enabled) runScan(true).catch(() => {}); }, 60000);
    guard(async () => { await loadSettings(); if (state.settings.enabled) runScan(true).catch(() => {}); });
}
