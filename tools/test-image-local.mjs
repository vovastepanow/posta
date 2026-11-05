import assert from 'node:assert/strict';
import { prepareOwnImage } from '../js/image-prepare.js';
import { cachedImage, setImageCacheUser, storeImage } from '../js/image-cache.js';

globalThis.location = new URL('https://posta.example/posta/');
const stores = new Map();
globalThis.caches = { async open(name) {
    if (!stores.has(name)) stores.set(name, new Map());
    const map = stores.get(name);
    const key = request => new URL(typeof request === 'string' ? request : request.url, location.href).href;
    return {
        async match(request) { return map.get(key(request))?.clone(); },
        async put(request, response) { map.delete(key(request)); map.set(key(request), response.clone()); },
        async delete(request) { return map.delete(key(request)); },
        async keys() { return [...map.keys()].map(url => new Request(url)); },
    };
} };

const user = 'a'.repeat(32), other = 'b'.repeat(32);
const image = new Blob(['image'], { type: 'image/jpeg' });
setImageCacheUser(user);
storeImage('/posta/media_cache/test.jpg', image);
// storeImage is deliberately asynchronous; wait for its bounded write queue.
for (let n = 0; n < 100 && !(await cachedImage('/posta/media_cache/test.jpg')); n++) await new Promise(r => setTimeout(r, 5));
assert.equal(await (await cachedImage('/posta/media_cache/test.jpg')).text(), 'image');
setImageCacheUser(other);
assert.equal(await cachedImage('/posta/media_cache/test.jpg'), null);
storeImage('https://other.example/media_cache/test.jpg', image);
await new Promise(r => setTimeout(r, 10));
assert.equal(await cachedImage('https://other.example/media_cache/test.jpg'), null);

const small = new File(['x'], 'small.jpg', { type: 'image/jpeg' });
const gif = new File([new Uint8Array(3 * 1024 * 1024)], 'animated.gif', { type: 'image/gif' });
assert.equal(await prepareOwnImage(small), small);
assert.equal(await prepareOwnImage(gif), gif);
let closed = false, dimensions;
globalThis.createImageBitmap = async () => ({ width: 4000, height: 2000, close() { closed = true; } });
globalThis.document = { createElement() { return { width: 0, height: 0, getContext() { return { drawImage: (...args) => { dimensions = args.slice(1); } }; }, toBlob(fn) { fn(new Blob(['resized'], { type: 'image/jpeg' })); } }; } };
const large = new File([new Uint8Array(3 * 1024 * 1024)], 'large.jpg', { type: 'image/jpeg', lastModified: 42 });
const result = await prepareOwnImage(large);
assert.equal(result.name, large.name);
assert.equal(result.lastModified, 42);
assert.deepEqual(dimensions, [0, 0, 2048, 1024]);
assert.ok(result.size < large.size && closed);
globalThis.createImageBitmap = async () => { throw new Error('decode failed'); };
assert.equal(await prepareOwnImage(large), large);
console.log('Browser image cache and preparation checks passed.');
