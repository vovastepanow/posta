// video-cache.js — временная копия роликов в Cache Storage браузера: скачали один раз, живёт час, потом удаляется.
const CACHE_NAME = 'postmaker-videos-v1';
const TTL_MS = 60 * 60 * 1000;
const SWEEP_MS = 5 * 60 * 1000;

const cachedAt = res => Number(res?.headers.get('x-cached-at') || 0);
const storing = new Set();

/** Удаляет ролики старше часа. Без Cache Storage (нет https и не localhost) просто ничего не делает. */
export async function sweepVideoCache() {
    try {
        const cache = await caches.open(CACHE_NAME);
        for (const request of await cache.keys()) {
            if (Date.now() - cachedAt(await cache.match(request)) > TTL_MS) await cache.delete(request);
        }
    } catch { /* кэша нет — ролики просто идут напрямую */ }
}
sweepVideoCache();
setInterval(sweepVideoCache, SWEEP_MS);

/** Свежая копия ролика из кэша как blob-адрес. null — копии нет или она устарела. */
export async function cachedVideoUrl(url) {
    try {
        const cache = await caches.open(CACHE_NAME);
        const res = await cache.match(url);
        if (!res || Date.now() - cachedAt(res) > TTL_MS) return null;
        return URL.createObjectURL(await res.blob());
    } catch {
        return null;
    }
}

/** Скачивает ролик в кэш в фоне, для следующих открытий. Ошибки молча пропускаются: ролик и так играет по сети. */
export async function storeVideo(url) {
    if (storing.has(url)) return;
    storing.add(url);
    try {
        const net = await fetch(url);
        if (!net.ok) return;
        const cache = await caches.open(CACHE_NAME);
        await cache.put(url, new Response(await net.blob(), { headers: { 'x-cached-at': String(Date.now()) } }));
    } catch { /* нет кэша или чужой домен без CORS */ }
    finally { storing.delete(url); }
}

/** Освобождает память, которую держит blob-адрес ролика. Обычные ссылки и null не трогает. */
export function releaseVideoSource(src) {
    if (src?.startsWith('blob:')) URL.revokeObjectURL(src);
}
