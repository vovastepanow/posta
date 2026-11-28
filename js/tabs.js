// Дерево тем и отдельный тест оценки изображения.
import { $, ask, DEBUG_MODE, api, el, hide, isTabActive, log, onTabOpen, onTopicChange, renderTags, setMessage, show } from './core.js';
import { TOPIC_CONFIRM, loadCurrentItem, refreshTopicLabel, saveTopic } from './content.js';
import { buildTopicTree, topicTags, treeNorm } from './topic-tree-model.js';

// ---- «Дерево»: рубрики (общие темы) слева, карточки частных тем по центру, паспорт выбранной темы справа ----
// Раскладку выбирает ширина самой вкладки (data-width на #tree-tab): консоль занимает левую колонку, поэтому по окну не угадать.

const tree = { topics: [], current: null, selected: null, rubric: null, query: '', busy: false, parentOpen: false, width: 'narrow', sheetOpen: false, tagsOpen: null };
const RUBRIC_KEY = 'postaTreeRubric';
const RUBRIC_ICONS = { adult: '🔞', shock: '💥', humor: '😂', internet: '🌐', animals: '🐾', people: '👥', games: '🎮', screen: '🎬', music: '🎵', celebs: '⭐', news: '📰', tech: '💻', money: '💰', business: '💼', finance: '💰', self: '🧠', home: '🏡', health: '🩺', culture: '🎭', design: '✏️', environment: '🌱', memes: '😄', expert: '🎓', science: '🔬', food: '🍔', travel: '✈️', style: '👗', cars: '🚗', transport: '🚗', hobby: '🎨', sport: '⚽', other: '📁' };

// Rebuild the immutable index only when the server replaces the topic list.
let indexedTopics, index;
function treeModel() {
    if (indexedTopics !== tree.topics) {
        indexedTopics = tree.topics;
        index = buildTopicTree(indexedTopics);
    }
    return index;
}

/** Текст с подсветкой найденного: массив узлов и строк для el(). Совпадение ищем по строке без замен длины. */
function treeHighlight(text, q) {
    const raw = String(text ?? '');
    const at = q ? raw.toLowerCase().replace(/ё/g, 'е').replace(/[\s-]/g, '_').indexOf(q) : -1;
    return at < 0 ? [raw] : [raw.slice(0, at), el('mark', { textContent: raw.slice(at, at + q.length) }), raw.slice(at + q.length)];
}

function readRubric() { try { return localStorage.getItem(RUBRIC_KEY); } catch { return null; } }
function rememberRubric(id) { try { localStorage.setItem(RUBRIC_KEY, id); } catch { /* приватный режим: просто не запомним */ } }

function setTreeWidth(width) {
    tree.width = width;
    $('tree-tab').dataset.width = width;
    updateTreeSheet();
}

/** Нижний лист на телефоне: открыт только в узкой раскладке и только после выбора карточки. */
function updateTreeSheet() {
    const open = tree.width === 'narrow' && tree.sheetOpen;
    $('tree-detail').classList.toggle('is-open', open);
    $('tree-backdrop').hidden = !open;
    document.body.classList.toggle('tree-sheet-open', open);
}

function closeTreeSheet() { tree.sheetOpen = false; updateTreeSheet(); }

function selectTopic(id, fromCard = false) {
    const model = treeModel();
    const t = model.ids.get(id);
    if (!t) return;
    Object.assign(tree, { selected: id, rubric: model.rubricOf(t).id, parentOpen: false });
    rememberRubric(tree.rubric);
    if (fromCard) tree.sheetOpen = tree.width === 'narrow';
    renderTree();
    renderDetail();
    updateTreeSheet();
}

/** Рубрика: её карточки и она сама в паспорте. */
function pickRubric(id) {
    Object.assign(tree, { rubric: id, selected: id });
    rememberRubric(id);
    renderTree();
    renderDetail();
}

