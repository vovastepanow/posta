// Browser-only disposable cache. Server copies remain authoritative for scheduled posts.
const PREFIX = 'posta-images-v1-';
const MAX_ENTRIES = 100;
const MAX_BYTES = 50 * 1024 * 1024;
const MAX_IMAGE = 5 * 1024 * 1024;
const TTL = 7 * 24 * 60 * 60 * 1000;
let cacheName = '';
let queue = Promise.resolve();

export function setImageCacheUser(userId) {
    cacheName = /^[a-f0-9]{32}$/.test(userId || '') ? PREFIX + userId : '';
}

function cacheable(url) {
    try {
        const u = new URL(url, location.href);
        return u.origin === location.origin && /\/media_cache\/[^/]+$/.test(u.pathname) && !u.search;
    } catch { return false; }
}

export async function cachedImage(url) {
    const name = cacheName;
    if (!name || !cacheable(url) || !globalThis.caches) return null;
    try {
        const cache = await caches.open(name);
        const res = await cache.match(url);
        if (!res) return null;
        if (Date.now() - Number(res.headers.get('x-posta-cached-at') || 0) > TTL) {
            await cache.delete(url);
            return null;
        }
        return await res.blob();
    } catch { return null; }
}

export function storeImage(url, blob) {
    const name = cacheName;
    if (!name || !cacheable(url) || !globalThis.caches || !blob.type.startsWith('image/') || blob.size > MAX_IMAGE || !blob.size) return;
    queue = queue.catch(() => {}).then(async () => {
        const cache = await caches.open(name);
        await cache.put(url, new Response(blob, { headers: { 'Content-Type': blob.type, 'x-posta-cached-at': String(Date.now()), 'x-posta-size': String(blob.size) } }));
        const keys = await cache.keys();
        let total = 0;
        const entries = [];
        for (const key of keys) {
            const res = await cache.match(key);
            const size = Number(res?.headers.get('x-posta-size') || 0);
            const expired = Date.now() - Number(res?.headers.get('x-posta-cached-at') || 0) > TTL;
            if (expired) await cache.delete(key);
            else { entries.push({ key, size }); total += size; }
        }
        while (entries.length > MAX_ENTRIES || total > MAX_BYTES) {
            const entry = entries.shift();
            total -= entry.size;
            await cache.delete(entry.key);
        }
    }).catch(() => {});
}
