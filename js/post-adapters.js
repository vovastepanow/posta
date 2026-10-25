// post-adapters.js — приведение постов разных мест приложения к одному формату просмотрщика (post-view.js).
// Без DOM: модуль проверяется в Node (tools/test_post_view.mjs). Вся «грязь» форматов — только здесь.
import { NO_TEXT as PLACEHOLDERS, pluralRu, safeSourcePostUrl, videoPageFor } from './core.js';

const NO_TEXT = 'Текст к посту отсутствует';
const STATUS_BADGE = { queued: { text: 'в очереди', tone: 'accent' }, sending: { text: 'отправляется', tone: 'accent' }, sent: { text: 'опубликован', tone: 'ok' }, telegram: { text: 'опубликован', tone: 'ok' }, failed: { text: 'не вышел', tone: 'bad' } };

/** Слайды записи подбора: картинка или ролик на каждый файл (то же, что галерея карточки). */
export function recordSlides(item, noText = NO_TEXT) {
    if (!item) return [];
    const text = item.text || item.description || noText;
    if (!item.image && !item.display_image && item.text_post) return [{ image: '', fallbackImage: '', video: '', fallbackVideo: '', text: item.text || '', textPost: true }];
    if (item.gallery?.length > 1) return item.gallery.map(f => ({ image: f.display_image || f.image, fallbackImage: f.image, video: f.video ? f.display_video || f.video : '', fallbackVideo: f.video || '', text }));
    if (item.video || item.display_video) return [{ image: item.display_image || item.image || '', fallbackImage: item.image || '', video: item.display_video || item.video, fallbackVideo: item.video || '', text }];
    const images = item.images?.length ? item.images : [item.image].filter(Boolean);
    return images.map((image, i) => ({ image: i === 0 ? item.display_image || image : image, fallbackImage: image, text: item.captions?.[i] || item.text || '' }));
}

/** Медиа одного файла в формате просмотрщика; неизвестные поля не переносятся. */
function media(kind, fields = {}) {
    return { kind, src: '', poster: '', width: 0, height: 0, duration: 0, fallback: '', page: '', pending: false, unavailable: false, ...fields };
}

/** Текст поста: переносы сохраняются; пустой текст и заглушки («Пост без текста», NO_TEXT) — пустая строка. */
const plainText = value => { const text = String(value || '').replace(/\r\n?/g, '\n').trim(); return PLACEHOLDERS.includes(text) ? '' : text; };

/** Пост источника (лента кандидата) → нормализованный пост. */
/** Ролик с адресом, но без файла и без докачки: скачивание ставит просмотрщик, когда пост показан (watchPending). */
export const needsFetch = (m, watchPending) => m.kind === 'video' && !m.src && !m.pending && !m.unavailable && Boolean(videoPageFor(m.page)) && watchPending;

export function fromSourcePost(post, source = {}) {
    const url = safeSourcePostUrl({ source_url: post.url });
    const items = (post.media || []).map(m => media(m.kind === 'video' ? 'video' : 'image', {
        src: m.src || '', poster: m.poster || '', page: m.page || '', pending: Boolean(m.pending), unavailable: Boolean(m.unavailable),
        width: Number(m.width) || 0, height: Number(m.height) || 0, duration: Number(m.duration) || 0 }));
    return {
        key: `source:${post.url || post.posted_at || ''}`,
        source: { name: source.name || '', url: safeSourcePostUrl({ source_url: source.url }), badge: '' },
        date: post.posted_at || null,
        url,
        text: plainText(post.text),
        media: items,
        article: null,
        poll: null,
        stats: { views: post.views ?? null, comments: post.comments ?? null, reactions: post.reactions ?? null },
        badge: null,
        insight: insightOf(source),
    };
}

/** Контекст канала для оценки поста: подписчики, доля подписчиков, видящих пост, постов в неделю, медиана просмотров. Нет данных — null. */
function insightOf({ subscribers, viewRate, perWeek, medianViews } = {}) {
    const n = v => (Number.isFinite(Number(v)) && v !== null && v !== '' ? Number(v) : null);
    const out = { subscribers: n(subscribers) > 0 ? n(subscribers) : null, viewRate: n(viewRate), perWeek: n(perWeek), medianViews: n(medianViews) > 0 ? n(medianViews) : null };
    return Object.values(out).some(v => v !== null) ? out : null;
}