function renderRubrics(model, q, rubricId, liveRubricId) {
    const rubrics = model.rubrics.map(r => {
        const members = model.descendants(r);
        const found = q ? members.filter(t => model.matches(t, q)) : members.slice(1);
        return { r, found };
    }).filter(({ found }) => !q || found.length);
    $('tree-rubrics').replaceChildren(...rubrics.map(({ r, found }) => {
        const live = r.id === liveRubricId;
        return el('button', { type: 'button', className: ['tree-rubric', r.id === rubricId && 'is-active'].filter(Boolean).join(' '), title: r.about || r.title,
                'aria-pressed': String(r.id === rubricId), onclick: () => pickRubric(r.id) },
            el('span', { className: 'tree-rubric-icon', textContent: RUBRIC_ICONS[r.key] ?? '📁', 'aria-hidden': 'true' }),
            el('span', { className: 'tree-rubric-name', textContent: r.title }),
            el('span', { className: 'tree-rubric-count', textContent: String(found.length), title: q ? 'Найдено' : 'Частных тем' }),
            live ? el('span', { className: 'tree-air', textContent: 'В ЭФИРЕ' }) : null);
    }));
}

function treeCard(t, model, q) {
    const rubric = model.rubricOf(t);
    const whole = t.id === rubric.id;
    const isCurrent = tree.current === t.id;
    const nsfw = t.nsfw || rubric.nsfw;
    const tags = topicTags(t);
    const classes = ['tree-card', isCurrent && 'is-current', tree.selected === t.id && 'is-selected'].filter(Boolean).join(' ');
    return el('button', { type: 'button', className: classes, title: t.about || t.title, 'aria-pressed': String(tree.selected === t.id), onclick: () => selectTopic(t.id, true) },
        whole ? el('span', { className: 'tree-card-caption', textContent: 'Вся рубрика' }) : null,
        el('span', { className: 'tree-card-head' },
            el('span', { className: 'tree-card-name' }, ...treeHighlight(whole ? rubric.title : t.title, q)),
            nsfw ? el('span', { className: 'tree-badge is-18', textContent: '18+' }) : null,
            isCurrent ? el('span', { className: 'tree-air', textContent: '📍 В ЭФИРЕ' }) : null),
        el('span', { className: 'tree-card-about' }, ...treeHighlight(t.about, q)),
        tags.length ? el('span', { className: 'tree-card-tags' }, ...tags.slice(0, 3).map(tag => el('span', { className: 'tree-tag', textContent: `#${tag}` })),
            tags.length > 3 ? el('span', { className: 'tree-tag is-more', textContent: `+${tags.length - 3}` }) : null) : null,
        el('span', { className: t.own_sources ? 'tree-card-src' : 'tree-card-src is-muted', textContent: t.own_sources ? `📡 ${t.own_sources} своих` : 'источники рубрики',
            title: t.own_sources ? 'Свои источники темы' : 'Своих нет: берутся источники общей темы' }));
}

/** Карточки: при поиске — совпадения по всем рубрикам с заголовками, без поиска — выбранная рубрика целиком. Возвращает число карточек. */
function renderCards(model, q, rubricId) {
    const grid = (items) => el('div', { className: 'tree-grid' }, ...items.map(t => treeCard(t, model, q)));
    if (q) {
        const blocks = model.rubrics.map(r => [r, model.descendants(r).filter(t => model.matches(t, q))])
            .filter(([, hits]) => hits.length)
            .map(([r, hits]) => el('section', { className: 'tree-block-cards' }, el('h3', { className: 'tree-cards-group', textContent: `${r.title} · найдено ${hits.length}` }), grid(hits)));
        $('tree-cards').replaceChildren(...blocks);
        return blocks.length;
    }
    const rubric = model.ids.get(rubricId);
    if (!rubric) { $('tree-cards').replaceChildren(); return 0; }
    const kids = model.descendants(rubric).slice(1);
    $('tree-cards').replaceChildren(
        el('div', { className: 'tree-cards-head' }, el('h2', { className: 'tree-rubric-title', textContent: rubric.title }), rubric.about ? el('p', { className: 'tree-rubric-about', textContent: rubric.about }) : null),
        grid([rubric, ...kids]));
    return kids.length + 1;
}

