// Independent search profiles and the image assessment tool.
import { $, DEBUG_MODE, api, el, hide, isTabActive, onTabOpen, onTopicChange, renderTags, setMessage, show } from './core.js';
import { loadCurrentItem, refreshTopicLabel, saveTopic } from './content.js';
import { filterSearchProfiles } from './search-profile-model.js';

let profiles = [], currentProfile = null, selectedProfile = null;
let request = 0, busy = false;

function renderProfiles() {
    const found = filterSearchProfiles(profiles, $('tree-input').value, currentProfile);
    $('tree-current-name').textContent = profiles.find(p => p.id === currentProfile)?.title || '—';
    $('tree-cards').replaceChildren(...found.map(p =>
        el('button', { type: 'button', className: 'tree-card' + (p.id === currentProfile ? ' is-current' : '') + (p.id === selectedProfile ? ' is-selected' : ''),
            'aria-pressed': String(p.id === selectedProfile), onclick: () => { selectedProfile = p.id; renderProfiles(); renderProfileDetail(); } },
            el('span', { className: 'tree-card-name', textContent: p.title }),
            el('span', { className: 'tree-card-src', textContent: p.id === currentProfile ? 'Текущий запрос' : 'Источников: ' + (p.own_sources ?? 0) }),
            p.nsfw ? el('span', { className: 'tree-badge is-18', textContent: '18+' }) : null)));
    if (!busy) setMessage('tree-status', found.length ? '' : 'Введите свой запрос и нажмите «Выбрать».');
}

function fact(label, value) {
    return el('div', { className: 'tree-row' }, el('dt', { textContent: label }), el('dd', { textContent: String(value ?? '—') }));
}

async function renderProfileDetail() {
    const box = $('tree-detail'), p = profiles.find(p => p.id === selectedProfile);
    if (!p) { box.replaceChildren(el('p', { className: 'tree-detail-empty', textContent: 'Выберите поисковый запрос.' })); return; }
    const stats = el('div', { className: 'tree-health' }, 'Загружаю показатели…');
    box.replaceChildren(el('h3', { className: 'tree-detail-title', textContent: p.title }),
        p.id !== currentProfile ? el('button', { type: 'button', className: 'primary-button tree-primary', textContent: 'Искать по этому запросу', onclick: () => chooseProfile(p.title) }) : el('p', { className: 'tree-detail-empty', textContent: 'Этот запрос сейчас активен.' }),
        p.criteria ? el('dl', { className: 'tree-facts' }, fact('Подходит', p.criteria), p.exclude ? fact('Не подходит', p.exclude) : null) : null,
        stats);
    try {
        const { health } = await api('topic_record', { params: { id: p.id } });
        if (!stats.isConnected) return;
        stats.replaceChildren(el('dl', { className: 'tree-facts' },
            ...[['Источники','sources'],['Кандидаты источников','candidates'],['Проверено источников','sources_checked'],
                ['Оценено постов','analyzed'],['Подходят','relevant'],['В личной подборке','pool'],
                ['Понравились','liked'],['Не понравились','disliked'],['Опубликованы','published']].map(([label,key])=>fact(label,health?.[key]))));
    } catch { if (stats.isConnected) stats.textContent = 'Показатели сейчас недоступны.'; }
}

async function loadProfiles() {
    if (DEBUG_MODE) return;
    const seq = ++request;
    try {
        const data = await api('topics');
        if (seq !== request) return;
        profiles = data.topics || [];
        currentProfile = data.current;
        if (!profiles.some(p => p.id === selectedProfile)) selectedProfile = currentProfile;
        $('tree-options').replaceChildren(...profiles.map(p => el('option', { value: p.title })));
        renderProfiles();
        renderProfileDetail();
    } catch (error) { if (seq === request) setMessage('tree-status', 'Не удалось загрузить запросы: ' + error.message); }
}

