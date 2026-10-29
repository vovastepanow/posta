// admin-settings.js — разделы админки «Модели» (поставщик и модель по ролям, бюджет) и «Поведение» (параметры для всех пользователей).
import { $, api, el, log } from './core.js';
import { adminSections } from './account.js';

const money = n => `$${Number(n || 0).toFixed(n >= 1 ? 2 : 4)}`;

// ---- Модели ----

let llm = null;

function modelOptions(provider) {
    if (provider === 'ollama') return (llm.ollama_models || []).map(m => [m, m]);
    if (provider === 'openrouter') {
        const free = $('admin-llm-free-only').checked;
        return (llm.openrouter_models || []).filter(m => !free || m.id.endsWith(':free'))
            .map(m => [m.id, `${m.id}${m.vision ? ' 👁' : ''}${m.id.endsWith(':free') ? '' : ` · ${money(m.prompt_price * 1e6)}/${money(m.completion_price * 1e6)} за 1M`}`]);
    }
    return [];
}

/** Поставщик + модель одной цели (основная или резерв). */
function targetPicker(target, onChange) {
    const provider = el('select', { className: 'llm-provider' },
        ...[['off', 'выключено'], ['ollama', 'Ollama (свой компьютер)'], ['openrouter', 'OpenRouter']].map(([v, t]) => el('option', { value: v, textContent: t })));
    const model = el('select', { className: 'llm-model' });
    const fill = () => {
        const opts = modelOptions(provider.value);
        const current = target.model || '';
        if (current && !opts.some(([v]) => v === current)) opts.unshift([current, `${current} (нет в списке)`]);
        model.replaceChildren(...opts.map(([v, t]) => el('option', { value: v, textContent: t })));
        model.value = current;
        model.hidden = provider.value === 'off';
    };
    provider.value = target.provider || 'off';
    if (!llm.openrouter_key) provider.querySelector('[value="openrouter"]').disabled = true;
    provider.onchange = () => { target.provider = provider.value; target.model = ''; fill(); target.model = model.value; onChange(); };
    model.onchange = () => { target.model = model.value; onChange(); };
    fill();
    return el('span', { className: 'llm-target' }, provider, model);
}

function renderLLM(data) {
    llm = data;
    const s = data.settings;
    $('admin-llm-presets').replaceChildren(...data.presets.map(p => el('button', {
        type: 'button', className: s.preset === p.id ? 'primary-button' : 'secondary-button', textContent: p.title, title: p.note,
        onclick: () => saveLLM({ preset: p.id }, `Набор моделей: ${p.title}.`),
    })));
    $('admin-llm-preset-note').textContent = (data.presets.find(p => p.id === s.preset)?.note) || 'Свой набор моделей.';
    const credits = data.openrouter_credits ? ` · баланс OpenRouter: ${money(data.openrouter_credits.total - data.openrouter_credits.used)}` : '';
    $('admin-llm-state').textContent = data.openrouter_key
        ? `Сегодня потрачено ${money(data.spent_today)} из ${money(s.daily_budget_usd)} · бесплатных запросов ${data.free_used} из ${s.free_daily_limit}${credits}`
        : 'Ключ OpenRouter не задан на сервере (OPENROUTER_API_KEY в posta.env): доступна только Ollama.';
    if (data.ollama_error) $('admin-llm-state').textContent += ` · ${data.ollama_error}`;
    $('admin-llm-budget').value = s.daily_budget_usd;
    $('admin-llm-free-limit').value = s.free_daily_limit;
    $('admin-llm-fast-only').checked = !!s.fast_only;
    const rows = data.roles.map(r => {
        const rc = s.roles[r.role];
        const threshold = r.role === 'analysis'
            ? el('label', { className: 'llm-threshold' }, 'порог «по теме» ', el('input', { type: 'number', min: 0, max: 100, value: rc.threshold || 0, onchange: e => { rc.threshold = Number(e.target.value); } }))
            : '';
        return el('div', { className: 'llm-role' }, el('b', { textContent: r.title }),
            el('div', {}, el('span', { className: 'account-muted', textContent: 'основная ' }), targetPicker(rc.primary, () => {})),
            el('div', {}, el('span', { className: 'account-muted', textContent: 'резерв ' }), targetPicker(rc.fallback, () => {})), threshold);
    });
    $('admin-llm-roles').replaceChildren(...rows);
}

