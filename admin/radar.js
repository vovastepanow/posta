// radar.js — админка радара лидов. Всё из Telegram (тексты, имена, названия чатов) выводится только через textContent.
(() => {
    const $ = id => document.getElementById(id);
    const STATUSES = { new: 'Новые', interested: 'Интересно', replied: 'Ответил', spam: 'Спам', ignored: 'Пропустить' };
    let csrf = '';
    let settings = null;
    let leadStatus = 'new';
    let leadOffset = 0;
    let stats = null;
    let polling = null;
    let found = [];

    /** Элемент с текстом и свойствами: el('a', {href, className}, 'текст', вложенные…). */
    function el(tag, props = {}, ...children) {
        const node = document.createElement(tag);
        for (const [key, value] of Object.entries(props)) node[key] = value;
        for (const child of children) node.append(child);
        return node;
    }

    async function api(action, { body = null, params = {} } = {}) {
        const query = new URLSearchParams({ action, ...params });
        const init = body === null ? {} : { method: 'POST', headers: { 'X-Admin-CSRF': csrf, 'Content-Type': 'application/json' }, body: JSON.stringify(body) };
        const response = await fetch('index.php?' + query, { credentials: 'same-origin', cache: 'no-store', ...init });
        const data = await response.json().catch(() => ({ message: 'Сервер ответил не JSON.' }));
        if (!response.ok || data.status === 'error') {
            if (response.status === 401 && action !== 'login') showLogin();
            throw new Error(data.message || 'Ошибка ' + response.status);
        }
        return data;
    }

    function flash(message) { $('flash').textContent = message || ''; }
    async function guard(task) {
        try { flash(''); return await task(); } catch (error) { flash(error.message); }
    }

    // ---- Вход ----
    function showLogin() {
        $('login').hidden = false;
        $('app').hidden = true;
        $('logout').hidden = true;
    }

    async function start() {
        const session = await api('session');
        csrf = session.csrf;
        if (!session.admin) return showLogin();
        $('login').hidden = true;
        $('app').hidden = false;
        $('logout').hidden = false;
        await guard(loadAll);
        setInterval(autoScan, 60000);
    }

    $('login').addEventListener('submit', async event => {
        event.preventDefault();
        $('login-error').textContent = '';
        try {
            const result = await api('login', { body: { password: $('login-password').value } });
            csrf = result.csrf;
            $('login-password').value = '';
            await start();
        } catch (error) { $('login-error').textContent = error.message; }
    });

    $('logout').addEventListener('click', async () => {
        await guard(() => api('logout', { body: {} }));
        location.reload();
    });

    // ---- Аккаунт и настройки ----
    async function loadAll() {
        await loadAccounts();
        await loadSettings();
        if (!settings.account_uid) return;
        await Promise.all([loadStats(), loadLeads(true), loadRejected(), refreshLog()]);
    }

    async function loadAccounts() {
        const { accounts } = await api('accounts');
        const select = $('account');
        select.replaceChildren(...accounts.map(a => {
            const when = a.created_at ? ' · с ' + new Date(a.created_at * 1000).toLocaleDateString() : '';
            return el('option', { value: a.uid, selected: a.selected, textContent: a.short + when });
        }));
        $('account-empty').hidden = accounts.length > 0;
        $('account-body').hidden = accounts.length === 0;
    }

    async function loadSettings() {
        settings = await api('settings');
        $('enabled').checked = settings.enabled;
        if (settings.account_uid) $('account').value = settings.account_uid;
        const parts = [];
        parts.push(settings.last_run ? 'Последняя проверка: ' + new Date(settings.last_run * 1000).toLocaleString() + ', просмотрено ' + settings.last_seen : 'Проверок ещё не было.');
        if (settings.last_error) parts.push('Ошибка: ' + settings.last_error);
        $('radar-state').textContent = parts.join(' · ');
        if (!$('queries').value) $('queries').value = settings.queries.join('\n');
        renderChats();
    }

    /** Сохранить настройки как они сейчас на странице. */
    function saveSettings() {
        return api('settings_save', { body: {
            enabled: $('enabled').checked,
            chats: settings.chats.map(c => ({ ref: c.ref, members: c.members })),
            queries: $('queries').value.split('\n')
        } });
    }

    $('account').addEventListener('change', () => guard(async () => {
        await api('account_select', { body: { uid: $('account').value } });
        $('queries').value = '';
        await loadAll();
    }));

    $('enabled').addEventListener('change', () => guard(async () => { await saveSettings(); await loadSettings(); }));

    // ---- Чаты ----
    function renderChats() {
        $('chats-empty').hidden = settings.chats.length > 0;
        $('chats').replaceChildren(...settings.chats.map(chat => {
            const sub = [chat.ref, chat.members ? chat.members + ' участников' : ''].filter(Boolean).join(' · ');
            const remove = el('button', { type: 'button', className: 'btn small', title: 'Убрать', textContent: '×' });
            remove.addEventListener('click', () => guard(async () => {
                settings.chats = settings.chats.filter(c => c.ref !== chat.ref);
                await saveSettings();
                await loadSettings();
            }));
            return el('li', {}, el('span', { className: 'grow' }, chat.title || chat.ref,
                el('span', { className: 'sub', textContent: sub }),
                ...(chat.error ? [el('span', { className: 'sub bad', textContent: chat.error })] : [])), remove);
        }));
    }

    async function addChats(items) {
        const have = new Set(settings.chats.map(c => c.ref.toLowerCase()));
        for (const item of items) if (!have.has(item.ref.toLowerCase())) settings.chats.push({ ref: item.ref, title: item.title || '', members: item.members || 0, error: null });
        await saveSettings();
        await loadSettings();
    }

    $('chat-add').addEventListener('submit', event => {
        event.preventDefault();
        const ref = $('chat-ref').value.trim();
        if (!ref) return;
        guard(async () => { await addChats([{ ref }]); $('chat-ref').value = ''; });
    });

    $('find-toggle').addEventListener('click', () => { $('find').hidden = !$('find').hidden; });

    $('find-run').addEventListener('click', () => guard(async () => {
        $('find-run').disabled = true;
        $('find-state').textContent = 'Ищу в Telegram…';
        try {
            await saveSettings();
            found = (await api('chats_search', { body: { queries: $('queries').value.split('\n') } })).chats;
        } finally { $('find-run').disabled = false; }
        $('find-state').textContent = found.length ? 'Найдено групп: ' + found.length + '. Отметьте подходящие.' : 'Новых групп не нашлось: попробуйте другие запросы.';
        renderFound();
    }));

    function renderFound() {
        $('found').replaceChildren(...found.map((chat, index) => el('li', {},
            el('input', { type: 'checkbox', value: String(index), id: 'found-' + index }),
            el('label', { className: 'grow', htmlFor: 'found-' + index }, chat.title || chat.ref,
                el('span', { className: 'sub', textContent: chat.ref + ' · ' + chat.members + ' участников · по запросу «' + chat.query + '»' })))));
        $('found-add').hidden = found.length === 0;
    }

    $('found-add').addEventListener('click', () => guard(async () => {
        const picked = [...$('found').querySelectorAll('input:checked')].map(box => found[Number(box.value)]);
        if (!picked.length) return flash('Отметьте хотя бы одну группу.');
        await addChats(picked);
        found = found.filter(chat => !picked.includes(chat));
        renderFound();
    }));

    // ---- Проверка и журнал ----
    async function refreshLog() {
        const { running, lines } = await api('scan_log');
        const log = $('log');
        const atEnd = log.scrollTop + log.clientHeight >= log.scrollHeight - 20;
        log.textContent = lines.join('\n');
        if (atEnd) log.scrollTop = log.scrollHeight;
        $('scan').disabled = running;
        return running;
    }

    function poll() {
        if (polling) return;
        polling = setInterval(async () => {
            try {
                if (await refreshLog()) return;
            } catch (error) { flash(error.message); }
            clearInterval(polling);
            polling = null;
            guard(async () => { await loadSettings(); await Promise.all([loadStats(), loadLeads(true), loadRejected()]); });
        }, 2000);
    }

    async function runScan(auto) {
        const result = await api('scan_start', { body: { auto } });
        if (result.running) { $('scan').disabled = true; poll(); }
        return result;
    }

    $('scan').addEventListener('click', () => guard(() => runScan(false)));
    // Пока страница открыта: раз в минуту, если радар включён (старше 10 минут проверяет сервер)
    function autoScan() { if (settings && settings.account_uid && settings.enabled) runScan(true).catch(() => {}); }

    // ---- Лиды ----
    async function loadStats() {
        stats = await api('stats');
        $('stats').textContent = 'В очереди на модель: ' + stats.queued + ' · за сутки просмотрено: ' + stats.seen_24h + ', оценено моделью: ' + stats.classified_24h;
        $('filters').replaceChildren(...Object.entries(STATUSES).map(([status, label]) => {
            const button = el('button', { type: 'button', className: 'btn small' + (status === leadStatus ? ' on' : ''), textContent: label + ' ' + stats.statuses[status] });
            button.addEventListener('click', () => { leadStatus = status; guard(async () => { await loadStats(); await loadLeads(true); }); });
            return button;
        }));
    }

    async function loadLeads(reset) {
        if (reset) leadOffset = 0;
        const { leads, total } = await api('leads', { params: { status: leadStatus, offset: leadOffset } });
        const box = $('leads');
        if (reset) box.replaceChildren();
        box.append(...leads.map(leadCard));
        leadOffset += leads.length;
        $('leads-more').hidden = leadOffset >= total;
        const empty = $('leads-empty');
        empty.hidden = total > 0;
        if (total === 0) empty.textContent = leadStatus === 'new'
            ? 'Лидов пока нет: просмотрено сообщений за сутки — ' + (stats ? stats.seen_24h : 0) + '.' : 'В этом списке пусто.';
    }

    $('leads-more').addEventListener('click', () => guard(() => loadLeads(false)));

    function safeLink(url, text) {
        return url && url.startsWith('https://t.me/') ? el('a', { href: url, target: '_blank', rel: 'noopener noreferrer', textContent: text }) : document.createTextNode(text);
    }

    function leadCard(lead) {
        const meta = el('div', { className: 'meta' }, 'Чат: ' + (lead.chat_title || '—') + ' · Автор: ', safeLink(lead.profile_url, lead.sender_name || lead.sender_username || 'без имени'),
            ' · ' + lead.created_at);
        const actions = el('div', { className: 'actions' });
        if (lead.message_url) actions.append(safeLink(lead.message_url, 'Открыть сообщение'));
        for (const [status, label] of Object.entries(STATUSES)) {
            if (status === lead.lead_status) continue;
            const button = el('button', { type: 'button', className: 'btn small', textContent: label });
            button.addEventListener('click', () => guard(async () => {
                await api('lead_status', { body: { id: lead.id, status } });
                await loadStats();
                await loadLeads(true);
            }));
            actions.append(button);
        }
        return el('div', { className: 'item' }, el('p', { textContent: lead.message }), meta, el('div', { className: 'meta', textContent: lead.reason ? 'Модель: ' + lead.reason : '' }), actions);
    }

    // ---- Отсеяно моделью ----
    async function loadRejected() {
        const { rejected } = await api('rejected');
        $('rejected-empty').hidden = rejected.length > 0;
        $('rejected').replaceChildren(...rejected.map(row => {
            const button = el('button', { type: 'button', className: 'btn small', textContent: 'Переоценить' });
            button.addEventListener('click', () => guard(async () => {
                await api('rejected_retry', { body: { id: row.id } });
                await Promise.all([loadRejected(), loadStats()]);
            }));
            return el('div', { className: 'item' }, el('p', { textContent: row.message }),
                el('div', { className: 'meta', textContent: 'Чат: ' + (row.chat_title || '—') + ' · Модель: ' + (row.reason || 'причина не записана') }),
                el('div', { className: 'actions' }, button));
        }));
    }

    start().catch(error => { $('login').hidden = false; $('login-error').textContent = error.message; });
})();
