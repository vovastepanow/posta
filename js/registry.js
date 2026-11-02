// registry.js — «Проверенные» на вкладке «Источники»: источники, которые уже находили, предлагали или отклоняли.
// Подбор их не ищет повторно; здесь их можно вернуть в предложения или забыть.
import { $, ask, api, el, formatAgo, log } from './core.js';

const STATUS_LABEL = {
    catalog: 'в каталоге', suggested: 'предложен', owner_yes: 'одобрили вы', owner_no: 'отклонили вы', owner_maybe: 'отметили «не знаю»',
    rejected: 'не подошёл', dead: 'мёртвый', failed: 'не загрузился', ukraine: 'украинский',
};
const PLATFORMS = ['Telegram', 'ВКонтакте', 'RSS', 'Одноклассники', 'TikTok', 'Двач', '4chan'];
// «Предложить снова» — только для тех, кого не добавили в каталог и не предлагают сейчас
const CAN_READD = new Set(['rejected', 'owner_no', 'owner_maybe', 'dead', 'failed']);

const registry = { rows: [] };
let searchTimer = 0;

function filters() {
    return {
        status: $('registry-status').value,
        platform: $('registry-platform').value,
        q: $('registry-search').value.trim(),
        only_topic: $('registry-only-topic').checked ? '1' : '',
    };
}

function registryTopicLines(topics) {
    return Object.entries(topics || {}).map(([key, t]) => `${key}: ${t.score ? `${t.score}%` : 'без балла'}${t.reason ? ` — ${t.reason}` : ''}`);
}

function registryCard(row) {
    const actions = el('div', { className: 'proposal-actions' });
    if (row.url) actions.append(el('a', { className: 'proposal-action', href: row.url, target: '_blank', rel: 'noopener noreferrer', textContent: 'Открыть ↗' }));
    if (CAN_READD.has(row.status)) actions.append(el('button', { className: 'proposal-action', type: 'button', textContent: 'Предложить снова', onclick: () => readd(row) }));
    actions.append(el('button', { className: 'proposal-action proposal-hide', type: 'button', textContent: 'Забыть', onclick: () => forget(row) }));
    const lines = registryTopicLines(row.topics);
    const origin = row.origin && row.origin.detail ? `найден: ${row.origin.detail}` : '';
    const checked = row.checked_at ? `проверен ${formatAgo(row.checked_at)}, проверок ${row.check_count}` : '';
    return el('article', { className: 'sp-card is-text registry-card' },
        el('header', {}, el('strong', { textContent: row.name || row.source_id }),
            el('span', { className: 'source-status', textContent: ` ${row.platform || ''} · ${STATUS_LABEL[row.status] || row.status}` })),
        row.reason ? el('p', { className: 'registry-reason', textContent: row.reason }) : null,
        lines.length ? el('ul', { className: 'registry-topics' }, ...lines.map(text => el('li', { textContent: text }))) : null,
        el('p', { className: 'registry-meta', textContent: [checked, origin].filter(Boolean).join(' · ') }),
        actions);
}

function render() {
    const rows = registry.rows;
    $('registry-count').textContent = rows.length ? `${rows.length}` : '';
    $('registry-status-line').textContent = rows.length ? '' : 'Ничего не найдено: реестр пуст или фильтры слишком строгие.';
    $('registry-list').replaceChildren(...rows.map(registryCard));
}

function countsLine(counts) {
    const parts = Object.keys(STATUS_LABEL).filter(k => counts[k]).map(k => `${STATUS_LABEL[k]} ${counts[k]}`);
    return parts.length ? `Всего в реестре: ${parts.join(', ')}.` : '';
}

export async function loadRegistry() {
    if (!$('registry-list')) return;
    $('registry-status-line').textContent = 'Загружаю…';
    try {
        const data = await api('registry_list', { params: filters() });
        registry.rows = data.rows || [];
        render();
        $('registry-counts').textContent = countsLine(data.counts || {});
    } catch (error) {
        $('registry-status-line').textContent = `Не удалось загрузить реестр: ${error.message}`;
    }
}

async function readd(row) {
    try {
        await api('registry_readd', { body: { source_id: row.source_id } });
        log(`«${row.name}» снова в предложениях этой темы.`);
        await loadRegistry();
    } catch (error) { log(`Не удалось вернуть источник: ${error.message}`); }
}

async function forget(row) {
    if (!(await ask('При следующем подборе он может найтись снова.', { title: `Забыть «${row.name || row.source_id}»?`, ok: 'Забыть', danger: true }))) return;
    try {
        await api('registry_forget', { body: { source_id: row.source_id } });
        log(`«${row.name || row.source_id}» убран из реестра.`);
        await loadRegistry();
    } catch (error) { log(`Не удалось забыть источник: ${error.message}`); }
}

export function initRegistry() {
    if (!$('registry-list')) return;
    const statusSelect = $('registry-status');
    Object.entries(STATUS_LABEL).forEach(([value, label]) => statusSelect.append(el('option', { value, textContent: label })));
    const platformSelect = $('registry-platform');
    PLATFORMS.forEach(name => platformSelect.append(el('option', { value: name, textContent: name })));
    statusSelect.addEventListener('change', loadRegistry);
    platformSelect.addEventListener('change', loadRegistry);
    $('registry-only-topic').addEventListener('change', loadRegistry);
    $('registry-search').addEventListener('input', () => {
        clearTimeout(searchTimer);
        searchTimer = setTimeout(loadRegistry, 300);
    });
    $('registry-refresh').addEventListener('click', loadRegistry);
}