const loadLLM = () => api('admin_llm').then(renderLLM).catch(e => { $('admin-llm-state').textContent = `Ошибка: ${e.message}`; });

async function saveLLM(body, message) {
    try { renderLLM(await api('admin_llm_save', { body })); $('admin-llm-note').textContent = message; log(message); }
    catch (e) { $('admin-llm-note').textContent = `Ошибка: ${e.message}`; }
}

function saveCustom() {
    const s = llm.settings;
    s.daily_budget_usd = Number($('admin-llm-budget').value) || 0;
    s.free_daily_limit = Number($('admin-llm-free-limit').value) || 0;
    s.fast_only = $('admin-llm-fast-only').checked;
    saveLLM({ settings: s }, 'Модели сохранены.');
}

// ---- Поведение ----

let tunables = [];

function tunableInput(t) {
    const input = t.kind === 'bool' ? el('input', { type: 'checkbox', checked: !!t.value })
        : t.kind === 'text' ? el('input', { type: 'text', value: t.value ?? '' })
        : el('input', { type: 'number', value: t.value, min: t.min, max: t.max, step: t.kind === 'float' ? 'any' : 1 });
    input.dataset.key = t.key;
    return input;
}

function renderTunables(list) {
    tunables = list;
    const groups = {};
    for (const t of list) (groups[t.group] ||= []).push(t);
    $('admin-tunables').replaceChildren(...Object.entries(groups).map(([group, items]) => el('fieldset', { className: 'tunable-group' },
        el('legend', { textContent: group }),
        ...items.map(t => el('label', { className: `tunable${t.changed ? ' is-changed' : ''}` },
            el('span', { className: 'tunable-title', textContent: t.title + (t.unit ? `, ${t.unit}` : '') }),
            tunableInput(t),
            el('small', { className: 'account-muted', textContent: [t.help, t.changed ? `по умолчанию: ${t.default === true ? 'да' : t.default === false ? 'нет' : t.default}` : ''].filter(Boolean).join(' · ') }),
            t.changed ? el('button', { type: 'button', className: 'link-button', textContent: 'сбросить', onclick: e => { e.preventDefault(); saveTunables({ [t.key]: null }, `«${t.title}»: по умолчанию.`); } }) : '')))));
}

const loadTunables = () => api('admin_tunables').then(d => renderTunables(d.tunables)).catch(e => { $('admin-tunables-note').textContent = `Ошибка: ${e.message}`; });

async function saveTunables(values, message) {
    try { const d = await api('admin_tunables_save', { body: { values } }); renderTunables(d.tunables); $('admin-tunables-note').textContent = message; log(message); }
    catch (e) { $('admin-tunables-note').textContent = `Ошибка: ${e.message}`; }
}

function collectTunables() {
    const values = {};
    for (const input of $('admin-tunables').querySelectorAll('[data-key]')) {
        const t = tunables.find(x => x.key === input.dataset.key);
        const v = t.kind === 'bool' ? input.checked : t.kind === 'text' ? input.value : Number(input.value);
        if (JSON.stringify(v) !== JSON.stringify(t.value)) values[t.key] = v;
    }
    return values;
}

export function initializeAdminSettings() {
    $('admin-llm-save').onclick = saveCustom;
    $('admin-llm-free-only').onchange = () => llm && renderLLM(llm);
    $('admin-tunables-save').onclick = () => {
        const values = collectTunables();
        if (!Object.keys(values).length) { $('admin-tunables-note').textContent = 'Изменений нет.'; return; }
        saveTunables(values, `Сохранено параметров: ${Object.keys(values).length}. Действует для всех пользователей.`);
    };
    adminSections.models = loadLLM;
    adminSections.behavior = loadTunables;
}
