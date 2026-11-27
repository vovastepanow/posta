import assert from 'node:assert/strict';
import { collectSourcePages } from '../js/source-pages.js';

const pages = {
    '': { sources: [{ source_id: 'a', name: 'old' }, { source_id: 'b' }], next_after: '1' },
    '1': { sources: [{ source_id: 'a', name: 'new' }, { source_id: 'c' }, {}] }
};
const result = await collectSourcePages(async after => pages[after], { state: 'candidate' });
assert.deepEqual(result, { sources: [{ source_id: 'a', name: 'new' }, { source_id: 'b' }, { source_id: 'c' }], snapshotAvailable: true });
assert.deepEqual(await collectSourcePages(async () => ({ snapshot_available: false }), { state: 'found' }), { sources: [], snapshotAvailable: false });
await assert.rejects(collectSourcePages(async () => ({ next_after: '1' }), { state: 'active' }), /пагинацию/);
await assert.rejects(collectSourcePages(async after => ({ next_after: after === '1' ? '2' : '1' })), /пагинацию/);
await assert.rejects(collectSourcePages(async () => ({ next_after: '1' }), { maxPages: 1 }), /пагинацию/);
const controller = new AbortController();
await assert.rejects(collectSourcePages(async () => { controller.abort(); return { sources: [] }; }, { signal: controller.signal }), { name: 'AbortError' });
console.log('Source pagination checks passed.');