/** Статья записи (vc.ru, dtf.ru и др.) → поле article нормализованного поста; null, если статьи нет. */
function articleOf(item) {
    const a = item?.article;
    if (!a) return null;
    return { title: a.title || '', lead: a.lead || '', site: a.site || '', rubric: a.rubric || '', author: a.author || '', minutes: a.minutes || 0,
        cover: a.cover?.src || '', readUrl: safeSourcePostUrl(item) };
}

/** Запись подбора → нормализованный пост: галерея из слайдов, статья, реакции и просмотры из engagement. */
export function fromRecord(item) {
    const url = safeSourcePostUrl(item);
    const slides = recordSlides(item).filter(s => !s.textPost);
    let firstVideo = true;
    const items = slides.map(s => {
        if (!s.video) return media('image', { src: s.image, fallback: s.fallbackImage });
        const first = firstVideo;
        firstVideo = false;
        // ролик: недоступный — адрес поста, ещё качаемый — адрес ролика (как videoFields в viewer.js)
        const pending = first && Boolean(item.video_via) && !item.video && !item.video_unavailable;
        const unavailable = first && Boolean(item.video_unavailable);
        return media('video', { src: s.video, poster: s.image, fallback: s.fallbackVideo, pending, unavailable,
            page: unavailable ? url : first && pending ? item.video_page || '' : '' });
    });
    const e = item?.engagement || {};
    const reactions = Object.entries(e.reactions || {}).filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1]);
    const stats = { views: e.source === 'telegram' && Number.isFinite(e.views) ? e.views : null, comments: e.source === 'osnova' ? e.comments ?? 0 : null,
        reactions: reactions.length ? reactions : null };
    return {
        key: `record:${item?.id ?? url ?? ''}`,
        source: { name: item?.article?.site || hostOf(url || item?.url), url: url, badge: '' },
        date: Number(item?.posted_at) > 0 ? Number(item.posted_at) : null,
        url,
        text: plainText(item?.text || item?.description),
        media: items,
        article: articleOf(item),
        poll: null,
        stats,
        badge: null,
        insight: insightOf({ medianViews: e.neighbors_median_views }),
    };
}

/** Пост очереди календаря → нормализованный пост; картинка — только первая (thumb). */
export function fromQueuePost(p, channel = {}) {
    const url = safeSourcePostUrl({ source_url: p.url });
    return {
        key: `queue:${p.id}`,
        source: { name: channel.title || p.channel_title || '', url: '', badge: '' },
        date: Number(p.at) > 0 ? Number(p.at) : null,
        url,
        text: plainText(p.full_text ?? p.text),
        media: p.thumb ? [media('image', { src: p.thumb })] : [],
        article: articleOf(p),
        poll: p.poll ? { question: p.poll.question || '', options: (p.poll.options || []).map(o => String(o)) } : null,
        stats: { views: null, comments: null, reactions: null },
        badge: STATUS_BADGE[p.status] || (p.status ? { text: String(p.status), tone: 'muted' } : null),
        insight: null,
    };
}

/** Опубликованный пост журнала → нормализованный пост: текст и ссылка; файлы в журнале не хранятся, только число. */
export function fromPublished(e) {
    const url = safeSourcePostUrl({ source_url: e.url });
    return {
        key: `published:${e.url || e.at || ''}`,
        source: { name: '', url: '', badge: '' },
        date: Number(e.at) > 0 ? Number(e.at) : null,
        url,
        text: plainText(e.text),
        media: [],
        article: null,
        poll: null,
        stats: { views: null, comments: null, reactions: null },
        badge: { text: Number(e.media) > 0 ? `опубликован · файлов: ${e.media}` : 'опубликован', tone: 'ok' },
        insight: null,
    };
}

/** Имя сайта по адресу записи — если статьи нет. */
function hostOf(url) {
    try { return new URL(url || '').hostname.replace(/^www\./, ''); } catch { return ''; }
}

export const CAPTION_LIMIT = 1024;   // подпись к медиа в Telegram
const percent = x => (x >= 0.1 ? Math.round(x * 100) : Math.round(x * 1000) / 10).toString().replace('.', ',');
const clock = s => `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, '0')}`;

