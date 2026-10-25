// media-dedupe.js — один файл картинки в нескольких размерах считается одним: остаётся самый большой вариант.
// Та же логика, что media_dedupe_urls в lib/sources.php (для данных, уже сохранённых в каталоге). Без зависимостей.

const SIZE_SUFFIX = /[_-](?:s|m|x|y|w|z|small|medium|large|thumb|preview)(\.[a-z0-9]+)$/;

/** Адрес разобран или null (строка не адрес). */
function parseUrl(url) {
    try { return new URL(url, 'https://url.invalid/'); } catch { return null; }
}

/** Ключ файла: имя без размерного суффикса; короткое имя — с доменом и путём, длинное (id CDN) — без домена. */
export function mediaFileKey(url) {
    const u = parseUrl(url);
    if (!u) return String(url);
    let path = u.pathname;
    try { path = decodeURIComponent(path); } catch { /* оставляем как есть */ }
    path = path.toLowerCase();
    const name = path.split('/').pop().replace(SIZE_SUFFIX, '$1');
    if (name.length >= 16) return name;
    return u.hostname.toLowerCase() + path.replace(SIZE_SUFFIX, '$1');
}

/** Размер по адресу: число в параметрах (w, width, size) — больше всего; буква размера в имени (s < m < x < y < z < w); без суффикса — оригинал; миниатюры — меньше всех. */
const LETTER_SCORE = { s: 100, m: 200, x: 300, y: 400, z: 500, w: 600 };
export function mediaSizeScore(url) {
    const u = parseUrl(url);
    if (!u) return 1000;
    for (const key of ['w', 'width', 'size']) {
        const value = u.searchParams.get(key);
        if (value !== null && value.trim() !== '' && !Number.isNaN(Number(value))) return 1000 + Math.trunc(Number(value));
    }
    const name = u.pathname.toLowerCase().split('/').pop();
    const letter = name.match(/[_-](s|m|x|y|z|w)\.[a-z0-9]+$/);
    if (letter) return LETTER_SCORE[letter[1]];
    return /thumb|small|preview/.test(name) ? 10 : 1000;
}

/** Адреса без повторов файлов: из группы одного файла — самый большой вариант (при равенстве — самый длинный адрес). Порядок — первого вхождения. */
export function mediaDedupeUrls(urls) {
    const best = new Map();
    for (const url of new Set(urls.filter(u => typeof u === 'string' && u !== ''))) {
        const key = mediaFileKey(url);
        const current = best.get(key);
        const score = mediaSizeScore(url);
        if (current === undefined || score > mediaSizeScore(current) || (score === mediaSizeScore(current) && url.length > current.length)) best.set(key, url);
    }
    return [...best.values()];
}
