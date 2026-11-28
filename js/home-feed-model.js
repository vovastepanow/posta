import { recordSlides } from './post-adapters.js';

export const PLATFORMS = { YouTube: '▶', Telegram: '✈', 'ВКонтакте': 'VK', RSS: '◉', 'Двач': '2ch', '4chan': '4ch', 'Одноклассники': 'OK', TikTok: '♪' };
const platformOf = item => {
    const s = String(item.source || '');
    if (/youtu\.?be/.test(String(item.source_url || item.image || ''))) return 'YouTube';
    if (item.board && /^[a-z0-9]+$/.test(item.board)) return item.source_key?.startsWith('fourchan') ? '4chan' : 'Двач';
    const p = s.split(' | ')[0];
    return PLATFORMS[p] ? p : (s.includes('Двач') ? 'Двач' : 'Сайт');
};

export function describeFeedItem(item) {
    const files = recordSlides(item).filter(file => !file.textPost);
    const video = files.some(file => file.video) || Boolean(item.video_via || item.video_pending || item.video_unavailable);
    const platform = platformOf(item);
    return { item, files, video, platform, search: [item.text, item.source, ...(item.hashtags || [])].join(' ').toLowerCase() };
}

const FILTERS = [
    ['all', 'Всё', () => true],
    ['images', 'Картинки', r => r.files.some(file => file.image) && !r.video],
    ['videos', 'Видео', r => r.video],
    ['text', 'Текст', r => !r.files.length && !r.video],
    ['telegram', 'Из Telegram', r => r.platform === 'Telegram'],
    ['boards', 'С имиджбордов', r => ['Двач', '4chan'].includes(r.platform)]
];

// Exact unchanged records keep their identity, allowing live DOM cards to
// survive reloads without restarting their media or replacing handlers.
export function reuseFeedItems(previous, incoming) {
    const old = new Map();
    for (let i = previous.length - 1; i >= 0; i--) {
        const item = previous[i], key = JSON.stringify(item);
        if (!old.has(key)) old.set(key, []);
        old.get(key).push(item);
    }
    return incoming.map(item => old.get(JSON.stringify(item))?.pop() || item);
}

export function buildHomeFeed(items) {
    const rows = items.map(describeFeedItem);
    const groups = new Map(FILTERS.map(([id, , match]) => [id, rows.filter(match)]));
    const filters = FILTERS.map(([id, label]) => ({ id, label, count: groups.get(id).length }));
    const sorted = new Map();
    function visible(filter, sort, query) {
        const key = filter + ':' + sort;
        if (!sorted.has(key)) {
            const order = sort === 'rel' ? (a, b) => (b.item.relevance || 0) - (a.item.relevance || 0)
                : (a, b) => (b.item.timestamp || 0) - (a.item.timestamp || 0);
            sorted.set(key, [...(groups.get(filter) || groups.get('all'))].sort(order));
        }
        const list = sorted.get(key), q = query.toLowerCase();
        return q ? list.filter(row => row.search.includes(q)) : list;
    }
    return { filters, visible };
}