/**
 * Что пользователю нужно знать о посте, прежде чем его брать: насколько он «выстрелил», что в нём лежит и что придётся поправить.
 * Без DOM. Возвращает { verdict: {tone, text, hint}|null, metrics: [{label, value, hint}], facts: [строка], warnings: [строка] }.
 */
export function analyzePost(post) {
    const s = post.stats || {}, ins = post.insight || {}, text = post.text || '', media = post.media || [];
    const metrics = [], facts = [], warnings = [];
    let verdict = null;

    if (s.views > 0 && ins.medianViews) {
        const ratio = s.views / ins.medianViews;
        const times = ratio >= 10 ? Math.round(ratio) : Math.round(ratio * 10) / 10;
        const hint = `Просмотры этого поста ÷ медиана просмотров соседних постов канала (${Math.round(ins.medianViews)})`;
        if (ratio >= 1.5) verdict = { tone: 'hot', text: `🔥 Выстрелил: в ${String(times).replace('.', ',')}× больше обычного для канала`, hint };
        else if (ratio >= 0.7) verdict = { tone: 'ok', text: 'Обычный охват для этого канала', hint };
        else verdict = { tone: 'cold', text: `Слабее обычного: ${String(Math.round(ratio * 100))}% от типичных просмотров канала`, hint };
    }
    if (s.views > 0 && ins.subscribers) metrics.push({ label: 'Видели', value: `${percent(Math.min(1, s.views / ins.subscribers))}% подписчиков`, hint: `Просмотры ÷ ${ins.subscribers} подписчиков канала` });
    const reacts = Array.isArray(s.reactions) ? s.reactions.reduce((a, [, n]) => a + n, 0) : typeof s.reactions === 'number' ? s.reactions : 0;
    if (s.views > 0 && reacts > 0) metrics.push({ label: 'Реакции', value: `${percent(reacts / s.views)}% от просмотров`, hint: 'Реакции ÷ просмотры: насколько пост цепляет' });
    if (s.views > 0 && s.comments > 0) metrics.push({ label: 'Обсуждают', value: `${percent(s.comments / s.views)}% от просмотров`, hint: 'Комментарии ÷ просмотры' });
    if (ins.perWeek) metrics.push({ label: 'Канал', value: `~${Math.round(ins.perWeek)} постов в неделю`, hint: '' });
    if (post.date) {
        const hours = (Date.now() / 1000 - post.date) / 3600;
        if (hours > 24 * 365) facts.push('старый пост: больше года');
    }

    const photos = media.filter(m => m.kind === 'image').length, videos = media.filter(m => m.kind === 'video');
    if (photos) facts.push(`${photos} фото`);
    if (videos.length) facts.push(`${videos.length} видео${videos.some(v => v.duration) ? ` · ${videos.filter(v => v.duration).map(v => clock(v.duration)).join(', ')}` : ''}`);
    if (text) facts.push(`текст ${text.length} симв.`);
    const tags = text.match(/#[\p{L}\p{N}_]+/gu) || [];
    if (tags.length) facts.push(`${tags.length} ${pluralRu(tags.length, 'хэштег', 'хэштега', 'хэштегов')}`);

    if (media.length && text.length > CAPTION_LIMIT) warnings.push(`Текст длиннее ${CAPTION_LIMIT} символов (${text.length}): лимит подписи к фото и видео в Telegram — ${CAPTION_LIMIT}. Сократите текст в редакторе.`);
    const tg = text.match(/(?:t\.me|telegram\.me)\/[A-Za-z0-9_+\/]+|(?<![\w@])@[A-Za-z0-9_]{4,}/gi) || [];
    if (tg.length) warnings.push(`В тексте чужие упоминания или ссылки на Telegram (${[...new Set(tg)].slice(0, 3).join(', ')}): при репосте вы рекламируете чужой канал. Уберите или замените на свой.`);
    if (/\berid[:=\s]*[A-Za-z0-9]{6,}|\bреклама\b|#реклама|на правах рекламы/iu.test(text)) warnings.push('Похоже на рекламу (erid или пометка «реклама»): без вашего договора публиковать её нельзя.');
    if (/https?:\/\/\S+/.test(text) && !tg.length) facts.push('есть ссылки');
    if (media.some(m => m.unavailable)) warnings.push('Большое видео не скачано, в редактор попадут только остальные файлы.');
    return { verdict, metrics, facts, warnings };
}
