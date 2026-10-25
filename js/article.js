// article.js — читалка статьи: панель поверх приложения (на телефоне — весь экран). Блоки приходят из article_read.
// Текст сайта выводится только через textContent; ссылки — из списка links блока, адреса https.
import { $, api, el, safeSourcePostUrl } from './core.js';
import { articleCover, articleSite, siteBadge } from './card.js';
import { openViewerGallery } from './viewer.js';

let reader = null;        // открытая читалка: { item, opener, scrollY, onPost }
let bound = false;

/** Текст абзаца с ссылками: куски текста и ссылки по порядку их появления. */
function withLinks(text, links = []) {
    const out = [];
    let rest = String(text || '');
    for (const [label, href] of links) {
        const at = rest.indexOf(label);
        if (at < 0 || !String(href).startsWith('https://')) continue;
        if (at > 0) out.push(rest.slice(0, at));
        out.push(el('a', { href, target: '_blank', rel: 'noopener noreferrer', textContent: label }));
        rest = rest.slice(at + label.length);
    }
    if (rest) out.push(rest);
    return out;
}

/** Картинка блока: клик — просмотрщик со всеми картинками статьи. */
function imageNode(block, images) {
    const index = images.indexOf(block);
    const img = el('img', { src: block.src, alt: block.caption || 'Картинка из статьи', loading: 'lazy', decoding: 'async' });
    img.onerror = () => { img.hidden = true; };
    const view = el('button', { type: 'button', className: 'ar-image', title: 'Открыть картинку', onclick: () => openViewerGallery(images.map(i => ({ kind: 'image', src: i.src, alt: i.caption || 'Картинка из статьи' })), index) }, img);
    return el('figure', { className: 'ar-figure' }, view, block.caption ? el('figcaption', { textContent: block.caption }) : null);
}

/** Один блок статьи. Незнакомые типы пропускаются. */
export function blockNode(block, images) {
    switch (block.type) {
        case 'h': return el('h3', { className: 'ar-h', textContent: block.text });
        case 'p': return el('p', { className: 'ar-p' }, ...withLinks(block.text, block.links));
        case 'list': return el('ul', { className: 'ar-list' }, ...block.items.map(i => el('li', {}, ...withLinks(i.text, i.links))));
        case 'quote': return el('blockquote', { className: 'ar-quote', textContent: block.text });
        case 'image': return imageNode(block, images);
        case 'video': return el('a', { className: 'ar-embed', href: block.url, target: '_blank', rel: 'noopener noreferrer', textContent: '▶ Видео в статье — смотреть на сайте ↗' });
        case 'embed': return el('div', { className: 'ar-embed is-card' }, el('b', { textContent: block.title }), block.description ? el('span', { textContent: block.description }) : null);
        default: return null;
    }
}

/** Скелет на время загрузки. */
function skeleton(title) {
    return el('div', { className: 'ar-skeleton', 'aria-busy': 'true' },
        el('h1', { className: 'ar-title', textContent: title || 'Загружаем статью…' }),
        el('span', { className: 'ar-bar' }), el('span', { className: 'ar-bar' }), el('span', { className: 'ar-bar is-short' }));
}

/** Статья с сервера: обложка, заголовок, мета и блоки. */
function renderArticle(item, data) {
    const a = data.article || {};
    const images = (a.blocks || []).filter(b => b.type === 'image');
    const meta = [articleSite(item), a.rubric, a.author, a.minutes ? `⏱ ${a.minutes} мин` : ''].filter(Boolean).join(' · ');
    return el('article', { className: 'ar-article' },
        a.cover?.src ? articleCover({ ...item, article: { ...item.article, cover: a.cover } }) : null,
        el('div', { className: 'ar-meta' }, siteBadge(articleSite(item)), el('span', { textContent: meta })),
        el('h1', { className: 'ar-title', id: 'article-reader-title', textContent: a.title || item.article?.title || '' }),
        ...(a.blocks || []).map(b => blockNode(b, images)),
        (a.blocks || []).length ? null : el('p', { className: 'ar-note', textContent: 'На странице не нашлось текста статьи. Откройте её на сайте.' }));
}

/** Tab не уходит за панель: фокус по кругу внутри читалки. */
function trapFocus(event) {
    const items = [...$('article-reader').querySelectorAll('button, a[href]')].filter(el => !el.disabled && !el.hidden);
    if (!items.length) return;
    const first = items[0], last = items[items.length - 1];
    if (event.shiftKey && (document.activeElement === first || !$('article-reader').contains(document.activeElement))) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
}

/** Закрыть читалку: вернуть фокус и прокрутку к карточке. fromHistory — закрытие по кнопке «назад». */
export function closeArticleReader(fromHistory = false) {
    if (!reader) return;
    const { opener, scrollY } = reader;
    reader = null;
    $('article-reader').hidden = true;
    document.body.classList.remove('is-reading');
    if (!fromHistory && history.state?.articleReader) history.back();   // запись истории снимаем; popstate закроет повторно — без эффекта
    opener?.focus?.({ preventScroll: true });
    window.scrollTo(0, scrollY);
}

function bind() {
    if (bound) return;
    bound = true;
    $('article-reader-close').addEventListener('click', () => closeArticleReader());
    $('article-reader').addEventListener('click', event => { if (event.target.id === 'article-reader') closeArticleReader(); });   // клик по затемнению
    $('article-reader-post').addEventListener('click', () => reader?.onPost?.());
    document.addEventListener('keydown', event => {
        if (!reader) return;
        if (event.key === 'Escape') closeArticleReader();
        if (event.key === 'Tab') trapFocus(event);
    });
    window.addEventListener('popstate', () => { if (reader) closeArticleReader(true); });   // «назад» браузера закрывает читалку
    const sheet = $('article-reader').querySelector('.article-reader-sheet');
    let startY = null;
    sheet.addEventListener('touchstart', event => { startY = sheet.scrollTop <= 0 ? event.touches[0].clientY : null; }, { passive: true });
    sheet.addEventListener('touchend', event => { if (startY !== null && event.changedTouches[0].clientY - startY > 90) closeArticleReader(); startY = null; });   // свайп вниз на телефоне
}

/** Открыть статью записи: скелет, затем текст с сервера. onPost — кнопка «Запостить» в читалке. */
export async function openArticleReader(item, { onPost = null } = {}) {
    bind();
    closeArticleReader(false);
    const url = safeSourcePostUrl(item) || '';
    reader = { item, opener: document.activeElement, scrollY: window.scrollY, onPost };
    $('article-reader-site').textContent = articleSite(item);
    const open = $('article-reader-open');
    open.hidden = !url;
    if (url) open.href = url;
    const body = $('article-reader-body');
    body.replaceChildren(skeleton(item.article?.title));
    body.scrollTop = 0;
    $('article-reader').hidden = false;
    document.body.classList.add('is-reading');
    history.pushState({ articleReader: true }, '');
    $('article-reader-close').focus({ preventScroll: true });
    const current = reader;
    try {
        const data = await api('article_read', { params: { url } });
        if (reader !== current) return;   // уже закрыта или открыта другая статья
        body.replaceChildren(renderArticle(item, data));
    } catch (error) {
        if (reader !== current) return;
        body.replaceChildren(el('p', { className: 'ar-note is-error', textContent: `Не удалось открыть статью: ${error.message}` }));
    }
}
