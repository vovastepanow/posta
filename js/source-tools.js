// On-demand source tools: importing personal content, selecting best posts and mirroring.
import { $, COUNT, api, el, log, safeSourcePostUrl } from './core.js';
import { loadCurrentItem } from './content.js';
import { prepareOwnImage } from './image-prepare.js';

// ---- Свой контент: картинки и CSV сразу в подборку ----

/** Отправка файлов; итог — строка статуса и ошибки списком (textContent). */
async function sendOwn(action, form, button) {
    const [status, errors] = [$('own-content-status'), $('own-content-errors')];
    button.disabled = true;
    status.textContent = 'Загружаю…';
    errors.replaceChildren();
    try {
        const result = await api(action, { body: form });
        status.textContent = `Добавлено в подборку: ${result.added}` + (result.skipped ? `, пропущено: ${result.skipped}` : '');
        errors.replaceChildren(...(result.errors || []).map(text => el('li', { textContent: text })));
        if (result.added) { log(`Свой контент: добавлено в подборку — ${result.added}.`); loadCurrentItem(); }
    } catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; }
}

/** Образец CSV собирается на клиенте. */
function downloadCsvSample(event) {
    event.preventDefault();
    const text = '\uFEFFtext;image\r\nПодпись к первому посту;https://example.com/photo1.jpg\r\nВторой пост с двумя картинками;https://example.com/photo2.jpg|https://example.com/photo3.jpg\r\n';
    const link = el('a', { href: URL.createObjectURL(new Blob([text], { type: 'text/csv;charset=utf-8' })), download: 'posta-example.csv' });
    link.click();
    URL.revokeObjectURL(link.href);
}

function initializeOwnContent() {
    const [files, csv] = [$('own-files'), $('own-csv')];
    $('own-upload-button').addEventListener('click', () => files.click());
    $('own-csv-button').addEventListener('click', () => csv.click());
    $('own-csv-sample').addEventListener('click', downloadCsvSample);
    files.addEventListener('change', async () => {
        if (!files.files.length) return;
        const selected = [...files.files];
        $('own-upload-button').disabled = true;
        $('own-content-status').textContent = 'Подготавливаю картинки на вашем устройстве…';
        const form = new FormData();
        try {
            for (const file of selected) form.append('files[]', await prepareOwnImage(file));
            form.append('text', $('own-text').value.trim());
            form.append('album', $('own-album').checked ? '1' : '0');
            await sendOwn('own_upload', form, $('own-upload-button'));
        } catch (error) {
            $('own-content-status').textContent = error.message;
        } finally {
            $('own-upload-button').disabled = false;
            files.value = '';
        }
    });
    csv.addEventListener('change', () => {
        if (!csv.files.length) return;
        const form = new FormData();
        form.append('csv', csv.files[0]);
        sendOwn('own_import_csv', form, $('own-csv-button')).finally(() => { csv.value = ''; });
    });
}

// ---- Лучшие посты источника: поиск по ссылке, выбор и забор в подборку ----

const BEST_POSTS_TAKE_MAX = 20;   // как на сервере
const BEST_SORT_LABELS = { date: 'по дате', likes: 'по лайкам', reposts: 'по репостам', comments: 'по комментариям', views: 'по просмотрам', viral: 'по виральности' };
const bestPosts = { key: '', posts: [], selected: new Set(), request: 0, taking: false };

function updateBestPostsButton() {
    const take = $('best-posts-take');
    take.textContent = `Забрать в подборку (${bestPosts.selected.size})`;
    take.disabled = bestPosts.taking || bestPosts.selected.size === 0;
}

/** Отметка поста; сверх лимита за раз отметка снимается. */
function toggleBestPost(token, check) {
    if (check.checked && bestPosts.selected.size >= BEST_POSTS_TAKE_MAX) {
        check.checked = false;
        $('best-posts-status').textContent = `За раз — не больше ${BEST_POSTS_TAKE_MAX} постов.`;
        return;
    }
    check.checked ? bestPosts.selected.add(token) : bestPosts.selected.delete(token);
    updateBestPostsButton();
}

