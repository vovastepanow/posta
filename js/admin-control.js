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
    adminSections.control = loadModels;
}