function renderTree() {
    const q = treeNorm(tree.query);
    const model = treeModel();
    if (!model.ids.has(tree.rubric)) tree.rubric = model.rubrics[0]?.id ?? null;
    const current = model.ids.get(tree.current);
    const liveRubric = current ? model.rubricOf(current).id : null;
    renderRubrics(model, q, tree.rubric, liveRubric);
    const cardCount = renderCards(model, q, tree.rubric);
    $('tree-current-name').textContent = current?.title || '—';
    if (!tree.topics.length) setMessage('tree-status', 'Тем пока нет: введите первую тему в поле выше.');
    else if (q && !cardCount) setMessage('tree-status', `Ничего не найдено. Enter — перейти к теме или создать «${tree.query.trim()}».`);
    else setMessage('tree-status', '');
}

/** Строка паспорта: подпись и значение, пустые строки не выводим. */
const treeRow = (label, value) => value ? el('div', { className: 'tree-row' }, el('dt', { textContent: label }), el('dd', {}, value)) : null;

function renderDetail() {
    const close = el('button', { type: 'button', className: 'tree-sheet-close', textContent: '✕', title: 'Закрыть', 'aria-label': 'Закрыть паспорт темы', onclick: closeTreeSheet });
    const model = treeModel();
    const t = model.ids.get(tree.selected);
    if (!t) return $('tree-detail').replaceChildren(close, el('p', { className: 'tree-detail-empty', textContent: 'Выберите тему: здесь её описание, хэштеги и действия.' }));
    const detail = { record: null };
    const getRecord = () => detail.record ??= api('topic_record', { params: { id: t.id } }).catch(error => { detail.record = null; throw error; });
    const rubric = model.rubricOf(t);
    const isCurrent = tree.current === t.id;
    const children = model.children.get(t.id) || [];
    const note = el('span', { className: 'tree-copied', role: 'status' });
    const sources = t.uses_parent_sources ? `${t.own_sources || 0} своих. Пока берутся источники общей темы «${t.parent.title}».`
        : t.own_sources ? `${t.own_sources} своих — по ним идёт сбор.` : 'Своих пока нет: автоподбор ищет их, как только тема становится текущей.';
    const profile = t.profile ? `составлен ${t.profile.at ? new Date(t.profile.at * 1000).toLocaleString('ru-RU') : 'недавно'}, ${t.profile.smart ? 'умной моделью' : 'быстрой моделью'}` : 'ещё не составлен: напишется при следующем запуске сборщика.';
    const action = (className, textContent, onclick, title = '') => el('button', { className, type: 'button', textContent, onclick, title });
    const copy = tag => { copyTreeText(`#${tag}`).then(() => note.textContent = `Скопировано: #${tag}`, () => note.textContent = 'Не удалось скопировать'); clearTimeout(note.timer); note.timer = setTimeout(() => { note.textContent = ''; }, 1500); };
    $('tree-detail').replaceChildren(...[close,
        el('nav', { className: 'tree-crumbs', 'aria-label': 'Путь к теме' }, t.parent
            ? el('span', {}, el('button', { type: 'button', className: 'tree-crumb', textContent: rubric.title, onclick: () => pickRubric(rubric.id) }), ' › ', ...(t.parent.id !== rubric.id ? [el('button', { type: 'button', className: 'tree-crumb', textContent: t.parent.title, onclick: () => selectTopic(t.parent.id) }), ' › '] : []), t.title)
            : el('span', { textContent: 'Рубрика' })),
        el('h3', { className: 'tree-detail-title', textContent: t.title }),
        el('div', { className: 'tree-badges' },
            el('span', { className: 'tree-badge', textContent: t.scope === 'broad' ? 'общая тема' : 'частная тема' }),
            t.nsfw || rubric.nsfw ? el('span', { className: 'tree-badge is-18', textContent: '18+' }) : null,
            isCurrent ? el('span', { className: 'tree-badge is-current', textContent: '📍 сейчас выбрана' }) : null),
        isCurrent ? el('p', { className: 'tree-detail-empty', textContent: 'Эта тема сейчас в эфире: автоподбор ищет по ней.' })
            : el('button', { className: 'primary-button tree-primary', type: 'button', textContent: 'Сделать текущей', onclick: () => choose(t.title) }),
        t.about ? el('p', { className: 'tree-about', textContent: t.about }) : null,
        hashtagBlock(t, copy, note),
        t.aliases?.length ? el('div', { className: 'tree-block' }, el('h4', { className: 'tree-block-title', textContent: 'Другие названия' }),
            el('div', { className: 'tree-tags' }, ...t.aliases.map(a => el('span', { className: 'tree-alias', textContent: a })))) : null,
        children.length ? el('div', { className: 'tree-block' }, el('h4', { className: 'tree-block-title', textContent: 'Частные темы' }),
            el('div', { className: 'tree-tags' }, ...children.map(c => el('button', { type: 'button', className: 'tree-alias is-button', textContent: c.title, onclick: () => selectTopic(c.id) })))) : null,
        el('p', { className: 'tree-sources', textContent: sources }),
		el('div', { className: 'tree-block tree-health' }, el('h4', { className: 'tree-block-title', textContent: 'Как работает тема' }), el('p', { className: 'tree-detail-empty', textContent: 'Загружаю показатели…' })),
        el('details', { className: 'tree-ai' }, el('summary', { textContent: 'Как ИИ отбирает' }),
            el('dl', { className: 'tree-facts' }, treeRow('Подходит', t.criteria || t.about), treeRow('Границы темы', t.hint), treeRow('Не подходит', t.exclude), treeRow('Проверка темы', t.question), treeRow('Профиль', profile))),
        el('div', { className: 'tree-actions' },
            action('secondary-button tree-small', 'Сменить общую тему', () => { tree.parentOpen = !tree.parentOpen; $('tree-parent-editor').hidden = !tree.parentOpen; if (tree.parentOpen) $('tree-parent-editor').querySelector('select').focus(); }),
            action('secondary-button tree-small', 'Пересоставить профиль', async () => {
                try { await api('topic_reprofile', { body: { id: t.id } }); log(`Профиль темы «${t.title}» пересоставится при следующем запуске сборщика.`); await loadTree(); }
                catch (error) { setMessage('tree-status', `Профиль не сброшен: ${error.message}`); }
            }, 'Критерии и план поиска напишутся заново')),
        parentEditor(t),
        el('details', { className: 'tree-debug', ontoggle: e => { if (e.currentTarget.open) loadTopicRecord(e.currentTarget.querySelector('.tree-record'), getRecord); } },
            el('summary', { textContent: 'Для отладки: полная запись из базы' }),
            el('div', { className: 'tree-record' }, el('p', { className: 'tree-detail-empty', textContent: 'Раскройте, чтобы загрузить запись.' })))].filter(Boolean));
    loadTopicHealth($('tree-detail').querySelector('.tree-health'), getRecord);
}

