// main.js — точка входа (ES-модуль, без сборки): вкладки, панель модели, демо-режим.
import { $, DEBUG_MODE, api, el, hide, initializeCore, log, show, startTabSession, switchTab, watchParser } from './core.js';
import { initializeImageViewer } from './viewer.js';
import { initializeContentTab, loadCurrentItem } from './content.js';
import { clearDiscoveredSource, initializeSourceTab } from './sources.js';
import { consumeReferralLink, consumeVkRedirectResult, initializeAccountTab, openAccountSection, openNewItemEditor } from './account.js';
import { initializeTabs } from './tabs.js';
import { showPlan, showProfile } from './profile.js';
import { initializeRadarTab } from './radar.js';
import { initializeCalendarTab } from './calendar.js';
import { setImageCacheUser } from './image-cache.js';

// ---- Панель модели под журналом: что загружено в Ollama, загрузка и выгрузка ----

const model = { busy: false, timer: null, status: null, armed: '', armedAt: 0 };

function renderModel(s) {
    model.status = s;
    if (!s.ollama) {
        $('model-summary').textContent = 'Ollama недоступна';
        $('model-gpu').textContent = 'Служба Ollama не отвечает. Перезапуск службы: sudo systemctl restart ollama';
        return $('model-loaded').replaceChildren();
    }
    $('model-summary').textContent = s.loaded.length ? s.loaded.map(m => `${m.name} · ${m.gpu_percent}% GPU`).join(', ') : 'ничего не загружено';
    $('model-gpu').textContent = [`Ollama ${s.version}`, s.gpu ? `видеопамять ${s.gpu.used_mb} / ${s.gpu.total_mb} МБ, загрузка GPU ${s.gpu.util}%` : `модели в видеопамяти: ${s.loaded.reduce((a, m) => a + m.vram_mb, 0)} МБ`,
        s.parser_running ? 'парсер работает' : 'парсер остановлен'].join(' · ');
    $('model-loaded').replaceChildren(...s.loaded.map(m => {
        const role = { prescreen: 'быстрая', analysis: 'основная' }[Object.keys(s.roles).find(k => [s.roles[k], `${s.roles[k]}:latest`].includes(m.name))];
        const left = Math.round((new Date(m.expires_at) - Date.now()) / 1000);
        const until = left > 0 ? `выгрузится через ${left >= 120 ? `${Math.round(left / 60)} мин` : `${left} с`}` : '';
        return el('li', {}, el('span', { className: 'model-loaded-name', textContent: m.name + (role ? ` (${role})` : '') }),
            el('span', { className: 'model-loaded-details', textContent: [`${m.size_mb} МБ`, `${100 - m.gpu_percent}%/${m.gpu_percent}% CPU/GPU`, `контекст ${m.context}`, until].filter(Boolean).join(' · ') }),
            el('button', { type: 'button', textContent: 'Выгрузить', onclick: () => controlModel({ op: 'unload', model: m.name }) }));
    }));
    document.querySelectorAll('#model-panel [data-role]').forEach(b => { b.title = s.roles[b.dataset.role] || ''; });
}

async function refreshModel() {
    clearTimeout(model.timer);
    try { renderModel(await api('model_status')); } catch (error) { $('model-summary').textContent = `нет ответа: ${error.message}`; }
    model.timer = setTimeout(refreshModel, $('model-panel').open ? 4000 : 20000);
}

/** Пока работает парсер, ручная выгрузка оборвёт его запрос: первое нажатие предупреждает, второе (в течение 5 с) выполняет. */
async function controlModel(body) {
    if (model.busy) return;
    const key = JSON.stringify(body);
    if (model.status?.parser_running && !(model.armed === key && Date.now() - model.armedAt < 5000)) {
        Object.assign(model, { armed: key, armedAt: Date.now() });
        return void ($('model-panel-note').textContent = 'Парсер сейчас работает: его текущий запрос к модели оборвётся. Нажмите ещё раз, чтобы подтвердить.');
    }
    model.armed = '';
    model.busy = true;
    const buttons = document.querySelectorAll('#model-panel button');
    buttons.forEach(b => { b.disabled = true; });
    $('model-panel-note').textContent = ['load', 'restart'].includes(body.op) ? 'Загружаю модель — до минуты…' : 'Выгружаю…';
    try { const { message } = await api('model_control', { body }); $('model-panel-note').textContent = message; log(`Модель: ${message}`); }
    catch (error) { $('model-panel-note').textContent = `Ошибка: ${error.message}`; log(`Модель: ошибка — ${error.message}`); }
    finally { model.busy = false; buttons.forEach(b => { b.disabled = false; }); refreshModel(); }
}

// ---- Демо-режим (?debug=1): переключатель в шапке перезагружает страницу с параметром или без него ----

function initializeDebugMode() {
    const toggle = $('debug-toggle');
    toggle.setAttribute('aria-pressed', String(DEBUG_MODE));
    toggle.title = DEBUG_MODE ? 'Выключить демо-режим: вернуться к своим данным' : 'Включить демо-режим: тестовые записи, файлы и модель не трогаются';
    toggle.onclick = () => {
        const url = new URL(location.href);
        DEBUG_MODE ? url.searchParams.delete('debug') : url.searchParams.set('debug', '1');
        location.assign(url);
    };
    if (!DEBUG_MODE) return;
    show('debug-banner');
    $('debug-reset-button').onclick = () => api('debug_reset', { body: null }).then(() => { clearDiscoveredSource(); return loadCurrentItem(); }).catch(e => log(`Ошибка сброса демо: ${e.message}`));
}