function bestPostCard(post) {
    const check = el('input', { type: 'checkbox', checked: bestPosts.selected.has(post.token) });
    check.addEventListener('change', () => toggleBestPost(post.token, check));
    const stats = [post.likes != null && `♥ ${COUNT.format(post.likes)}`, post.comments != null && `💬 ${COUNT.format(post.comments)}`,
        post.reposts != null && `↪ ${COUNT.format(post.reposts)}`, post.views != null && `👁 ${COUNT.format(post.views)}`, post.viral > 0 && `виральность ${post.viral}`];
    const date = post.posted_at ? new Date(post.posted_at * 1000).toLocaleDateString('ru-RU') : '';
    const link = safeSourcePostUrl({ source_url: post.source_url, source_key: post.source_key });
    return el('label', { className: 'proposal-card best-post' },
        el('img', { className: 'best-post-image', src: post.image, alt: '', loading: 'lazy', decoding: 'async', referrerPolicy: 'no-referrer' }),
        el('span', { className: 'best-post-meta' }, check, el('span', { textContent: [date, ...stats].filter(Boolean).join(' · ') })),
        el('p', { className: 'best-post-text', textContent: post.text }),
        link && el('a', { href: link, target: '_blank', rel: 'noopener noreferrer', textContent: 'Открыть пост ↗' }));
}

function renderBestPosts() {
    $('best-posts-grid').replaceChildren(...bestPosts.posts.map(bestPostCard));
    $('best-posts-actions').hidden = bestPosts.posts.length === 0;
    updateBestPostsButton();
}

/** Сортировки — только те, что умеет площадка источника. */
function fillBestSorts(sorts, current) {
    const select = $('best-posts-sort');
    select.replaceChildren(...sorts.map(s => el('option', { value: s, textContent: BEST_SORT_LABELS[s] || s })));
    select.value = sorts.includes(current) ? current : 'date';
}

async function searchBestPosts(event) {
    event?.preventDefault();
    const [link, days, sort, type, phrase] = [$('best-posts-link').value.trim(), $('best-posts-days').value, $('best-posts-sort').value, $('best-posts-type').value, $('best-posts-phrase').value.trim()];
    if (!link) return;
    const [button, status] = [$('best-posts-button'), $('best-posts-status')];
    const request = ++bestPosts.request;
    button.disabled = true;
    status.textContent = 'Ищу лучшие посты…';
    try {
        const data = await api('best_posts', { params: { link, days, sort, type, phrase } });
        if (request !== bestPosts.request) return;
        Object.assign(bestPosts, { key: data.key, posts: data.posts || [], selected: new Set() });
        fillBestSorts(data.sorts, data.sort);
        status.textContent = data.posts.length ? `${data.source.name}: постов — ${data.posts.length}` : 'За этот период подходящих постов нет.';
    } catch (error) {
        if (request !== bestPosts.request) return;
        Object.assign(bestPosts, { key: '', posts: [], selected: new Set() });
        status.textContent = error.message;
    } finally {
        if (request === bestPosts.request) {
            button.disabled = false;
            renderBestPosts();
        }
    }
}

function selectAllBestPosts() {
    bestPosts.selected = new Set(bestPosts.posts.slice(0, BEST_POSTS_TAKE_MAX).map(p => p.token));
    renderBestPosts();
}

/** Выбранные посты — в подборку; повторы и нескачанные картинки пропускаются. */
async function takeBestPosts() {
    const [button, status] = [$('best-posts-take'), $('best-posts-status')];
    if (bestPosts.taking || !bestPosts.selected.size) return;
    bestPosts.taking = true;
    button.disabled = true;
    status.textContent = 'Забираю в подборку…';
    const request = bestPosts.request;
    const picked = bestPosts.selected.size;
    try {
        const result = await api('best_posts_take', { body: { key: bestPosts.key, tokens: [...bestPosts.selected] } });
        if (request === bestPosts.request) {
            status.textContent = `Добавлено в подборку: ${result.added}` + (result.added < picked ? ' (часть уже была в подборке или не скачалась)' : '');
            bestPosts.selected = new Set();
            renderBestPosts();
        }
        if (result.added) { log(`Лучшие посты: в подборку — ${result.added}.`); loadCurrentItem(); }
    } catch (error) { if (request === bestPosts.request) status.textContent = error.message; }
    finally { bestPosts.taking = false; updateBestPostsButton(); }
}

function initializeBestPosts() {
    $('best-posts-form').addEventListener('submit', searchBestPosts);
    $('best-posts-sort').addEventListener('change', () => { if ($('best-posts-link').value.trim()) searchBestPosts(); });
    $('best-posts-select-all').addEventListener('click', selectAllBestPosts);
    $('best-posts-take').addEventListener('click', takeBestPosts);
}

// ---- Свой контент: папка и Яндекс Диск ----

