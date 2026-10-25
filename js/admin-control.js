// admin-control.js — раздел админки «Управление»: запуск сборщика и автоподбора, остановка всего, режим «только быстрая модель».
import { $, TAB_ID, api, log, watchParser } from './core.js';
import { adminSections } from './account.js';
import { loadCurrentItem } from './content.js';

const buttons = () => ['admin-run-search', 'admin-run-discovery', 'admin-stop-all'].map($);
const note = text => { $('admin-control-note').textContent = text; };

/** Пока идёт действие, остальные кнопки раздела недоступны. */
async function run(button, action, done, body = null) {
    buttons().forEach(b => { b.disabled = true; });
    note('');
    try { const message = done(await api(action, { body })); note(message); log(message); }
    catch (error) { note(`Ошибка: ${error.message}`); log(`Ошибка: ${error.message}`); }
    finally { buttons().forEach(b => { b.disabled = false; }); }
}

function renderModels(m) {
    const box = $('admin-fast-only');
    box.checked = m.fast_only && m.fast_installed;
    box.disabled = !m.fast_installed;
    $('admin-fast-only-label').textContent = `Только быстрая модель (${m.fast_model})`;
    $('admin-models-state').textContent = m.fast_installed
        ? `Сейчас: отбор постов — ${m.analysis}, задачи «раз на тему» — ${m.smart}. Основная модель — ${m.main_model}.`
        : `Быстрая модель не установлена — режим недоступен. Установите: ollama pull ${m.fast_model}`;
}

const loadModels = () => api('radar', { params: { op: 'models' } }).then(renderModels).catch(error => { $('admin-models-state').textContent = `Ошибка: ${error.message}`; });

const lines = text => text.split('\n').map(line => line.trim()).filter(Boolean);

/** Украинский контент: переключатель, списки и заблокированные источники (разблокировка — кнопкой в строке). */
function renderUa(u) {
    $('admin-ua-on').checked = u.on;
    $('admin-ua-state').textContent = `Отсеяно за сутки: ${u.stats} постов, источников в блокировке: ${u.blocked_sources.length}`;
    $('admin-ua-domains').value = u.blocklist.domains.join('\n');
    $('admin-ua-sources').value = u.blocklist.sources.join('\n');
    const box = $('admin-ua-blocked');
    box.replaceChildren();
    if (!u.blocked_sources.length) { box.textContent = 'Нет.'; return; }
    for (const s of u.blocked_sources) {
        const row = document.createElement('div');
        const name = document.createElement('span');
        name.textContent = `${s.name} — ${s.reason} `;
        const button = document.createElement('button');
        button.type = 'button';
        button.textContent = 'Разблокировать';
        button.onclick = () => unblockUa(s.id);
        row.append(name, button);
        box.append(row);
    }
}

const loadUa = () => api('radar', { params: { op: 'ua_filter' } }).then(renderUa).catch(error => { $('admin-ua-state').textContent = `Ошибка: ${error.message}`; });

async function saveUa(body, message) {
    try { const r = await api('radar_do', { body: { op: 'ua_filter_save', ...body } }); renderUa(r); $('admin-ua-note').textContent = message; log(message); }
    catch (error) { $('admin-ua-note').textContent = `Ошибка: ${error.message}`; }
}

async function unblockUa(id) {
    try { const r = await api('radar_do', { body: { op: 'ua_unblock', id } }); renderUa(r); log(`Источник разблокирован вручную: ${id}.`); }
    catch (error) { $('admin-ua-note').textContent = `Ошибка: ${error.message}`; }
}

export function initializeAdminControl() {
    $('admin-run-search').onclick = e => run(e.currentTarget, 'control_search', d => { watchParser(); loadCurrentItem(); return d.message || 'Запущен поиск контента.'; });
    $('admin-run-discovery').onclick = e => run(e.currentTarget, 'control_sources', d => { watchParser(); return d.message || 'Запущен автоподбор источников.'; });
    $('admin-stop-all').onclick = e => confirm('Остановить всё на сервере? Будут завершены сборщики контента всех пользователей и помощник Telegram, модели выгрузятся из видеопамяти. Чтобы снова работать, обновите страницу.')
        && run(e.currentTarget, 'app_stop', d => `Остановлено процессов: ${d.stopped}. Обновите страницу, чтобы снова работать.`, { tab: TAB_ID });
    $('admin-fast-only').onchange = async e => {
        const box = e.currentTarget;
        box.disabled = true;
        try {
            const r = await api('radar_do', { body: { op: 'models_save', fast_only: box.checked } });
            renderModels(r);
            log(r.fast_only ? `Режим «только быстрая модель» включён: все задачи на ${r.fast_model}.` : `Режим «только быстрая модель» выключен: снова ${r.main_model}.`);
        } catch (error) { $('admin-models-state').textContent = `Ошибка: ${error.message}`; box.checked = !box.checked; box.disabled = false; }
    };
    $('admin-ua-on').onchange = e => saveUa({ on: e.currentTarget.checked }, e.currentTarget.checked ? 'Фильтр украинского контента включён.' : 'Фильтр украинского контента выключен: скрытое снова видно.');
    $('admin-ua-save').onclick = () => saveUa({ blocklist: { domains: lines($('admin-ua-domains').value), sources: lines($('admin-ua-sources').value) } }, 'Список блокировки сохранён.');
    adminSections.control = () => { loadModels(); loadUa(); };
}
