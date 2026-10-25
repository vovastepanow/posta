// card.js — общие части карточек записи: вид записи (медиа, текст, статья), обрезка текста, карточка статьи.
// Все места показа (главная карточка, предложка, календарь, редактор) берут вид и тексты отсюда.
import { el, safeSourcePostUrl } from './core.js';
import { openViewerGallery } from './viewer.js';

/** Вид записи: статья (есть article), текстовый пост без картинки или медиа. */
export function cardKind(item) {
    if (item?.article) return 'article';
    if (item && !item.image && !item.display_image && item.text_post) return 'text';
    return 'media';
}

/** Текст обрезается по границе слова, не посреди слова. */
export function shortText(text, chars) {
    const clean = String(text || '').trim();
    if (clean.length <= chars) return clean;
    const cut = clean.slice(0, chars);
    const at = cut.lastIndexOf(' ');
    return `${(at > chars * 0.6 ? cut.slice(0, at) : cut).replace(/[\s,.;:–—-]+$/u, '')}…`;
}

/** Текст в заданное число строк с кнопкой «Показать полностью» — только если текст действительно обрезан. */
export function clampText(text, lines = 6, className = 'clamp-text') {
    const body = el('p', { className: `${className}-body`, textContent: text, style: `-webkit-line-clamp: ${lines}` });
    const more = el('button', { type: 'button', className: `${className}-more`, textContent: 'Показать полностью', hidden: true });
    more.addEventListener('click', () => {
        const open = body.classList.toggle('is-expanded');
        more.textContent = open ? 'Свернуть' : 'Показать полностью';
    });
    requestAnimationFrame(() => { more.hidden = body.scrollHeight <= body.clientHeight + 2 && !body.classList.contains('is-expanded'); });
    return el('div', { className }, body, more);
}

/** Сайт записи: из статьи, иначе адрес записи. */
export function articleSite(item) {
    if (item.article?.site) return item.article.site;
    try { return new URL(item.url || '').hostname.replace(/^www\./, ''); } catch { return ''; }
}

/** Подпись для публикации (то же, что news_caption() на сервере): заголовок, до трёх предложений выжимки, источник со ссылкой. */
export function articleCaption(item) {
    const a = item.article || {};
    const url = safeSourcePostUrl(item) || '';
    const lines = String(item.text || '').split('\n').map(l => l.trim()).filter(Boolean);
    const title = a.title || lines[0] || '';
    const lead = a.lead ?? lines.slice(1).join(' ');
    const excerpt = String(lead).split(/(?<=[.!?])\s+/u).slice(0, 3).join(' ');
    return [title && `**${title}**`, excerpt, `Источник: ${articleSite(item)}${url ? ` — ${url}` : ''}`].filter(Boolean).join('\n');
}

/** Значок сайта: первая буква в кружке; цвет — один из четырёх тонов темы, выбран по имени сайта. */
export function siteBadge(site) {
    const letter = String(site || '?').replace(/^www\./, '').charAt(0).toUpperCase();
    const tone = [...String(site || '')].reduce((sum, ch) => sum + ch.charCodeAt(0), 0) % 4;
    return el('span', { className: `site-badge tone-${tone}`, textContent: letter, 'aria-hidden': 'true' });
}

/** Обложка статьи 16:9; клик — просмотрщик. Нет обложки — плашка с названием сайта, не пустое место. */
export function articleCover(item) {
    const src = item.article?.cover?.src || '';
    const site = articleSite(item);
    if (!src) return el('div', { className: 'article-cover is-plate' }, siteBadge(site), el('span', { className: 'article-plate-name', textContent: site }));
    const img = el('img', { src, alt: item.article?.title || 'Обложка статьи', loading: 'lazy', decoding: 'async' });
    img.onerror = () => { img.replaceWith(el('span', { className: 'article-plate-name', textContent: site })); };
    const button = el('button', { type: 'button', className: 'article-cover', title: 'Открыть картинку', onclick: event => { event.stopPropagation(); openViewerGallery([{ kind: 'image', src, alt: 'Обложка статьи' }], 0); } }, img);
    return button;
}

/** Строка «сайт · рубрика · автор · ⏱ N мин». */
export function articleMeta(item) {
    const a = item.article || {};
    const parts = [articleSite(item), a.rubric, a.author, a.minutes ? `⏱ ${a.minutes} мин` : ''].filter(Boolean);
    return el('div', { className: 'article-meta' }, siteBadge(articleSite(item)), el('span', { textContent: parts.join(' · ') }));
}

/** Карточка статьи для главной карточки: обложка, мета, заголовок, анонс и кнопка «Читать статью». */
export function articleCard(item, onRead) {
    const a = item.article || {};
    const title = a.title || String(item.text || '').split('\n')[0];
    return el('div', { className: 'article-card' },
        articleCover(item),
        el('div', { className: 'article-body' },
            articleMeta(item),
            el('h2', { className: 'article-title', textContent: title }),
            a.lead ? el('p', { className: 'article-lead', textContent: a.lead }) : null,
            el('button', { type: 'button', className: 'article-read', textContent: 'Читать статью', onclick: onRead }),
            el('p', { className: 'article-hint', textContent: 'В пост пойдёт: заголовок, выжимка и ссылка на источник.' })));
}

/** Краткий признак статьи для очереди: заголовок, анонс, сайт, рубрика, автор, минуты — без блоков и HTML. */
export function articleBrief(item) {
    const a = item.article || {};
    return { title: a.title || '', lead: String(a.lead || '').slice(0, 400), site: articleSite(item), rubric: a.rubric || '', author: a.author || '', minutes: a.minutes || 0 };
}