const OWN_FOLDER_LIMIT = 1000;            // файлов за один выбор папки (и в сутки на сервере)
const OWN_FILE_MAX = 2 * 1024 * 1024;     // upload_max_filesize на сервере
const OWN_IMAGE_EXT = /\.(jpe?g|png|gif|webp|bmp)$/i;
const OWN_VIDEO_EXT = /\.(mp4|mov|webm|avi|mkv|m4v)$/i;
let ownFolderBusy = false;
let ownFolderStop = false;

/** Файлы папки по одному (лимит загрузки сервера); подпись — из .txt с тем же именем. */
async function importOwnFolder(files) {
    if (ownFolderBusy) return;
    ownFolderBusy = true;
    const [status, errors, stop, button] = [$('own-content-status'), $('own-content-errors'), $('own-folder-stop'), $('own-folder-button')];
    const stem = file => file.webkitRelativePath.replace(/\.[^./]+$/, '');
    const media = files.filter(f => OWN_IMAGE_EXT.test(f.name) || OWN_VIDEO_EXT.test(f.name));
    const captions = new Map(files.filter(f => /\.txt$/i.test(f.name)).map(f => [stem(f), f]));
    const queue = media.slice(0, OWN_FOLDER_LIMIT);
    errors.replaceChildren();
    ownFolderStop = false;
    button.disabled = true;
    stop.hidden = false;
    let added = 0;
    try {
        for (let i = 0; i < queue.length && !ownFolderStop; i++) {
            const original = queue[i];
            const file = await prepareOwnImage(original);
            status.textContent = `Загружено ${i} из ${queue.length}` + (added ? `, в подборку: ${added}` : '');
            if (file.size > OWN_FILE_MAX) {
                errors.append(el('li', { textContent: `${original.name}: файл больше 2 МБ после подготовки.` }));
                continue;
            }
            const note = captions.has(stem(original)) ? (await captions.get(stem(original)).text()).trim() : $('own-text').value.trim();
            const form = new FormData();
            form.append('files[]', file);
            form.append('text', note.slice(0, 1000));
            form.append('album', '0');
            try {
                const result = await api('own_upload', { body: form });
                added += result.added;
                errors.append(...(result.errors || []).map(text => el('li', { textContent: text })));
            } catch (error) { errors.append(el('li', { textContent: `${file.name}: ${error.message}` })); }
        }
        const stopped = ownFolderStop ? ' Остановлено.' : '';
        const rest = media.length > OWN_FOLDER_LIMIT ? ` Загружены первые ${OWN_FOLDER_LIMIT} файлов.` : '';
        status.textContent = `Добавлено в подборку: ${added} из ${queue.length}.${stopped}${rest}`;
        if (added) { log(`Свой контент: из папки в подборку — ${added}.`); loadCurrentItem(); }
    } finally {
        ownFolderBusy = false;
        ownFolderStop = false;
        stop.hidden = true;
        button.disabled = false;
    }
}

/** Импорт картинок с Яндекс Диска: до 100 за нажатие; если остались — повторное нажатие продолжает. */
async function importYadisk() {
    const [input, button, status, errors] = [$('own-yadisk-url'), $('own-yadisk-button'), $('own-content-status'), $('own-content-errors')];
    const url = input.value.trim();
    if (!url) return;
    button.disabled = true;
    status.textContent = 'Импортирую с Яндекс Диска…';
    try {
        const result = await api('own_import_yadisk', { body: { url } });
        status.textContent = `Добавлено в подборку: ${result.added} (проверено ${result.processed} из ${result.total})` + (result.skipped ? `, видео пропущено: ${result.skipped}` : '')
            + (result.done ? '. Папка готова.' : '. Нажмите ещё раз — продолжу с того места.');
        errors.replaceChildren(...(result.errors || []).map(text => el('li', { textContent: text })));
        if (result.added) { log(`Свой контент: с Яндекс Диска в подборку — ${result.added}.`); loadCurrentItem(); }
    } catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; }
}

/** Твиты X по ссылкам (до 20 за нажатие, до 100 в сутки). */
async function importX() {
    const [input, button, status, errors] = [$('own-x-links'), $('own-x-button'), $('own-content-status'), $('own-content-errors')];
    const links = input.value.split('\n').map(s => s.trim()).filter(Boolean);
    if (!links.length) return;
    button.disabled = true;
    status.textContent = 'Загружаю твиты…';
    try {
        const result = await api('own_import_x', { body: { links } });
        status.textContent = `Добавлено в подборку: ${result.added}` + (result.skipped ? `, пропущено: ${result.skipped}` : '');
        errors.replaceChildren(...(result.errors || []).map(([link, text]) => el('li', { textContent: `${link}: ${text}` })));
        if (result.added) { log(`Свой контент: твиты X в подборку — ${result.added}.`); input.value = ''; loadCurrentItem(); }
    } catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; }
}

