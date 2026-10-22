// main.js — точка входа (ES-модуль, без сборки): вкладки, панель модели, кнопки управления, демо-режим.
import { $, DEBUG_MODE, TAB_ID, api, el, hide, initializeCore, log, show, startTabSession, watchParser } from './core.js';
import { initializeImageViewer } from './viewer.js';
import { initializeContentTab, loadCurrentItem } from './content.js';
import { clearDiscoveredSource, initializeSourceTab } from './sources.js';
import { consumeReferralLink, consumeVkRedirectResult, initializeAccountTab, openAccountSection, openNewItemEditor } from './account.js';
import { initializeTabs } from './tabs.js';
import { initializeTheme } from './theme.js';

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

// ---- Кнопки управления справа: искать контент, обновить источники, остановить всё ----

async function dockAction(button, action, done, body = null) {
    document.querySelectorAll('.controls-management .btn').forEach(b => { b.disabled = b !== button; });
    button.classList.add('is-busy');
    try { done(await api(action, { body })); } catch (error) { log(`Ошибка: ${error.message}`); }
    finally { document.querySelectorAll('.controls-management .btn').forEach(b => { b.disabled = false; }); button.classList.remove('is-busy'); }
}

function initializeDock() {
    $('card-control-search').onclick = e => dockAction(e.currentTarget, 'control_search', d => { log(d.message || 'Запущен поиск контента.'); watchParser(); loadCurrentItem(); });
    $('card-control-sources').onclick = e => dockAction(e.currentTarget, 'control_sources', d => { log(d.message || 'Запущено обновление базы источников.'); watchParser(); });
    $('card-control-stop').onclick = e => confirm('Остановить приложение? Будут остановлены все процессы: сборщики контента (в том числе других пользователей), помощник Telegram и модели. Чтобы снова работать, обновите страницу.')
        && dockAction(e.currentTarget, 'app_stop', d => log(`Приложение остановлено: процессов завершено ${d.stopped}. Обновите страницу, чтобы снова работать.`), { tab: TAB_ID });
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
initializeTheme();
$('model-panel').addEventListener('toggle', refreshModel);
document.querySelectorAll('#model-panel [data-model-op]').forEach(b => b.addEventListener('click', () => controlModel({ op: b.dataset.modelOp, role: b.dataset.role })));
refreshModel();
initializeDock();
initializeDebugMode();

log('Приложение запущено. Загружаю пул контента.');
if (consumeVkRedirectResult()) openAccountSection('social');
await startTabSession();   // сначала сессия: запрос пула запускает парсер, привязанный к этой вкладке
consumeReferralLink();   // после первого запроса: у браузера уже есть кука, иначе два запроса без неё заведут двух пользователей
const first = loadCurrentItem();
watchParser();
await first.catch(() => {});
$('app-loading').classList.add('is-done');   // кольцо загрузки на весь экран — пока не готова первая вкладка
setTimeout(() => $('app-loading').remove(), 400);