async function loadTopicHealth(box, getRecord) {
	try {
		const { health } = await getRecord();
		if (!box?.isConnected) return;
		const values = [
			['Источники', health.sources], ['Кандидаты автоподбора', health.candidates],
			['Проверено источников', health.sources_checked], ['Проанализировано постов', health.analyzed],
			['Достигли порога темы', health.relevant], ['Сейчас в подборе', health.pool],
			['Понравились', health.liked], ['Не понравились', health.disliked], ['Опубликованы', health.published],
		];
		box.replaceChildren(el('h4', { className: 'tree-block-title', textContent: 'Как работает тема' }),
			el('dl', { className: 'tree-facts' }, ...values.map(([label, value]) => treeRow(label, String(value ?? '—')))));
	} catch {
		if (box?.isConnected) box.replaceChildren(el('p', { className: 'tree-detail-empty', textContent: 'Показатели сейчас недоступны.' }));
	}
}

/** Хэштеги темы: рубрика (одна, её ставим в посты) и имена; клик копирует. «Изменить» — своя рубрика вместо словарной. */
function hashtagBlock(t, copy, note) {
    const h = t.hashtags || {};
    const edit = tree.tagsOpen === t.id;
    const save = async rubric => {
        try {
            await api('topic_rubric', { body: { id: t.id, rubric } });
            tree.tagsOpen = null;
            await loadTree();
        } catch (error) { setMessage('tree-status', `Рубрика не сохранена: ${error.message}`); }
    };
    const chip = (tag, extra = '') => el('button', { type: 'button', className: `tree-tag is-button ${extra}`.trim(), textContent: `#${tag}`, title: `Скопировать #${tag}`, onclick: () => copy(tag) });
    const toggle = h.rubric_default ? el('button', { type: 'button', className: 'tree-link', textContent: edit ? 'Отмена' : 'Изменить', onclick: () => { tree.tagsOpen = edit ? null : t.id; renderDetail(); } }) : null;
    const title = el('h4', { className: 'tree-block-title' }, h.rubric ? 'Хэштеги · рубрика ставится в посты' : 'Хэштегов нет: рубрика есть у частных тем', ' ', toggle);
    const synonyms = h.synonyms?.length ? el('details', { className: 'tree-tag-synonyms' }, el('summary', { textContent: `Узнаём ещё по ${h.synonyms.length} похожим тегам` }),
        el('p', { textContent: h.synonyms.map(x => `#${x}`).join(' ') })) : null;
    if (!edit) return el('div', { className: 'tree-block' }, title,
        h.rubric ? el('div', { className: 'tree-tags' }, chip(h.rubric, 'is-rubric'), ...(h.entities || []).map(tag => chip(tag))) : null,
        h.rubric && h.rubric !== h.rubric_default ? el('p', { className: 'tree-detail-empty', textContent: `Ваша рубрика. В словаре — #${h.rubric_default}.` }) : null, synonyms, note);
    const input = el('input', { className: 'tree-tag-input', type: 'text', maxLength: 17, value: h.rubric || '', placeholder: 'рубрика одним словом', autocomplete: 'off', ariaLabel: 'Своя рубрика' });
    const submit = () => { input.disabled = true; save(input.value.replace(/^#+/, '').trim()); };
    input.onkeydown = e => { if (e.key === 'Enter') { e.preventDefault(); submit(); } };
    return el('div', { className: 'tree-block' }, title,
        el('div', { className: 'tree-tag-add' }, input, el('button', { type: 'button', className: 'secondary-button tree-small', textContent: 'Сохранить', onclick: submit }),
            h.rubric !== h.rubric_default ? el('button', { type: 'button', className: 'secondary-button tree-small', textContent: 'Вернуть из словаря', onclick: () => save('') }) : null));
}

async function copyTreeText(text) {
    if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(text);
    const area = el('textarea', { value: text });
    document.body.append(area);
    area.select();
    const copied = document.execCommand('copy');
    area.remove();
    if (!copied) throw new Error('буфер недоступен');
}

const TOPIC_LABELS = { id: 'ID', title: 'Название', scope: 'Тип', parent: 'Общая тема', aliases: 'Ещё называют', about: 'Описание', classified: 'Классифицирована', smart: 'Классифицирована ИИ', manual: 'Создана вручную', created_at: 'Создана', hashtags: 'Хэштеги' };
const PROFILE_LABELS = { criteria: 'Подходит', exclude: 'Не подходит', question: 'Вопрос к картинке', queries: 'Поисковые запросы', telegram: 'Поиск в Telegram', v: 'Версия профиля', smart: 'Профиль составлен ИИ', at: 'Профиль составлен' };
const TIME_FIELDS = ['created_at', 'at'];

/** Значение поля для показа: читаемо, а сложные структуры целиком, как JSON. */
function recordValue(key, value) {
    if (value === null || value === undefined || value === '') return '—';
    if (typeof value === 'boolean') return value ? 'да' : 'нет';
    if (TIME_FIELDS.includes(key)) return value ? new Date(value * 1000).toLocaleString('ru-RU') : '—';
    if (key === 'scope') return value === 'broad' ? 'общая тема' : 'частная тема';
    if (Array.isArray(value) && value.every(x => x === null || typeof x !== 'object')) return value.length ? value.join(', ') : '—';
    if (typeof value === 'object') return el('pre', { textContent: JSON.stringify(value, null, 2) });
    return String(value);
}

/** Все поля записи темы из базы, включая профиль. Неизвестные поля тоже выводятся, под своим именем. */
function recordRows(topic) {
    const rows = [];
    const add = (label, key, value) => rows.push(el('div', { className: 'tree-row' }, el('dt', { textContent: label }), el('dd', {}, recordValue(key, value))));
    for (const [key, value] of Object.entries(topic)) {
        if (key === 'profile') continue;
        const shown = key === 'parent' && value ? (tree.topics.find(x => x.id === value)?.title ?? value) : value;
        add(TOPIC_LABELS[key] || key, key, shown);
    }
    for (const [key, value] of Object.entries(topic.profile || {})) add(PROFILE_LABELS[key] || `Профиль: ${key}`, key, value);
    return rows;
}

async function loadTopicRecord(box, getRecord) {
    if (!box || box.dataset.loaded) return;
    box.replaceChildren(el('p', { className: 'tree-detail-empty', textContent: 'Загружаю полную запись…' }));
    try {
        const { topic } = await getRecord();
        if (!box.isConnected) return;
        box.replaceChildren(el('h4', { className: 'tree-record-title', textContent: 'Полная запись из базы' }), el('dl', { className: 'tree-facts' }, ...recordRows(topic)));
        box.dataset.loaded = '1';
    } catch (error) {
        if (box.isConnected) box.replaceChildren(el('p', { className: 'tree-detail-empty', textContent: `Запись не загрузилась: ${error.message}` }));
    }
}

function parentEditor(t) {
    const select = el('select', { className: 'tree-parent-select', ariaLabel: 'Общая тема' }, el('option', { value: '', textContent: '— это сама общая тема —' }),
        ...tree.topics.filter(x => x.scope === 'broad' && x.id !== t.id).map(x => el('option', { value: x.id, textContent: x.title })), el('option', { value: '__new', textContent: 'Новая общая тема…' }));
    select.value = t.scope === 'broad' ? '' : t.parent?.id || '__new';
    const input = el('input', { className: 'tree-parent-new', type: 'text', maxLength: 40, placeholder: 'Название новой общей темы', autocomplete: 'off', hidden: select.value !== '__new' });
    select.onchange = () => { input.hidden = select.value !== '__new'; if (!input.hidden) input.focus(); };
    const save = el('button', { className: 'secondary-button', type: 'button', textContent: 'Готово' });
    save.onclick = async () => {
        const parent = select.value === '__new' ? input.value.trim() : select.value;
        if (select.value === '__new' && !parent) return input.focus();
        save.disabled = true;
        try {
            const { topic_info: i } = await api('topic_parent', { body: { id: t.id, parent } });
            log(i.parent ? `Тема «${i.title}» теперь внутри «${i.parent.title}».` : `Тема «${i.title}» теперь общая.`);
            await loadTree();
        } catch (error) { setMessage('tree-status', `Общая тема не сохранена: ${error.message}`); }
        finally { save.disabled = false; }
    };
    input.onkeydown = e => { if (e.key === 'Enter') { e.preventDefault(); save.click(); } };
    return el('div', { className: 'tree-parent-editor', id: 'tree-parent-editor', hidden: !tree.parentOpen }, select, input, save);
}

function setTreeBusy(busy, message = '') {
    tree.busy = $('tree-save').disabled = $('tree-input').disabled = busy;
    $('tree-layout').classList.toggle('is-busy', busy);
    setMessage('tree-status', message);
}

async function choose(text, forceNew = false) {
    const typed = text.trim();
    const current = tree.topics.find(t => t.id === tree.current);
    if (!typed || tree.busy || (!forceNew && current?.title.toLowerCase() === typed.toLowerCase()) || !(await ask(TOPIC_CONFIRM, { ok: 'Сменить тему' }))) return;
    hide('tree-notice');
    setTreeBusy(true, 'Нейросеть определяет тему: название и общую тему (до минуты)…');
    try {
        const data = await saveTopic(typed, forceNew, 'tree-notice');
        $('tree-input').value = '';
        tree.query = '';
        tree.current = tree.selected = data.topic_info?.id ?? null;
        await loadTree({ reveal: true });
        await refreshTopicLabel();
        await loadCurrentItem();
    } catch (error) { setMessage('tree-status', `Тема не сохранена: ${error.message}`); }
    finally { setTreeBusy(false); }
}

/** reveal: открыли вкладку или сменили тему — показываем текущую тему и её рубрику; иначе сохраняем выбор. */
let treeRequest = 0;
async function loadTree({ reveal = false } = {}) {
    if (DEBUG_MODE) return setMessage('tree-status', 'В демо-режиме дерево тем не показывается.');
    const request = ++treeRequest;
    try {
        const data = await api('topics');
        if (request !== treeRequest) return;
        Object.assign(tree, { topics: data.topics || [], current: data.current ?? null });
        if (reveal) {
            const model = treeModel();
            const current = model.ids.get(tree.current);
            tree.selected = tree.current;
            tree.rubric = current ? model.rubricOf(current).id : readRubric();
        } else if (!tree.topics.some(t => t.id === tree.selected)) tree.selected = tree.current;
        $('tree-options').replaceChildren(...tree.topics.map(t => el('option', { value: t.title, label: t.path })));
        renderTree();
        renderDetail();
    } catch (error) { if (request === treeRequest) setMessage('tree-status', `Не удалось загрузить темы: ${error.message}`); }
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

    $('tree-form').addEventListener('submit', e => { e.preventDefault(); choose($('tree-input').value); });
    $('tree-force-new').addEventListener('click', e => choose(e.currentTarget.dataset.typed || $('tree-input').value, true));
    $('tree-refresh').addEventListener('click', () => loadTree());
    if (DEBUG_MODE) { hide('tree-form'); hide('tree-refresh'); }
    const search = $('tree-input');
    search.addEventListener('input', () => { tree.query = search.value; renderTree(); });   // фильтр на лету: поле и фокус не трогаем
    search.addEventListener('keydown', e => { if (e.key === 'Escape' && search.value) { e.preventDefault(); search.value = ''; tree.query = ''; renderTree(); } });
    $('tree-backdrop').addEventListener('click', closeTreeSheet);
    document.addEventListener('keydown', e => { if (e.key === 'Escape' && tree.sheetOpen) closeTreeSheet(); });
    new ResizeObserver(([entry]) => { const w = entry.contentRect.width; setTreeWidth(w >= 1000 ? 'wide' : w >= 700 ? 'mid' : 'narrow'); }).observe($('tree-tab'));
    onTabOpen('tree-tab', () => loadTree({ reveal: true }));
    // Тему сменили в шапке — открытые «Предложка» и «Дерево» перерисовываются под неё
    onTopicChange(() => { if (isTabActive('tree-tab')) loadTree({ reveal: true }); });

    $('image-file').addEventListener('change', e => { if (e.target.files[0]) $('image-url').value = ''; setPreview(e.target.files[0] || null); hide('test-result'); });
    $('image-url').addEventListener('input', () => { if ($('image-url').value) { $('image-file').value = ''; setPreview(null); } });
    $('analyze-form').addEventListener('submit', analyze);
}