function initializeOwnFolder() {
    const folder = $('own-folder');
    $('own-folder-button').addEventListener('click', () => folder.click());
    $('own-folder-stop').addEventListener('click', () => { ownFolderStop = true; });
    folder.addEventListener('change', () => { const files = Array.from(folder.files); folder.value = ''; if (files.length) importOwnFolder(files); });
    $('own-yadisk-button').addEventListener('click', importYadisk);
    $('own-x-button').addEventListener('click', importX);
}

// ---- Зеркало источника: личный источник → подборка или очередь канала ----

const mirrorState = { channels: [] };

/** Правила из формы; замены — строки «что → на что», галочка «регулярное выражение» относится ко всем. */
function mirrorRulesFromForm() {
    const words = id => $(id).value.split(',').map(w => w.trim()).filter(Boolean);
    const regex = $('mirror-regex').checked;
    const replace = $('mirror-replace').value.split('\n').map(line => line.split(/\s*(?:→|->)\s*/))
        .filter(([from]) => from && from.trim()).map(([from, to = '']) => ({ from: from.trim(), to: to.trim(), regex }));
    return { include: words('mirror-include'), exclude: words('mirror-exclude'), replace,
        strip_links: $('mirror-strip-links').checked, strip_mentions: $('mirror-strip-mentions').checked, strip_hashtags: $('mirror-strip-hashtags').checked,
        append: $('mirror-append').value.trim() };
}

function mirrorRow(m) {
    const mode = m.mode === 'queue' ? `в очередь: ${m.channel_title || 'канал'}` : 'в подборку';
    const checked = m.last_checked_at ? `последняя проверка: ${new Date(m.last_checked_at * 1000).toLocaleString('ru-RU')}` : 'ещё не проверялось';
    return el('li', { className: 'mirror-row' },
        el('span', { className: 'mirror-name', textContent: m.name }),
        el('span', { className: 'best-post-meta', textContent: `${mode} · ${checked}` }),
        el('button', { type: 'button', className: 'mirror-delete', textContent: 'Удалить', onclick: () => deleteMirror(m.id) }));
}

async function loadMirrors() {
    try {
        const data = await api('mirror_list');
        $('mirror-list').replaceChildren(...data.mirrors.map(mirrorRow));
    } catch (error) { $('mirror-status').textContent = error.message; }
}

async function deleteMirror(id) {
    try {
        await api('mirror_delete', { body: { id } });
        await loadMirrors();
    } catch (error) { $('mirror-status').textContent = error.message; }
}

async function saveMirror(event) {
    event.preventDefault();
    const [button, status, mode] = [$('mirror-button'), $('mirror-status'), $('mirror-mode').value];
    button.disabled = true;
    status.textContent = 'Проверяю источник…';
    try {
        const result = await api('mirror_save', { body: { link: $('mirror-link').value.trim(), mode, channel: mode === 'queue' ? $('mirror-channel').value : '', rules: mirrorRulesFromForm() } });
        status.textContent = `Зеркало добавлено: ${result.mirror.name}`;
        $('mirror-link').value = '';
        await loadMirrors();
    } catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; }
}

/** Каналы для очереди — из очереди Telegram; без подключённого Telegram список пуст. */
async function loadMirrorChannels() {
    try {
        const data = await api('telegram_queue', { params: { summary: '1' } });
        mirrorState.channels = data.connected ? data.channels || [] : [];
    } catch { mirrorState.channels = []; }
    $('mirror-channel').replaceChildren(...mirrorState.channels.map(c => el('option', { value: c.id, textContent: c.title || c.chat_title || c.id })));
    updateMirrorMode();
}

function updateMirrorMode() {
    const queue = $('mirror-mode').value === 'queue';
    $('mirror-channel').hidden = !queue;
    if (queue && !mirrorState.channels.length) $('mirror-status').textContent = 'Очередь канала — после подключения Telegram-канала.';
}

function initializeMirror() {
    $('mirror-form').addEventListener('submit', saveMirror);
    $('mirror-mode').addEventListener('change', updateMirrorMode);
}

export function initializeSourceTools() {
    initializeOwnContent();
    initializeOwnFolder();
    initializeBestPosts();
    initializeMirror();
}

export function loadSourceTools() {
    return Promise.all([loadMirrors(), loadMirrorChannels()]);
}