async function chooseProfile(text) {
    const typed = text.trim();
    if (!typed || busy) return;
    busy = true;
    $('tree-save').disabled = $('tree-input').disabled = true;
    setMessage('tree-status', 'Сохраняю запрос…');
    try {
        const result = await saveTopic(typed, false, 'tree-notice');
        $('tree-input').value = '';
        selectedProfile = result.topic_info?.id;
        await loadProfiles();
        await refreshTopicLabel();
        await loadCurrentItem();
        setMessage('tree-status', '');
    } catch (error) { setMessage('tree-status', 'Не удалось сохранить запрос: ' + error.message); }
    finally { busy = false; $('tree-save').disabled = $('tree-input').disabled = false; }
}

// ---- «Тест ИИ» ----

let previewUrl = '';
function setPreview(source) {
    if (previewUrl) URL.revokeObjectURL(previewUrl);
    previewUrl = source instanceof File ? URL.createObjectURL(source) : '';
    const url = previewUrl || (typeof source === 'string' ? source : '');
    if (url) $('test-image').src = url; else $('test-image').removeAttribute('src');
}

async function analyze(e) {
    e.preventDefault();
    const [file, url] = [$('image-file').files[0], $('image-url').value.trim()];
    setMessage('test-error', '');
    hide('test-result');
    if (!file && !url) return setMessage('test-error', 'Добавьте ссылку или выберите файл изображения.');
    if (file?.size > 10 * 1024 * 1024) return setMessage('test-error', 'Файл больше 10 МБ. Выберите изображение поменьше.');
    $('analyze-button').disabled = true;
    show('test-loading');
    try {
        const body = new FormData();
        file ? body.append('image_file', file) : body.append('image_url', url);
        const d = await api('analyze_image', { body });
        if (!file) setPreview(url);
        const a = d.analysis;
        setMessage('test-rating', d.accepted ? 'Принято' : 'Отклонено');
        const score = d.prescreen_score == null ? 'нет ответа, переход к полному анализу' : `${d.prescreen_score}% (порог ${d.prescreen_threshold}%)`;
        $('test-model').textContent = `Тема «${d.topic}» · быстрая проверка: ${d.prescreen_model} · мусор: ${score}${a ? ` · анализ: ${a.model}` : ''}`;
        $('test-relevance').textContent = a ? `По теме: ${Number(a.relevance)}%` : 'Полный анализ не запускался';
        renderTags($('test-tags'), d.tags_info || []);
        $('test-description').replaceChildren(el('strong', { textContent: 'Решение:' }), ` ${d.accepted ? 'прошло проверки' : d.reason}`);
        $('test-why').textContent = a ? [a.description, a.why_relevant && `Почему по теме: ${a.why_relevant}`, a.meme_kind && `Тип мема: ${a.meme_kind}`, a.joke && `Шутка: ${a.joke}`].filter(Boolean).join(' · ') : '';
        show('test-result');
    } catch (error) { setMessage('test-error', `Не удалось оценить изображение: ${error.message}`); }
    finally { hide('test-loading'); $('analyze-button').disabled = false; }
}

export function initializeTabs() {

    $('tree-form').addEventListener('submit', e => { e.preventDefault(); chooseProfile($('tree-input').value); });
    $('tree-refresh').addEventListener('click', loadProfiles);
    $('tree-input').addEventListener('input', renderProfiles);
    onTabOpen('tree-tab', loadProfiles);
    onTopicChange(() => { if (isTabActive('tree-tab')) loadProfiles(); });
    if (DEBUG_MODE) { hide('tree-form'); hide('tree-refresh'); }

    $('image-file').addEventListener('change', e => { if (e.target.files[0]) $('image-url').value = ''; setPreview(e.target.files[0] || null); hide('test-result'); });
    $('image-url').addEventListener('input', () => { if ($('image-url').value) { $('image-file').value = ''; setPreview(null); } });
    $('analyze-form').addEventListener('submit', analyze);
}