initializeCore();
initializeImageViewer();
initializeContentTab({ onAddItem: openNewItemEditor });
initializeSourceTab();
initializeAccountTab({ onDatabasesReset: loadCurrentItem });
initializeTabs();
// Only construct links from known public identifiers; private resources may have none.
function resourceUrl(platform, ref, username = '') {
    const id = String(ref || '');
    const handle = String(username || '').replace(/^@/, '');
    if (platform === 'telegram' && /^[A-Za-z][A-Za-z0-9_]{3,31}$/.test(handle)) return `https://t.me/${handle}`;
    if (platform === 'vk' && /^[A-Za-z0-9_.]{1,100}$/.test(handle)) return `https://vk.com/${handle}`;
    if (platform === 'vk' && /^[1-9][0-9]*$/.test(id)) return `https://vk.com/club${id}`;
    if (platform === 'youtube' && /^UC[A-Za-z0-9_-]+$/.test(id)) return `https://www.youtube.com/channel/${id}`;
    if (platform === 'pinterest' && /^[A-Za-z0-9_]{1,50}$/.test(id)) return `https://www.pinterest.com/${id}/`;
    if (platform === 'tumblr' && /^[A-Za-z0-9-]{1,64}$/.test(id)) return `https://${id}.tumblr.com/`;
    if (platform === 'reddit' && /^[A-Za-z0-9_-]{1,32}$/.test(id)) return `https://www.reddit.com/user/${id}/`;
    if (platform === 'bluesky' && /^[A-Za-z0-9.-]+\.[A-Za-z]{2,}$/.test(handle)) return `https://bsky.app/profile/${handle}`;
    if (platform === 'x' && /^[A-Za-z0-9_]{1,15}$/.test(handle)) return `https://x.com/${handle}`;
    if (platform === 'ok' && /^[1-9][0-9]*$/.test(id)) return `https://ok.ru/group/${id}`;
    return '';
}

let resourceLoadVersion = 0;
async function loadSideResources() {
    const version = ++resourceLoadVersion;
    const list = $('side-resources-list');
    try {
        const [telegram, other] = await Promise.all([api('telegram_queue'), api('networks_list')]);
        if (version !== resourceLoadVersion) return;
        const resources = [ ...(telegram.channels || []).map(c => ({ badge: 'TG', name: c.title || c.username || c.id, url: resourceUrl('telegram', '', c.username) })),
            ...(other.channels || []).map(c => ({ badge: (c.platform === 'vk' ? 'VK' : c.platform.slice(0, 2)).toUpperCase(), name: c.title || c.ref, url: resourceUrl(c.platform, c.ref, c.platform === 'vk' ? c.screen_name : c.title) })) ];
        list.replaceChildren(...(resources.length ? resources.map(c => { const content = [el('span', { className: 'side-resource-badge', textContent: c.badge }), el('span', { textContent: c.name })]; return el('li', { title: c.url ? c.name : `${c.name} — публичный адрес недоступен` }, c.url ? el('a', { href: c.url, target: '_blank', rel: 'noopener noreferrer' }, ...content) : el('span', { className: 'side-resource-unlinked' }, ...content)); }) : [el('li', { textContent: 'Нет подключённых ресурсов' })]));
    } catch (error) { if (version === resourceLoadVersion) list.replaceChildren(el('li', { textContent: 'Не удалось загрузить ресурсы' })); }
}
$('side-resources-manage').addEventListener('click', () => openAccountSection('social'));
addEventListener('posta:resources-changed', loadSideResources);
loadSideResources();
api('account_info').then(info => { setImageCacheUser(info.user_id); showProfile(info); }).catch(() => {});
api('billing_info').then(showPlan).catch(() => {});
initializeRadarTab();
try { initializeCalendarTab(); } catch (error) { log(`Календарь не запущен: ${error.message}`); }   // вкладка не должна ронять запуск всего приложения
$('model-panel').addEventListener('toggle', refreshModel);
document.querySelectorAll('#model-panel [data-model-op]').forEach(b => b.addEventListener('click', () => controlModel({ op: b.dataset.modelOp, role: b.dataset.role })));
refreshModel();
initializeDebugMode();

log('Приложение запущено. Загружаю пул контента.');
if (consumeVkRedirectResult()) openAccountSection('social');
const sessionReady = startTabSession();   // запрос пула начнём после регистрации вкладки
// Хеш-ссылки открывают нужную вкладку на единой странице приложения.
const HASH_TABS = { home: 'home-tab', content: 'content-tab', proposals: 'home-tab', calendar: 'calendar-tab', sources: 'source-tab', account: 'account-tab' };
const openHashTab = () => { const tab = HASH_TABS[location.hash.slice(1)]; if (tab) switchTab(tab); };
addEventListener('hashchange', openHashTab);
openHashTab();
// Интерфейс доступен сразу; содержимое подбора загружается после регистрации сессии.
$('app-loading').classList.add('is-done');
setTimeout(() => $('app-loading')?.remove(), 400);
sessionReady.then(() => {
    consumeReferralLink(); // у браузера уже есть кука после регистрации вкладки
    loadCurrentItem();
    watchParser();
}).catch(error => log(`Не удалось завершить запуск: ${error.message}`));
